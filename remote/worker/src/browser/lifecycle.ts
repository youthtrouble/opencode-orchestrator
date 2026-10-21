// Answers and actions share one lifecycle (plan D6.1-2): a tap creates the row once per client_key, it
// can be undone until its undo window ends, then the Mac pulls it and its ack settles it.
import type { WorkerEnv } from "../env.ts";
import { HttpError } from "../http.ts";
import type { Outcome } from "../machine/wire.ts";
import type { CountedDb } from "../queries.ts";
import type { LiveStatus } from "./wire.ts";

const DEFAULT_UNDO_SECONDS = 10;
const MIN_UNDO_SECONDS = 1;
const MAX_UNDO_SECONDS = 60;

/** The fields of an `answers` or `actions` row that say where it stands. */
export interface Lifecycle {
  apply_after: string;
  cancelled_at: string | null;
  delivered_at: string | null;
  outcome: Outcome | null;
  outcome_detail: string | null;
}

export function undoSeconds(env: WorkerEnv): number {
  const configured = Number.parseInt(env.UNDO_SECONDS ?? "", 10);
  if (Number.isNaN(configured)) return DEFAULT_UNDO_SECONDS;
  return Math.min(MAX_UNDO_SECONDS, Math.max(MIN_UNDO_SECONDS, configured));
}

export function applyAfter(now: Date, env: WorkerEnv): string {
  return new Date(now.getTime() + undoSeconds(env) * 1000).toISOString();
}

export function liveStatus(row: Lifecycle, now: Date): { status: LiveStatus; detail: string | null } {
  if (row.cancelled_at !== null) return { status: "rejected", detail: "undone" };
  if (row.outcome !== null) return { status: row.outcome, detail: row.outcome_detail };
  if (now.toISOString() < row.apply_after) return { status: "undo_window", detail: null };
  // Past its window the row can no longer be undone and is the Mac's to apply, pulled yet or not.
  return { status: "sent", detail: null };
}

/**
 * Runs an `INSERT ... ON CONFLICT(client_key) DO NOTHING` and returns whether it created the row, so a
 * retried tap changes nothing. A distinct tap that would make a second live row violates the table's
 * partial unique index instead; `findLive` then names the row it collided with, for a 409.
 */
export async function insertOnce<T>(
  insert: D1PreparedStatement,
  findLive: () => Promise<T | null>,
  conflict: (live: T) => HttpError,
): Promise<boolean> {
  try {
    return (await insert.run()).meta.changes === 1;
  } catch (error) {
    const live = await findLive();
    if (live === null) throw error;
    throw conflict(live);
  }
}

/** Cancels the row only inside its undo window and before the Mac has it; returns whether it did. */
export async function cancelWithinWindow(
  db: CountedDb,
  table: "answers" | "actions",
  id: string,
  now: Date,
): Promise<boolean> {
  const at = now.toISOString();
  const result = await db
    .prepare(
      `UPDATE ${table} SET cancelled_at = ?
       WHERE id = ? AND ? < apply_after AND delivered_at IS NULL AND cancelled_at IS NULL`,
    )
    .bind(at, id, at)
    .run();
  return result.meta.changes === 1;
}

export function tooLate(row: Lifecycle): HttpError {
  return new HttpError(409, "too_late", row.cancelled_at === null ? "the undo window has passed" : "already undone");
}
