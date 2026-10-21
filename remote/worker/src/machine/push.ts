// POST /api/v1/machine/push (plan D6.5, D7): stores a changed project snapshot at most once per
// DEBOUNCE_SECONDS and keeps the project's questions in step with it. An unchanged hash writes nothing.
//
// D1 cost: every statement counts against the per-invocation query limit, batched or not. A push costs
// 2 queries (3 on a machine's first push), plus 1 per project (its read), plus, per accepted project,
// 2 more (1 when new) and one statement per question written. The Workers Free plan allows 50
// queries per invocation, so a push near the schema's 50-project cap needs the Workers Paid plan (1000).
import { readJsonBody } from "../body.ts";
import { jsonResponse } from "../http.ts";
import { parseBody } from "../protocol.ts";
import type { HandlerContext } from "../routes.ts";
import type { ProjectPushResult, ProjectSnapshot, ProjectState, PushRequest, PushResult, Question } from "./wire.ts";

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

/** Runs once a project's writes have committed. WT9's notifier implements it. */
export type OnProjectAccepted = (ctx: HandlerContext, accepted: AcceptedProject) => Promise<void>;

interface PushRun {
  ctx: HandlerContext;
  push: PushRequest;
  isFirstPush: boolean;
  onProjectAccepted: OnProjectAccepted;
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

export function pushHandler(onProjectAccepted: OnProjectAccepted) {
  return async (ctx: HandlerContext): Promise<Response> => {
    const push = parseBody<PushRequest>("PushRequest", await readJsonBody(ctx.request));
    const isFirstPush = await recordMachinePush(ctx, push);
    const run: PushRun = { ctx, push, isFirstPush, onProjectAccepted };
    const projects: ProjectPushResult[] = [];
    for (const project of push.projects) projects.push(await pushProject(run, project));
    // Stamped only once every project has landed: a push that fails part-way is retried as a first
    // push, so the rest of its backlog is still suppressed (plan D8).
    if (isFirstPush) await stampFirstPush(ctx, push.machine.id);
    return jsonResponse(200, { v: 1, server_time: ctx.now.toISOString(), projects });
  };
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
  const stored = await run.ctx.db
    .prepare("SELECT machine_id, state, state_since, snapshot_hash, received_at FROM projects WHERE key = ?")
    .bind(project.key)
    .first<StoredProject>();
  const decision = decide(stored, run.push.machine.id, project.snapshot_hash, run.ctx.now);
  const outcome = decision.result === "accepted" ? await acceptProject(run, project, stored) : decision;
  if (outcome.logReason) console.log(JSON.stringify({ push: outcome.logReason, key: project.key }));
  return { key: project.key, result: outcome.result, retry_after_seconds: outcome.retryAfterSeconds };
}

async function acceptProject(run: PushRun, project: ProjectSnapshot, stored: StoredProject | null): Promise<Decision> {
  const newQuestions = await writeProject(run, project, stored);
  if (newQuestions === null) return LOST_RACE;
  await run.onProjectAccepted(run.ctx, { project, newQuestions, previousState: stored?.state ?? null });
  return ACCEPTED;
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
 * Writes the project and its questions in one batch (a D1 transaction), project first. The project
 * write applies only if the row is as this push read it, and each question write only if the project
 * row is the one this push wrote, so when a concurrent push for the key commits first this batch
 * changes nothing. Returns the questions new to the project, or null when that race was lost.
 */
async function writeProject(
  run: PushRun,
  project: ProjectSnapshot,
  stored: StoredProject | null,
): Promise<Question[] | null> {
  const { db } = run.ctx;
  const storedQuestions = stored === null ? [] : await loadQuestions(db, project.key);
  const changes = diffQuestions(storedQuestions, project.questions);
  const [projectWrite] = await db.batch([
    stored === null ? insertProject(run, project) : updateProject(run, project, stored),
    ...changes.added.map((question) => insertQuestion(run, project, question)),
    ...changes.revised.map((question) => reviseQuestion(run, project, question)),
    ...changes.closedIds.map((id) => closeQuestion(run, project, id)),
  ]);
  return projectWrite.meta.changes === 1 ? changes.added : null;
}

// True inside the batch once this push's own project write has applied. Bound by thisPushWrote().
const THIS_PUSH_WROTE_PROJECT =
  "EXISTS (SELECT 1 FROM projects WHERE key = ? AND snapshot_hash = ? AND received_at = ?)";

function thisPushWrote({ ctx }: PushRun, project: ProjectSnapshot): string[] {
  return [project.key, project.snapshot_hash, ctx.now.toISOString()];
}

async function loadQuestions(db: D1Database, projectKey: string): Promise<StoredQuestion[]> {
  const { results } = await db
    .prepare(
      `SELECT id, asked_at, asked_by, subject, body, recommended, blocking, closed_at
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
    stored.blocking === Number(question.blocking)
  );
}

function insertProject({ ctx, push }: PushRun, project: ProjectSnapshot): D1PreparedStatement {
  const at = ctx.now.toISOString();
  return ctx.db
    .prepare(
      `INSERT INTO projects
         (key, machine_id, name, state, snapshot_json, snapshot_hash, sent_at, received_at, state_since)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(key) DO NOTHING`,
    )
    .bind(project.key, push.machine.id, project.name, project.state, snapshotJson(project), project.snapshot_hash,
      push.machine.sent_at, at, at);
}

function updateProject({ ctx, push }: PushRun, project: ProjectSnapshot, stored: StoredProject): D1PreparedStatement {
  const at = ctx.now.toISOString();
  const stateSince = stored.state === project.state ? stored.state_since : at;
  return ctx.db
    .prepare(
      `UPDATE projects SET name = ?, state = ?, snapshot_json = ?, snapshot_hash = ?, sent_at = ?, received_at = ?,
         state_since = ?
       WHERE key = ? AND received_at = ?`,
    )
    // Matching the received_at this push read keeps the debounce decision true at write time.
    .bind(project.name, project.state, snapshotJson(project), project.snapshot_hash, push.machine.sent_at, at,
      stateSince, project.key, stored.received_at);
}

/** Questions live in their own table, so the stored snapshot leaves them out. */
function snapshotJson({ questions: _questions, ...snapshot }: ProjectSnapshot): string {
  return JSON.stringify(snapshot);
}

function insertQuestion(run: PushRun, project: ProjectSnapshot, question: Question): D1PreparedStatement {
  const { ctx, isFirstPush } = run;
  // A machine's first push carries its whole backlog; marking it suppressed keeps that from becoming
  // a storm of notifications (plan D8).
  const notifyState = isFirstPush ? "suppressed" : "pending";
  return ctx.db
    .prepare(
      `INSERT INTO questions
         (id, project_key, asked_at, asked_by, subject, body, recommended, blocking, first_seen_at, notify_state)
       SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE ${THIS_PUSH_WROTE_PROJECT}`,
    )
    .bind(question.id, project.key, question.at, question.from, question.subject, question.body,
      question.recommended, Number(question.blocking), ctx.now.toISOString(), notifyState,
      ...thisPushWrote(run, project));
}

function reviseQuestion(run: PushRun, project: ProjectSnapshot, question: Question): D1PreparedStatement {
  // notify_state is left alone, so a reopened question does not notify again: D8 sends at most one
  // notification per question.
  return run.ctx.db
    .prepare(
      `UPDATE questions SET asked_at = ?, asked_by = ?, subject = ?, body = ?, recommended = ?, blocking = ?,
         closed_at = NULL
       WHERE id = ? AND ${THIS_PUSH_WROTE_PROJECT}`,
    )
    .bind(question.at, question.from, question.subject, question.body, question.recommended,
      Number(question.blocking), question.id, ...thisPushWrote(run, project));
}

function closeQuestion(run: PushRun, project: ProjectSnapshot, id: string): D1PreparedStatement {
  return run.ctx.db
    .prepare(`UPDATE questions SET closed_at = ? WHERE id = ? AND ${THIS_PUSH_WROTE_PROJECT}`)
    .bind(run.ctx.now.toISOString(), id, ...thisPushWrote(run, project));
}
