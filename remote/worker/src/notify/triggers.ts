// When a push lands, tell the owner about what now needs them (plan D8): a blocking question owed a
// notification (`notify_state = 'pending'`), or a project in `paused` or `failed` it has not been told
// about (`notified_state`). Each is claimed in D1 before it is sent, so it is sent at most once, never
// retried, and never more than DAILY_EMAIL_CAP times a day in all.
//
// D1 budget: the notifier runs inside the push's invocation (waitUntil does not start a new one), so its
// queries add to the push's own (see machine/push.ts) under the same 50-per-invocation Free plan limit.
// It therefore works on sets, never per notification: one batch of 4 statements claims what is owed and
// reserves its daily slots, and at most 1 statement afterwards records what was not sent, so a push costs
// at most NOTIFY_QUERIES_PER_PUSH more. A claim marks questions `sent` before the send, so the final write
// must not be cut off by the limit: the notifier claims only when the invocation still has all
// NOTIFY_QUERIES_PER_PUSH queries left (see queries.ts), and otherwise leaves everything owed. Whatever
// is still owed is claimed by the next accepted push, not only what that push saw first. The claim
// batch is one transaction, so if it fails nonetheless nothing is claimed.
import type { OnPushAccepted } from "../machine/push.ts";
import type { ProjectState } from "../machine/wire.ts";
import type { HandlerContext } from "../routes.ts";
import type { Notification, Notifier } from "./notifier.ts";

export const DAILY_EMAIL_CAP = 20;
export const NOTIFY_QUERIES_PER_PUSH = 5;

// The sentence each notification says. Never the question text (owner decision Q3).
const STATE_SENTENCES: Partial<Record<ProjectState, string>> = { paused: "is paused", failed: "failed" };
const NEEDS_YOU = "needs you";

// At most a day's worth of each is claimed per push; the rest stays owed for the next push.
const OWED_QUESTIONS = `SELECT id FROM questions
  WHERE notify_state = 'pending' AND blocking = 1 AND closed_at IS NULL
  ORDER BY first_seen_at, id LIMIT ${DAILY_EMAIL_CAP}`;
const OWED_STATES = `SELECT key FROM projects
  WHERE state IN (${Object.keys(STATE_SENTENCES).map((state) => `'${state}'`).join(", ")})
    AND notified_state IS NOT state
  ORDER BY state_since, key LIMIT ${DAILY_EMAIL_CAP}`;

interface Alert {
  projectKey: string;
  projectName: string;
  sentence: string;
  /** Set for a question's alert: a question records what became of its claim. */
  questionId?: string;
}

interface ClaimedQuestion {
  id: string;
  project_key: string;
  project_name: string;
  first_seen_at: string;
}

interface ClaimedState {
  key: string;
  name: string;
  state: ProjectState;
  state_since: string;
}

/** A claimed question that was not sent: over the cap (`suppressed`) or refused by the sender (`failed`). */
type UnsentState = "suppressed" | "failed";

/** The push hook: notifications run after the response, inside waitUntil, so a slow send never delays a push. */
export function notifyOnAccepted(notifierFor: (ctx: HandlerContext) => Notifier): OnPushAccepted {
  return async (ctx) => {
    const notifier = notifierFor(ctx);
    ctx.waitUntil(
      notifyOwed(ctx, notifier).catch((error: unknown) => {
        // Only D1 can fail here (sendAlert handles a failed send), and its messages quote no address.
        const detail = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
        console.log(JSON.stringify({ notify: "error", detail }));
      }),
    );
  };
}

async function notifyOwed(ctx: HandlerContext, notifier: Notifier): Promise<void> {
  const queriesLeft = ctx.queries.remaining();
  if (queriesLeft < NOTIFY_QUERIES_PER_PUSH) {
    console.log(JSON.stringify({ notify: "deferred", queries_left: queriesLeft }));
    return;
  }
  const { alerts, slots } = await claimOwed(ctx);
  const unsent: Record<string, UnsentState> = {};
  for (const [index, alert] of alerts.entries()) {
    const outcome = index < slots ? await sendAlert(ctx, notifier, alert) : capped(alert);
    // A capped or failed state alert keeps its claim (no retry); a question records why it was not sent.
    if (outcome !== "sent" && alert.questionId !== undefined) unsent[alert.questionId] = outcome;
  }
  if (Object.keys(unsent).length > 0) await recordUnsent(ctx, unsent);
}

/**
 * In one transaction: reserves a daily slot for each owed alert (as many as the cap leaves), then claims
 * them. Returns the claimed alerts, questions first, and how many of them have a slot.
 */
async function claimOwed({ db, now }: HandlerContext): Promise<{ alerts: Alert[]; slots: number }> {
  const day = now.toISOString().slice(0, 10);
  const owedCount = `(SELECT count(*) FROM (${OWED_QUESTIONS})) + (SELECT count(*) FROM (${OWED_STATES}))`;
  const [before, after, questions, states] = await db.batch([
    db.prepare("SELECT coalesce((SELECT sent FROM notify_log WHERE day = ?), 0) AS sent").bind(day),
    db
      .prepare(
        `INSERT INTO notify_log (day, sent) SELECT ?, min(${DAILY_EMAIL_CAP}, ${owedCount})
         ON CONFLICT(day) DO UPDATE SET sent = min(${DAILY_EMAIL_CAP}, sent + excluded.sent)
         RETURNING sent`,
      )
      .bind(day),
    db.prepare(
      `UPDATE questions SET notify_state = 'sent' WHERE id IN (${OWED_QUESTIONS})
       RETURNING id, project_key, first_seen_at,
         (SELECT name FROM projects WHERE projects.key = questions.project_key) AS project_name`,
    ),
    db.prepare(
      `UPDATE projects SET notified_state = state WHERE key IN (${OWED_STATES})
       RETURNING key, name, state, state_since`,
    ),
  ]);
  const slots = sentOf(after) - sentOf(before);
  return { alerts: [...questionAlerts(questions.results), ...stateAlerts(states.results)], slots };
}

function sentOf(result: D1Result): number {
  return (result.results[0] as { sent: number }).sent;
}

// RETURNING gives no order, so each list is put back in the order it was claimed in.
function questionAlerts(rows: unknown[]): Alert[] {
  return (rows as ClaimedQuestion[])
    .sort((a, b) => compare(a.first_seen_at, b.first_seen_at) || compare(a.id, b.id))
    .map((row) => ({
      projectKey: row.project_key,
      projectName: row.project_name,
      sentence: NEEDS_YOU,
      questionId: row.id,
    }));
}

function stateAlerts(rows: unknown[]): Alert[] {
  return (rows as ClaimedState[])
    .sort((a, b) => compare(a.state_since, b.state_since) || compare(a.key, b.key))
    .map((row) => ({ projectKey: row.key, projectName: row.name, sentence: STATE_SENTENCES[row.state]! }));
}

function compare(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function capped(alert: Alert): UnsentState {
  console.log(JSON.stringify({ notify: "daily_cap_reached", key: alert.projectKey }));
  return "suppressed";
}

async function sendAlert(ctx: HandlerContext, notifier: Notifier, alert: Alert): Promise<"sent" | UnsentState> {
  try {
    await notifier.send(notification(alert.projectName, alert.sentence, ctx.cfg.publicOrigin));
    return "sent";
  } catch (error) {
    console.log(JSON.stringify({ notify: "send_failed", key: alert.projectKey, detail: errorKind(error) }));
    return "failed";
  }
}

/** One statement for every claimed question that was not sent, whatever their number. */
async function recordUnsent({ db }: HandlerContext, unsent: Record<string, UnsentState>): Promise<void> {
  await db
    .prepare(
      `UPDATE questions SET notify_state = unsent.value
       FROM json_each(?) AS unsent WHERE questions.id = unsent.key`,
    )
    .bind(JSON.stringify(unsent))
    .run();
}

function notification(projectName: string, sentence: string, publicOrigin: string): Notification {
  const line = `Work Trains: ${singleLine(projectName)} ${sentence}`;
  return { subject: line, text: `${line}\n${publicOrigin}\n` };
}

// The name comes from the Mac and lands in a mail header; a control character there could break it.
function singleLine(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f]+/g, " ");
}

/** The error's kind only: a send failure's message could quote an address. */
function errorKind(error: unknown): string {
  return error instanceof Error ? error.name : typeof error;
}
