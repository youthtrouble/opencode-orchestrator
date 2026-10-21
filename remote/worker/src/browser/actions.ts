// POST /api/v1/projects/:key/actions and DELETE /api/v1/actions/:id (plan D6, D13): pause or resume a
// project once per tap, undoable inside the window. These checks spare the owner an obvious no-op; the
// Mac revalidates against the project's real state before applying either.
import { readJsonBody } from "../body.ts";
import { jsonResponse } from "../http.ts";
import type { ProjectState } from "../machine/wire.ts";
import { parseBody } from "../protocol.ts";
import type { CountedDb } from "../queries.ts";
import type { HandlerContext } from "../routes.ts";
import { conflict, notFound } from "./errors.ts";
import { applyAfter, cancelWithinWindow, insertOnce, liveStatus, tooLate, type Lifecycle } from "./lifecycle.ts";
import type { ActionKind, ActionRequest } from "./wire.ts";

// The snapshot does not say whether a needs_you project is gated, but D12 makes every gate with a
// question needs_you, so resume is offered there too and the Mac answers `superseded` if there is no gate.
const RESUMABLE: ReadonlySet<ProjectState> = new Set(["paused", "needs_you"]);

interface StoredAction extends Lifecycle {
  id: string;
  project_key: string;
  kind: ActionKind;
}

const ACTION_COLUMNS = "id, project_key, kind, apply_after, cancelled_at, delivered_at, outcome, outcome_detail";

export async function createActionHandler(ctx: HandlerContext): Promise<Response> {
  const { db, env, now, params } = ctx;
  const request = parseBody<ActionRequest>("ActionRequest", await readJsonBody(ctx.request));
  // A retried tap is answered from its stored row before the project's current state is checked: a
  // pause the Mac has since applied leaves the project paused, and the retry must still see its action.
  const earlier = await actionByClientKey(db, request.client_key);
  if (earlier !== null) return jsonResponse(200, actionResponse(sameTap(earlier, params.key, request), now));

  const state = await db
    .prepare("SELECT state FROM projects WHERE key = ?")
    .bind(params.key)
    .first<ProjectState>("state");
  if (state === null) throw notFound("project");
  checkApplicable(request.kind, state);

  const insert = db
    .prepare(
      `INSERT INTO actions (id, project_key, kind, client_key, created_at, apply_after)
       VALUES (?, ?, ?, ?, ?, ?)
       ON CONFLICT(client_key) DO NOTHING`,
    )
    .bind(crypto.randomUUID(), params.key, request.kind, request.client_key, now.toISOString(), applyAfter(now, env));
  const created = await insertOnce(insert, () => openActionOn(db, params.key), (open) => alreadyPending(open, now));
  // Either this insert created the row or a concurrent request with the same client_key did.
  const action = await actionByClientKey(db, request.client_key);
  if (action === null) throw new Error("action missing after insert");
  if (created) return jsonResponse(201, actionResponse(action, now));
  return jsonResponse(200, actionResponse(sameTap(action, params.key, request), now));
}

export async function undoActionHandler({ db, now, params }: HandlerContext): Promise<Response> {
  const cancelled = await cancelWithinWindow(db, "actions", params.id, now);
  const action = await db
    .prepare(`SELECT ${ACTION_COLUMNS} FROM actions WHERE id = ?`)
    .bind(params.id)
    .first<StoredAction>();
  if (action === null) throw notFound("action");
  if (!cancelled) throw tooLate(action);
  return jsonResponse(200, actionResponse(action, now));
}

async function actionByClientKey(db: CountedDb, clientKey: string): Promise<StoredAction | null> {
  return db
    .prepare(`SELECT ${ACTION_COLUMNS} FROM actions WHERE client_key = ?`)
    .bind(clientKey)
    .first<StoredAction>();
}

/** The stored action, when the request repeats the tap that made it; anything else reuses its client_key. */
function sameTap(stored: StoredAction, projectKey: string, request: ActionRequest): StoredAction {
  if (stored.project_key !== projectKey) throw conflict("this client_key was used for another project");
  if (stored.kind !== request.kind) throw conflict(`this client_key was used for a ${stored.kind}`);
  return stored;
}

function checkApplicable(kind: ActionKind, state: ProjectState): void {
  if (kind === "pause" && state === "paused") throw conflict("already paused");
  if (kind === "resume" && !RESUMABLE.has(state)) throw conflict(`a ${state} project has nothing to resume`);
}

/** The project's one action not yet undone or settled (index actions_one_open_per_project). */
async function openActionOn(db: CountedDb, projectKey: string): Promise<StoredAction | null> {
  return db
    .prepare(
      `SELECT ${ACTION_COLUMNS} FROM actions WHERE project_key = ? AND cancelled_at IS NULL AND settled_at IS NULL`,
    )
    .bind(projectKey)
    .first<StoredAction>();
}

function alreadyPending(open: StoredAction, now: Date) {
  return conflict(`this project already has a ${open.kind} pending (${open.id}, ${liveStatus(open, now).status})`);
}

function actionResponse(action: StoredAction, now: Date) {
  const { status, detail } = liveStatus(action, now);
  return { v: 1, action: { id: action.id, kind: action.kind, apply_after: action.apply_after, status, detail } };
}
