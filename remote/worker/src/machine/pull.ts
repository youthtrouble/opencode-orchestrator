// GET /api/v1/machine/pull (plan D6.2-3, D7): the answers and actions whose undo window has passed and
// that the Mac has not settled yet. Delivery is at least once; the Mac's ack makes it exactly once.
import { jsonResponse } from "../http.ts";
import type { CountedDb } from "../queries.ts";
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
// in time order. Each FROM clause binds the pull's time once; the SELECT and the delivery stamp share
// it, so both see the same rows.
const DUE_ANSWERS_FROM = `
  FROM answers JOIN questions ON questions.id = answers.question_id
  WHERE answers.apply_after <= ? AND answers.cancelled_at IS NULL AND answers.outcome IS NULL
  ORDER BY answers.created_at, answers.id
  LIMIT ${PULL_LIMIT}`;

const DUE_ACTIONS_FROM = `
  FROM actions
  WHERE apply_after <= ? AND cancelled_at IS NULL AND outcome IS NULL
  ORDER BY created_at, id
  LIMIT ${PULL_LIMIT}`;

export async function pullHandler({ db, now }: HandlerContext): Promise<Response> {
  const at = now.toISOString();
  // One batch is one D1 transaction, so an Undo commits wholly before it (the row is cancelled, so
  // neither selected nor stamped) or wholly after (the row is delivered, so the Undo is refused). The
  // reply is built only from rows read inside the transaction that stamps them.
  const [answers, actions] = await db.batch([
    db
      .prepare(`SELECT answers.id, questions.project_key, answers.question_id, answers.kind, answers.text,
        answers.created_at ${DUE_ANSWERS_FROM}`)
      .bind(at),
    db.prepare(`SELECT id, project_key, kind, created_at ${DUE_ACTIONS_FROM}`).bind(at),
    markDelivered(db, "answers", `SELECT answers.id ${DUE_ANSWERS_FROM}`, at),
    markDelivered(db, "actions", `SELECT id ${DUE_ACTIONS_FROM}`, at),
    recordPull(db, now),
  ]);
  return jsonResponse(200, {
    v: 1,
    server_time: at,
    poll_after_seconds: POLL_AFTER_SECONDS,
    answers: answers.results as PulledAnswer[],
    actions: actions.results as PulledAction[],
  });
}

/**
 * Stamps the first delivery of the rows `dueIds` selects; a re-pull of an unacked row leaves
 * delivered_at as it was.
 */
function markDelivered(db: CountedDb, table: "answers" | "actions", dueIds: string, at: string) {
  return db
    .prepare(`UPDATE ${table} SET delivered_at = ? WHERE delivered_at IS NULL AND id IN (${dueIds})`)
    .bind(at, at);
}

// A pull carries no machine id, and one service token stands for the owner's machines, so the
// timestamp goes on every machine row.
function recordPull(db: CountedDb, now: Date) {
  const staleBefore = new Date(now.getTime() - LAST_PULL_RESOLUTION_SECONDS * 1000).toISOString();
  return db
    .prepare("UPDATE machines SET last_pull_at = ? WHERE last_pull_at IS NULL OR last_pull_at <= ?")
    .bind(now.toISOString(), staleBefore);
}
