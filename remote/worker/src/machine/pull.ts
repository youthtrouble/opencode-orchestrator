// GET /api/v1/machine/pull (plan D6.2-3, D7): the answers and actions whose undo window has passed and
// that the Mac has not settled yet. Delivery is at least once; the Mac's ack makes it exactly once.
import { jsonResponse } from "../http.ts";
import type { HandlerContext } from "../routes.ts";

/** How long the Mac waits before its next pull; the schema clamps it to 15..300. */
export const POLL_AFTER_SECONDS = 30;
const PULL_LIMIT = 50;
// last_pull_at only feeds the page's "Mac seen" line, so it is refreshed at most once a minute to
// keep a pull every 30 s from costing a D1 row write each time (plan D7).
const LAST_PULL_RESOLUTION_SECONDS = 60;

interface PulledAnswer {
  id: string;
  project_key: string;
  question_id: string;
  kind: string;
  text: string;
  created_at: string;
}

interface PulledAction {
  id: string;
  project_key: string;
  kind: string;
  created_at: string;
}

// Times are compared as text: every stored time is the Worker's own `toISOString()`, which sorts
// in time order.
const DUE_ANSWERS = `
  SELECT answers.id, questions.project_key, answers.question_id, answers.kind, answers.text, answers.created_at
  FROM answers JOIN questions ON questions.id = answers.question_id
  WHERE answers.apply_after <= ? AND answers.cancelled_at IS NULL AND answers.outcome IS NULL
  ORDER BY answers.created_at, answers.id
  LIMIT ${PULL_LIMIT}`;

const DUE_ACTIONS = `
  SELECT id, project_key, kind, created_at
  FROM actions
  WHERE apply_after <= ? AND cancelled_at IS NULL AND outcome IS NULL
  ORDER BY created_at, id
  LIMIT ${PULL_LIMIT}`;

export async function pullHandler({ db, now }: HandlerContext): Promise<Response> {
  const at = now.toISOString();
  const answers = (await db.prepare(DUE_ANSWERS).bind(at).all<PulledAnswer>()).results;
  const actions = (await db.prepare(DUE_ACTIONS).bind(at).all<PulledAction>()).results;
  await db.batch([
    markDelivered(db, "answers", answers, at),
    markDelivered(db, "actions", actions, at),
    recordPull(db, now),
  ]);
  return jsonResponse(200, { v: 1, server_time: at, poll_after_seconds: POLL_AFTER_SECONDS, answers, actions });
}

/** Stamps the first delivery only; a re-pull of an unacked row leaves delivered_at as it was. */
function markDelivered(db: D1Database, table: "answers" | "actions", rows: { id: string }[], at: string) {
  return db
    .prepare(`UPDATE ${table} SET delivered_at = ? WHERE delivered_at IS NULL AND id IN (SELECT value FROM json_each(?))`)
    .bind(at, JSON.stringify(rows.map((row) => row.id)));
}

// A pull carries no machine id, and one service token stands for the owner's machines, so the
// timestamp goes on every machine row.
function recordPull(db: D1Database, now: Date) {
  const staleBefore = new Date(now.getTime() - LAST_PULL_RESOLUTION_SECONDS * 1000).toISOString();
  return db
    .prepare("UPDATE machines SET last_pull_at = ? WHERE last_pull_at IS NULL OR last_pull_at <= ?")
    .bind(now.toISOString(), staleBefore);
}
