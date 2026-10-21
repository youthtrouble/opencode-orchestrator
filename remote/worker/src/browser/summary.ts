// GET /api/v1/summary (plan D15): everything the page shows, in one read. Projects that need the owner
// come first; each open question carries its live answer and each project its open action.
import { jsonResponse } from "../http.ts";
import type { ProjectState, QuestionAsk } from "../machine/wire.ts";
import type { HandlerContext } from "../routes.ts";
import { answerView, type StoredAnswer } from "./answers.ts";
import { undoSeconds } from "./lifecycle.ts";
import type { ActionKind } from "./wire.ts";

// The schema caps a summary at 50 projects; the order puts the ones that matter inside the cap.
const MAX_PROJECTS = 50;

const PROJECTS = `
  SELECT key, name, hidden, state, snapshot_json, received_at, sent_at FROM projects
  ORDER BY CASE state
      WHEN 'needs_you' THEN 0 WHEN 'failed' THEN 1 WHEN 'paused' THEN 2
      WHEN 'working' THEN 3 WHEN 'done' THEN 4 ELSE 5 END,
    received_at DESC
  LIMIT ${MAX_PROJECTS}`;

const OPEN_QUESTIONS = `
  SELECT id, project_key, asked_at, asked_by, subject, body, recommended, blocking, ask FROM questions
  WHERE closed_at IS NULL
  ORDER BY asked_at, id`;

// At most one per question (index answers_one_live_per_question).
const LIVE_ANSWERS = `
  SELECT answers.id, answers.question_id, answers.kind, answers.text, answers.apply_after, answers.cancelled_at,
    answers.delivered_at, answers.outcome, answers.outcome_detail
  FROM answers JOIN questions ON questions.id = answers.question_id
  WHERE answers.cancelled_at IS NULL AND questions.closed_at IS NULL`;

// At most one per project (index actions_one_open_per_project).
const OPEN_ACTIONS = `
  SELECT id, project_key, kind, apply_after FROM actions
  WHERE cancelled_at IS NULL AND settled_at IS NULL`;

// One Mac for v1 (pull carries no machine id), so the page shows the one that pushed last.
const LATEST_MACHINE = `
  SELECT orch_version, last_pull_at, last_push_at, budget_json FROM machines
  ORDER BY last_push_at DESC LIMIT 1`;

interface ProjectRow {
  key: string;
  name: string;
  hidden: number;
  state: ProjectState;
  snapshot_json: string;
  received_at: string;
  sent_at: string;
}

interface QuestionRow {
  id: string;
  project_key: string;
  asked_at: string;
  asked_by: string;
  subject: string;
  body: string;
  recommended: string | null;
  blocking: number;
  ask: QuestionAsk | null;
}

interface ActionRow {
  id: string;
  project_key: string;
  kind: ActionKind;
  apply_after: string;
}

interface MachineRow {
  orch_version: string;
  last_pull_at: string | null;
  last_push_at: string | null;
  budget_json: string | null;
}

export async function summaryHandler({ db, env, cfg, now }: HandlerContext): Promise<Response> {
  const [projects, questions, answers, actions, machines] = await db.batch([
    db.prepare(PROJECTS),
    db.prepare(OPEN_QUESTIONS),
    db.prepare(LIVE_ANSWERS),
    db.prepare(OPEN_ACTIONS),
    db.prepare(LATEST_MACHINE),
  ]);
  const machine = (machines.results as MachineRow[])[0] ?? null;
  const answerTo = new Map((answers.results as StoredAnswer[]).map((answer) => [answer.question_id, answer]));
  const actionOn = new Map((actions.results as ActionRow[]).map((action) => [action.project_key, action]));
  const questionsOf = groupBy(questions.results as QuestionRow[], (question) => question.project_key);

  const projectView = (project: ProjectRow) => ({
    ...snapshotFields(project),
    pending_action: pendingAction(actionOn.get(project.key)),
    questions: (questionsOf.get(project.key) ?? []).map((question) => {
      const answer = answerTo.get(question.id);
      return { ...questionFields(question), answer: answer ? answerView(answer, now) : null };
    }),
  });

  return jsonResponse(200, {
    v: 1,
    server_time: now.toISOString(),
    machine: machine && {
      orch_version: machine.orch_version,
      last_pull_at: machine.last_pull_at,
      last_push_at: machine.last_push_at,
    },
    budget: machine?.budget_json ? JSON.parse(machine.budget_json) : null,
    undo_seconds: undoSeconds(env),
    public_origin: cfg.publicOrigin,
    projects: (projects.results as ProjectRow[]).map(projectView),
  });
}

/** The page's fields from the stored snapshot, named one by one so nothing else it holds leaks through. */
function snapshotFields(project: ProjectRow) {
  const { state_reason, door, run, slice, now, progress, ci, events } = JSON.parse(project.snapshot_json);
  return {
    key: project.key,
    name: project.name,
    hidden: project.hidden === 1,
    state: project.state,
    state_reason,
    door,
    run,
    slice,
    now,
    progress,
    ci,
    events,
    received_at: project.received_at,
    sent_at: project.sent_at,
  };
}

function questionFields(question: QuestionRow) {
  return {
    id: question.id,
    at: question.asked_at,
    from: question.asked_by,
    subject: question.subject,
    body: question.body,
    recommended: question.recommended,
    blocking: question.blocking === 1,
    ...(question.ask === null ? {} : { ask: question.ask }),
  };
}

function pendingAction(action: ActionRow | undefined) {
  return action ? { id: action.id, kind: action.kind, apply_after: action.apply_after } : null;
}

function groupBy<T>(items: T[], keyOf: (item: T) => string): Map<string, T[]> {
  const groups = new Map<string, T[]>();
  for (const item of items) {
    const group = groups.get(keyOf(item));
    if (group) group.push(item);
    else groups.set(keyOf(item), [item]);
  }
  return groups;
}
