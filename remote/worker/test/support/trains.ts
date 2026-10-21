// A whole Worker for browser and notifier tests: the real routes over one database, the Mac's and the
// owner's calls through the real gate, a clock the test moves, and the notification hook wired to a
// notifier the test supplies. Every call is one invocation: it waits for the work it handed to
// waitUntil, and its D1 queries, that work's included, count against `queryLimit`.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { compileSchema } from "../../../protocol/validate.ts";
import { createWorker } from "../../src/index.ts";
import type { Notification, Notifier } from "../../src/notify/notifier.ts";
import { notifyOnAccepted } from "../../src/notify/triggers.ts";
import { appRoutes } from "../../src/routes.ts";
import { keys, machineToken, ORIGIN, testEnv, tokensAt } from "./access.ts";
import { budgeted, QueryBudget } from "./budget.ts";
import { D1Double } from "./d1.ts";

const PROTOCOL = new URL("../../../protocol/v1/", import.meta.url);
const validate = compileSchema(JSON.parse(readFileSync(new URL("schema.json", PROTOCOL), "utf8")));

export const START = new Date("2026-10-20T12:00:00.000Z");
export const MACHINE_ID = "00000000-0000-4000-8000-000000000001";
export const KEY = "0123456789abcdef0123456789abcdef";

export type Json = Record<string, any>;

export interface Reply {
  status: number;
  body: Json | null;
}

export class RecordingNotifier implements Notifier {
  readonly sent: Notification[] = [];
  failing = false;

  async send(notification: Notification): Promise<void> {
    if (this.failing) throw new Error("send refused");
    this.sent.push(notification);
  }
}

interface TrainsOptions {
  env?: Json;
  notifier?: Notifier;
  queryLimit?: number;
}

export function trainsApp({ env = {}, notifier = new RecordingNotifier(), queryLimit = Infinity }: TrainsOptions = {}) {
  const db = new D1Double();
  const budget = new QueryBudget(queryLimit);
  let now = START;
  let hashes = 0;
  const worker = createWorker({
    keysFor: () => keys,
    routes: appRoutes({ onPushAccepted: notifyOnAccepted(() => notifier) }),
    now: () => now,
  });
  const workerEnv = Object.assign(testEnv(env, db) as Json, { DB: budgeted(db, budget) });

  async function send(request: Request): Promise<Reply> {
    budget.startInvocation();
    const tasks: Promise<unknown>[] = [];
    const executionCtx = { waitUntil: (task: Promise<unknown>) => void tasks.push(task) };
    const response = await worker.fetch!(request as never, workerEnv as never, executionCtx as never);
    await Promise.all(tasks);
    const text = await response.text();
    return { status: response.status, body: text === "" ? null : JSON.parse(text) };
  }

  async function machine(method: string, path: string, body?: unknown): Promise<Reply> {
    const jwt = await tokensAt(now).serviceJwt();
    const headers = { "Cf-Access-Jwt-Assertion": jwt, Authorization: `Bearer ${machineToken}` };
    return send(new Request(`${ORIGIN}${path}`, { method, headers, body: body && JSON.stringify(body) }));
  }

  /**
   * An owner's call, checked against the schema: `okDef` for a 2xx with a body, `Error` otherwise.
   * Writes carry the JSON content type and the page's Origin, as the page sends them.
   */
  async function browser(method: string, path: string, okDef: string | null, body?: unknown): Promise<Reply> {
    const headers: Record<string, string> = { "Cf-Access-Jwt-Assertion": await tokensAt(now).browserJwt() };
    if (method !== "GET") Object.assign(headers, { "Content-Type": "application/json", Origin: ORIGIN });
    const reply = await send(new Request(`${ORIGIN}${path}`, { method, headers, body: body && JSON.stringify(body) }));
    const def = reply.status < 300 ? okDef : "Error";
    if (def !== null) {
      assert.deepEqual(validate(def, reply.body), { ok: true }, `${def}: ${JSON.stringify(reply.body)}`);
    }
    return reply;
  }

  return {
    db,
    budget,
    notifier,
    at(secondsAfterStart: number) {
      now = new Date(START.getTime() + secondsAfterStart * 1000);
    },
    /** Pushes the projects; each call gets fresh snapshot hashes, so every push is a change. */
    push(projects: Json[], machineId = MACHINE_ID) {
      const freshHash = () => (++hashes).toString(16).padStart(64, "0");
      const hashed = projects.map((project) => ({ snapshot_hash: freshHash(), ...project }));
      return machine("POST", "/api/v1/machine/push", pushRequest(hashed, machineId));
    },
    pull: () => machine("GET", "/api/v1/machine/pull"),
    ack: (results: Json[]) => machine("POST", "/api/v1/machine/ack", { v: 1, results }),
    summary: () => browser("GET", "/api/v1/summary", "Summary"),
    answer: (questionId: string, body: Json) =>
      browser("POST", `/api/v1/questions/${questionId}/answer`, "AnswerResponse", body),
    undoAnswer: (id: string) => browser("DELETE", `/api/v1/answers/${id}`, "AnswerResponse"),
    act: (key: string, body: Json) => browser("POST", `/api/v1/projects/${key}/actions`, "ActionResponse", body),
    undoAction: (id: string) => browser("DELETE", `/api/v1/actions/${id}`, "ActionResponse"),
    setHidden: (key: string, hidden: unknown) =>
      browser("POST", `/api/v1/projects/${key}/visibility`, null, { hidden }),
  };
}

export type TrainsApp = ReturnType<typeof trainsApp>;

export function question(id: string, overrides: Json = {}): Json {
  return {
    id,
    at: "2026-10-20T11:00:00Z",
    from: "conductor",
    subject: `Question ${id}?`,
    body: "Some context.",
    recommended: "Yes.",
    blocking: true,
    ...overrides,
  };
}

/** A project snapshot without a hash; `push` adds a fresh one. */
export function snapshot(overrides: Json = {}): Json {
  return {
    key: KEY,
    name: "example-app",
    state: "needs_you",
    state_reason: "1 blocking question",
    door: "opencode",
    run: { kind: "none", since: null, last_activity_at: null },
    slice: null,
    now: null,
    progress: null,
    ci: [],
    events: [],
    questions: [question("q-1")],
    ...overrides,
  };
}

function pushRequest(projects: Json[], machineId: string): Json {
  return {
    v: 1,
    machine: { id: machineId, orch_version: "0.9.0", sent_at: "2026-10-20T12:00:00Z" },
    budget: { anthropic: null, openai: null },
    projects,
  };
}

export const iso = (secondsAfterStart: number) => new Date(START.getTime() + secondsAfterStart * 1000).toISOString();

/** A client_key: a placeholder-shaped UUID (the public-repo guard wants mostly zeros), numbered. */
export const clientKey = (n: number) => `00000000-0000-4000-8000-${n.toString(16).padStart(12, "0")}`;
