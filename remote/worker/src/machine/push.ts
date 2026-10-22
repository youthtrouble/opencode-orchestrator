// POST /api/v1/machine/push (plan D6.5, D7): stores a changed project snapshot at most once per
// DEBOUNCE_SECONDS and keeps the project's questions in step with it. An unchanged hash writes nothing.
//
// D1 cost: every statement counts against the per-invocation query limit, batched or not. A push costs
// 2 queries (3 on a machine's first push), plus 1 per project (its read), plus, per accepted project,
// 2 more (1 when new) and one statement per question written. The Worker assumes the Free plan's 50
// queries per invocation (queries.ts), well short of a 50-project push with questions, so a push takes
// the projects that fit and answers `deferred` (retry in 1 s) for the rest, which the Mac sends again
// at its next poll. It keeps in hand the queries that must run after its projects: the first-push
// stamp's and the OnPushAccepted hook's (the notifier's NOTIFY_QUERIES_PER_PUSH; see reservedQueries).
import { readJsonBody } from "../body.ts";
import { jsonResponse } from "../http.ts";
import { parseBody } from "../protocol.ts";
import type { CountedDb } from "../queries.ts";
import type { HandlerContext } from "../routes.ts";
import type { ProjectPushResult, ProjectSnapshot, ProjectState, PushRequest, PushResult, Question, QuestionAsk } from "./wire.ts";

/** A little under the Mac's own 60 s spacing, so clock skew between the two never defers an honest push. */
export const DEBOUNCE_SECONDS = 55;
// The schema caps retry_after_seconds; only a Worker clock that stepped back could ask for more.
const MAX_RETRY_AFTER_SECONDS = 300;

export interface AcceptedProject {
  project: ProjectSnapshot;
  /** Questions this push stored for the first time (marked `suppressed` on a machine's first push). */
  newQuestions: Question[];
  /** The state before this push, or null when the project is new. */
  previousState: ProjectState | null;
}

/**
 * Runs once per push, after every project has landed, with the projects it accepted (never empty).
 * Once per push rather than per project, so its D1 cost does not grow with the push. WT9's notifier
 * implements it.
 */
export interface OnPushAccepted {
  /** The most D1 queries `run` issues; a push keeps that many for it (see reservedQueries). */
  maxQueries: number;
  run: (ctx: HandlerContext, accepted: AcceptedProject[]) => Promise<void>;
}

interface PushRun {
  ctx: HandlerContext;
  push: PushRequest;
  isFirstPush: boolean;
  /** Unique to this request; the project rows it writes carry it (see THIS_PUSH_WROTE_PROJECT). */
  id: string;
  /** The most queries the OnPushAccepted hook issues. */
  hookQueries: number;
  accepted: AcceptedProject[];
  /** How many projects this push deferred because their queries did not fit. */
  deferredForBudget: number;
}

interface StoredProject {
  machine_id: string;
  state: ProjectState;
  state_since: string;
  snapshot_hash: string;
  received_at: string;
}

interface StoredQuestion {
  id: string;
  asked_at: string;
  asked_by: string;
  subject: string;
  body: string;
  recommended: string | null;
  blocking: number;
  ask: QuestionAsk | null;
  closed_at: string | null;
}

interface Decision {
  result: PushResult;
  retryAfterSeconds: number | null;
  /** Set when the refusal is worth a line in the Worker log. */
  logReason?: string;
}

const ACCEPTED: Decision = { result: "accepted", retryAfterSeconds: null };
// Another push for the same key committed between this push's read and its write. The winner was
// just received, so the debounce window is the right wait.
const LOST_RACE: Decision = { result: "deferred", retryAfterSeconds: DEBOUNCE_SECONDS, logReason: "concurrent_push" };
// The query limit is per invocation, so the next push has a whole budget: the Mac's next poll is soon enough.
const OVER_QUERY_BUDGET: Decision = { result: "deferred", retryAfterSeconds: 1 };

const FIRST_PUSH_STAMP_QUERIES = 1;

export function pushHandler(onPushAccepted: OnPushAccepted) {
  return async (ctx: HandlerContext): Promise<Response> => {
    const push = parseBody<PushRequest>("PushRequest", await readJsonBody(ctx.request));
    const isFirstPush = await recordMachinePush(ctx, push);
    const run: PushRun = {
      ctx,
      push,
      isFirstPush,
      id: crypto.randomUUID(),
      hookQueries: onPushAccepted.maxQueries,
      accepted: [],
      deferredForBudget: 0,
    };
    const projects: ProjectPushResult[] = [];
    for (const project of push.projects) projects.push(await pushProject(run, project));
    if (run.deferredForBudget > 0) console.log(JSON.stringify({ push: "query_budget", deferred: run.deferredForBudget }));
    // A deferral the Mac will retry keeps the first-push window open, so the retry's backlog is still
    // suppressed (plan D8). One with no retry (a key another machine owns) never lands, so it must not.
    if (isFirstPush && !projects.some(awaitsRetry)) {
      await stampFirstPush(ctx, push.machine.id);
    }
    if (run.accepted.length > 0) await onPushAccepted.run(ctx, run.accepted);
    return jsonResponse(200, { v: 1, server_time: ctx.now.toISOString(), projects });
  };
}

function awaitsRetry(project: ProjectPushResult): boolean {
  return project.result === "deferred" && project.retry_after_seconds !== null;
}

/** Upserts the machine row and returns whether no earlier push of this machine has fully landed. */
async function recordMachinePush({ db, now }: HandlerContext, { machine, budget }: PushRequest): Promise<boolean> {
  const firstPushAt = await db
    .prepare("SELECT first_push_at FROM machines WHERE id = ?")
    .bind(machine.id)
    .first<string | null>("first_push_at");
  const at = now.toISOString();
  await db
    .prepare(
      `INSERT INTO machines (id, orch_version, last_push_at, budget_json) VALUES (?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET
         orch_version = excluded.orch_version, last_push_at = excluded.last_push_at, budget_json = excluded.budget_json`,
    )
    .bind(machine.id, machine.orch_version, at, JSON.stringify(budget))
    .run();
  return firstPushAt === null;
}

async function stampFirstPush({ db, now }: HandlerContext, machineId: string): Promise<void> {
  await db
    .prepare("UPDATE machines SET first_push_at = ? WHERE id = ? AND first_push_at IS NULL")
    .bind(now.toISOString(), machineId)
    .run();
}

async function pushProject(run: PushRun, project: ProjectSnapshot): Promise<ProjectPushResult> {
  const outcome = await landProject(run, project);
  if (outcome === OVER_QUERY_BUDGET) run.deferredForBudget++;
  if (outcome.logReason) console.log(JSON.stringify({ push: outcome.logReason, key: project.key }));
  return { key: project.key, result: outcome.result, retry_after_seconds: outcome.retryAfterSeconds };
}

async function landProject(run: PushRun, project: ProjectSnapshot): Promise<Decision> {
  if (!canSpend(run, 1)) return OVER_QUERY_BUDGET;
  const stored = await run.ctx.db
    .prepare("SELECT machine_id, state, state_since, snapshot_hash, received_at FROM projects WHERE key = ?")
    .bind(project.key)
    .first<StoredProject>();
  const decision = decide(stored, run.push.machine.id, project.snapshot_hash, run.ctx.now);
  return decision.result === "accepted" ? acceptProject(run, project, stored) : decision;
}

/** Whether `queries` more fit in this invocation and still leave the queries that run after the projects. */
function canSpend(run: PushRun, queries: number): boolean {
  return run.ctx.queries.remaining() - reservedQueries(run) >= queries;
}

/**
 * The first-push stamp's queries, and the hook's once a project is accepted. The first project to land
 * may use the hook's: any one project fits a push that way, so none is deferred for good, and the
 * notifier, finding too few left, leaves what is owed to the next push.
 */
function reservedQueries(run: PushRun): number {
  return (run.isFirstPush ? FIRST_PUSH_STAMP_QUERIES : 0) + (run.accepted.length > 0 ? run.hookQueries : 0);
}

function decide(stored: StoredProject | null, machineId: string, snapshotHash: string, now: Date): Decision {
  if (stored === null) return ACCEPTED;
  // Never a silent takeover: a key belongs to the machine that first pushed it.
  if (stored.machine_id !== machineId) {
    return { result: "deferred", retryAfterSeconds: null, logReason: "project_owned_by_another_machine" };
  }
  if (stored.snapshot_hash === snapshotHash) return { result: "unchanged", retryAfterSeconds: null };
  const elapsedSeconds = (now.getTime() - Date.parse(stored.received_at)) / 1000;
  if (elapsedSeconds < DEBOUNCE_SECONDS) {
    const wait = Math.min(MAX_RETRY_AFTER_SECONDS, Math.ceil(DEBOUNCE_SECONDS - elapsedSeconds));
    return { result: "deferred", retryAfterSeconds: wait };
  }
  return ACCEPTED;
}

/**
 * Writes the project and its questions in one batch (a D1 transaction), project first, when the
 * batch fits the query budget. The project write applies only if the row is as this push read it,
 * and each question write only if the project row carries this push's id, so when a concurrent push
 * for the key commits first this batch changes nothing.
 */
async function acceptProject(
  run: PushRun,
  project: ProjectSnapshot,
  stored: StoredProject | null,
): Promise<Decision> {
  if (stored !== null && !canSpend(run, 1)) return OVER_QUERY_BUDGET;
  const storedQuestions = stored === null ? [] : await loadQuestions(run.ctx.db, project.key);
  const changes = diffQuestions(storedQuestions, project.questions);
  const writes = [
    stored === null ? insertProject(run, project) : updateProject(run, project, stored),
    ...changes.added.map((question) => insertQuestion(run, project, question)),
    ...changes.revised.map((question) => reviseQuestion(run, project, question)),
    ...changes.closedIds.map((id) => closeQuestion(run, project, id)),
  ];
  if (!canSpend(run, writes.length)) return OVER_QUERY_BUDGET;
  const [projectWrite] = await run.ctx.db.batch(writes);
  if (projectWrite.meta.changes !== 1) return LOST_RACE;
  run.accepted.push({ project, newQuestions: changes.added, previousState: stored?.state ?? null });
  return ACCEPTED;
}

// True inside the batch once this push's own project write has applied. Keyed on the push's own id,
// not on its hash and time: an identical rival push in the same millisecond shares both. Bound by
// thisPushWrote().
const THIS_PUSH_WROTE_PROJECT = "EXISTS (SELECT 1 FROM projects WHERE key = ? AND push_id = ?)";

function thisPushWrote(run: PushRun, project: ProjectSnapshot): string[] {
  return [project.key, run.id];
}

async function loadQuestions(db: CountedDb, projectKey: string): Promise<StoredQuestion[]> {
  const { results } = await db
    .prepare(
      `SELECT id, asked_at, asked_by, subject, body, recommended, blocking, ask, closed_at
       FROM questions WHERE project_key = ?`,
    )
    .bind(projectKey)
    .all<StoredQuestion>();
  return results;
}

interface QuestionChanges {
  added: Question[];
  /** Questions already stored whose fields changed, or that were closed and are back (reopened). */
  revised: Question[];
  /** Open questions the snapshot no longer lists. */
  closedIds: string[];
}

function diffQuestions(stored: StoredQuestion[], current: Question[]): QuestionChanges {
  const storedById = new Map(stored.map((question) => [question.id, question]));
  const currentIds = new Set(current.map((question) => question.id));
  return {
    added: current.filter((question) => !storedById.has(question.id)),
    revised: current.filter((question) => {
      const before = storedById.get(question.id);
      return before !== undefined && (before.closed_at !== null || !sameFields(before, question));
    }),
    closedIds: stored.filter((question) => question.closed_at === null && !currentIds.has(question.id)).map((q) => q.id),
  };
}

function sameFields(stored: StoredQuestion, question: Question): boolean {
  return (
    stored.asked_at === question.at &&
    stored.asked_by === question.from &&
    stored.subject === question.subject &&
    stored.body === question.body &&
    stored.recommended === question.recommended &&
    stored.blocking === Number(question.blocking) &&
    stored.ask === storedAsk(question)
  );
}

/** A question's `ask` as its row holds it: a snapshot leaves it out for a decision. */
function storedAsk(question: Question): QuestionAsk | null {
  return question.ask ?? null;
}

function insertProject({ ctx, push, id }: PushRun, project: ProjectSnapshot): D1PreparedStatement {
  const at = ctx.now.toISOString();
  return ctx.db
    .prepare(
      `INSERT INTO projects
         (key, machine_id, name, state, snapshot_json, snapshot_hash, sent_at, received_at, state_since, push_id)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(key) DO NOTHING`,
    )
    .bind(project.key, push.machine.id, project.name, project.state, snapshotJson(project), project.snapshot_hash,
      push.machine.sent_at, at, at, id);
}

function updateProject(
  { ctx, push, id }: PushRun,
  project: ProjectSnapshot,
  stored: StoredProject,
): D1PreparedStatement {
  const at = ctx.now.toISOString();
  const stateSince = stored.state === project.state ? stored.state_since : at;
  return ctx.db
    .prepare(
      `UPDATE projects SET name = ?, state = ?, snapshot_json = ?, snapshot_hash = ?, sent_at = ?, received_at = ?,
         state_since = ?, push_id = ?,
         notified_state = CASE WHEN state = ? THEN notified_state ELSE NULL END
       WHERE key = ? AND received_at = ?`,
    )
    // Matching the received_at this push read keeps the debounce decision true at write time. Any state
    // change clears the notifier's claim (SET reads the old `state`), so re-entering paused or failed
    // notifies again (plan D8).
    .bind(project.name, project.state, snapshotJson(project), project.snapshot_hash, push.machine.sent_at, at,
      stateSince, id, project.state, project.key, stored.received_at);
}

/** Questions live in their own table, so the stored snapshot leaves them out. */
function snapshotJson({ questions: _questions, ...snapshot }: ProjectSnapshot): string {
  return JSON.stringify(snapshot);
}

function insertQuestion(run: PushRun, project: ProjectSnapshot, question: Question): D1PreparedStatement {
  const { ctx, isFirstPush } = run;
  // `pending` means a notification is owed, and only a newly seen blocking question is owed one. A
  // machine's first push carries its whole backlog; marking it suppressed keeps that from becoming a
  // storm of notifications (plan D8).
  const notifyState = question.blocking && !isFirstPush ? "pending" : "suppressed";
  return ctx.db
    .prepare(
      `INSERT INTO questions
         (id, project_key, asked_at, asked_by, subject, body, recommended, blocking, ask, first_seen_at, notify_state)
       SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE ${THIS_PUSH_WROTE_PROJECT}`,
    )
    .bind(question.id, project.key, question.at, question.from, question.subject, question.body,
      question.recommended, Number(question.blocking), storedAsk(question), ctx.now.toISOString(), notifyState,
      ...thisPushWrote(run, project));
}

function reviseQuestion(run: PushRun, project: ProjectSnapshot, question: Question): D1PreparedStatement {
  // notify_state is left alone, so a reopened question does not notify again: D8 sends at most one
  // notification per question.
  return run.ctx.db
    .prepare(
      `UPDATE questions SET asked_at = ?, asked_by = ?, subject = ?, body = ?, recommended = ?, blocking = ?,
         ask = ?, closed_at = NULL
       WHERE id = ? AND ${THIS_PUSH_WROTE_PROJECT}`,
    )
    .bind(question.at, question.from, question.subject, question.body, question.recommended,
      Number(question.blocking), storedAsk(question), question.id, ...thisPushWrote(run, project));
}

function closeQuestion(run: PushRun, project: ProjectSnapshot, id: string): D1PreparedStatement {
  return run.ctx.db
    .prepare(`UPDATE questions SET closed_at = ? WHERE id = ? AND ${THIS_PUSH_WROTE_PROJECT}`)
    .bind(run.ctx.now.toISOString(), id, ...thisPushWrote(run, project));
}
