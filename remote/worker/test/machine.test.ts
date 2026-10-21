import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { compileSchema } from "../../protocol/validate.ts";
import { createWorker } from "../src/index.ts";
import { cancelWithinWindow } from "../src/browser/lifecycle.ts";
import { ACCEPT_FROM_MAILBOX, ACCEPT_HEADER } from "../src/machine/pull.ts";
import type { AcceptedProject } from "../src/machine/push.ts";
import type { CountedDb } from "../src/queries.ts";
import { appRoutes } from "../src/routes.ts";
import { keys, machineToken, ORIGIN, testEnv, tokensAt } from "./support/access.ts";
import { D1Double } from "./support/d1.ts";

const PROTOCOL = new URL("../../protocol/v1/", import.meta.url);
const readJson = (path: string) => JSON.parse(readFileSync(new URL(path, PROTOCOL), "utf8"));
const validate = compileSchema(readJson("schema.json"));

const START = new Date("2026-10-20T12:00:00.000Z");
const MACHINE_ID = "00000000-0000-4000-8000-000000000001";
const OTHER_MACHINE_ID = "00000000-0000-4000-8000-000000000009";
const KEY = "0123456789abcdef0123456789abcdef";
const OTHER_KEY = "fedcba9876543210fedcba9876543210";
const HASH_A = "a".repeat(64);
const HASH_B = "b".repeat(64);
const HASH_C = "c".repeat(64);
const ANSWER_ID = "00000000-0000-4000-8000-000000000002";
const ACTION_ID = "00000000-0000-4000-8000-000000000003";

type Json = Record<string, any>;

interface Reply {
  status: number;
  body: Json;
}

/** A Worker with the real machine routes over one database, on a clock the test moves. */
function machineApp() {
  const db = new D1Double();
  const accepted: AcceptedProject[] = [];
  let now = START;
  const worker = createWorker({
    keysFor: () => keys,
    routes: appRoutes({ onPushAccepted: async (_ctx, projects) => void accepted.push(...projects) }),
    now: () => now,
  });
  const env = testEnv({}, db);

  async function call(method: string, path: string, body?: BodyInit, headers: Record<string, string> = {}) {
    const jwt = await tokensAt(now).serviceJwt();
    const request = new Request(`${ORIGIN}${path}`, {
      method,
      body,
      headers: { "Cf-Access-Jwt-Assertion": jwt, Authorization: `Bearer ${machineToken}`, ...headers },
      duplex: "half",
    } as RequestInit);
    const response = await worker.fetch!(request as never, env, {} as never);
    return { status: response.status, body: (await response.json()) as Json };
  }

  return {
    db,
    accepted,
    at(secondsAfterStart: number) {
      now = new Date(START.getTime() + secondsAfterStart * 1000);
    },
    push: (body: unknown) => call("POST", "/api/v1/machine/push", JSON.stringify(body)),
    /** A pull from an orch that answers an accept from its own mailbox, as every current orch does. */
    pull: () => call("GET", "/api/v1/machine/pull", undefined, { [ACCEPT_HEADER]: ACCEPT_FROM_MAILBOX }),
    /** A pull from an orch built before the accept header. */
    pullFromOldOrch: () => call("GET", "/api/v1/machine/pull"),
    ack: (body: unknown) => call("POST", "/api/v1/machine/ack", JSON.stringify(body)),
    call,
  };
}

function assertValid(defName: string, reply: Reply) {
  assert.deepEqual(validate(defName, reply.body), { ok: true }, JSON.stringify(reply.body));
}

function question(id: string, overrides: Json = {}): Json {
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

function snapshot(overrides: Json = {}): Json {
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
    snapshot_hash: HASH_A,
    ...overrides,
  };
}

function pushRequest(projects: Json[], machineId = MACHINE_ID): Json {
  return {
    v: 1,
    machine: { id: machineId, orch_version: "0.9.0", sent_at: "2026-10-20T12:00:00Z" },
    budget: { anthropic: null, openai: null },
    projects,
  };
}

const projectRow = (db: D1Double) => db.sqlite.prepare("SELECT * FROM projects WHERE key = ?").get(KEY) as Json;
const questionRow = (db: D1Double, id: string) =>
  db.sqlite.prepare("SELECT * FROM questions WHERE id = ?").get(id) as Json;
const machineRow = (db: D1Double, id = MACHINE_ID) =>
  db.sqlite.prepare("SELECT * FROM machines WHERE id = ?").get(id) as Json;
const iso = (secondsAfterStart: number) => new Date(START.getTime() + secondsAfterStart * 1000).toISOString();

function projectResult(reply: Reply) {
  assert.equal(reply.status, 200, JSON.stringify(reply.body));
  assertValid("PushResponse", reply);
  assert.equal(reply.body.projects.length, 1);
  return reply.body.projects[0];
}

test("a machine's first push stores the project and suppresses notification of its questions", async () => {
  const app = machineApp();
  const reply = await app.push(pushRequest([snapshot()]));
  assert.deepEqual(projectResult(reply), { key: KEY, result: "accepted", retry_after_seconds: null });
  assert.equal(reply.body.server_time, iso(0));

  const machine = machineRow(app.db);
  assert.equal(machine.first_push_at, iso(0));
  assert.equal(machine.last_push_at, iso(0));
  assert.deepEqual(JSON.parse(machine.budget_json), { anthropic: null, openai: null });

  const project = projectRow(app.db);
  assert.equal(project.machine_id, MACHINE_ID);
  assert.equal(project.state, "needs_you");
  assert.equal(project.state_since, iso(0));
  assert.equal(project.snapshot_hash, HASH_A);
  assert.equal(project.sent_at, "2026-10-20T12:00:00Z");
  const stored = JSON.parse(project.snapshot_json);
  assert.equal(stored.questions, undefined);
  assert.equal(stored.state_reason, "1 blocking question");

  const q = questionRow(app.db, "q-1");
  assert.equal(q.project_key, KEY);
  assert.equal(q.notify_state, "suppressed");
  assert.equal(q.first_seen_at, iso(0));
  assert.equal(q.blocking, 1);
  assert.equal(q.closed_at, null);

  assert.equal(app.accepted.length, 1);
  assert.equal(app.accepted[0].previousState, null);
  assert.deepEqual(app.accepted[0].newQuestions.map((nq) => nq.id), ["q-1"]);
});

test("a first push that fails part-way is retried as a first push, so its backlog stays suppressed", async (t) => {
  const app = machineApp();
  t.mock.method(console, "log", () => {});
  // A duplicate question id fails that project's batch (PK conflict) after the first project committed.
  const broken = snapshot({ key: OTHER_KEY, questions: [question("q-2"), question("q-2")] });
  const failed = await app.push(pushRequest([snapshot(), broken]));
  assert.equal(failed.status, 500);
  assert.equal(machineRow(app.db).first_push_at, null);

  app.at(60);
  const retried = await app.push(pushRequest([snapshot(), snapshot({ key: OTHER_KEY, questions: [question("q-2")] })]));
  assert.equal(retried.status, 200);
  assert.equal(questionRow(app.db, "q-1").notify_state, "suppressed");
  assert.equal(questionRow(app.db, "q-2").notify_state, "suppressed");
  assert.equal(machineRow(app.db).first_push_at, iso(60));

  app.at(120);
  await app.push(pushRequest([snapshot({ snapshot_hash: HASH_B, questions: [question("q-1"), question("q-3")] })]));
  assert.equal(questionRow(app.db, "q-3").notify_state, "pending");
});

/** Runs `rival` to completion just before the next batch, as if it committed between that push's read and write. */
function commitBeforeNextBatch(app: ReturnType<typeof machineApp>, rival: () => Promise<Reply>) {
  const batch = app.db.batch.bind(app.db);
  let raced = false;
  app.db.batch = async (statements) => {
    if (!raced) {
      raced = true;
      assert.equal((await rival()).status, 200);
    }
    return batch(statements);
  };
}

test("a push that loses a race to create a new key is deferred and writes nothing", async (t) => {
  const app = machineApp();
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const winner = snapshot({ snapshot_hash: HASH_B, questions: [question("q-2")] });
  commitBeforeNextBatch(app, () => app.push(pushRequest([winner])));

  const reply = await app.push(pushRequest([snapshot()]));
  assert.deepEqual(projectResult(reply), { key: KEY, result: "deferred", retry_after_seconds: 55 });
  assert.equal(projectRow(app.db).snapshot_hash, HASH_B);
  assert.equal(questionRow(app.db, "q-1"), undefined);
  assert.deepEqual(app.accepted.map((accepted) => accepted.project.snapshot_hash), [HASH_B]);
  assert.deepEqual(logged.map((line) => JSON.parse(line)), [{ push: "concurrent_push", key: KEY }]);
});

test("a push that loses a race to update a key is deferred and leaves the winner's questions alone", async (t) => {
  const app = machineApp();
  await app.push(pushRequest([snapshot({ questions: [question("q-1"), question("q-2")] })]));
  t.mock.method(console, "log", () => {});
  app.at(60);
  const winner = snapshot({ snapshot_hash: HASH_B, questions: [question("q-1", { subject: "Winner?" }), question("q-2")] });
  commitBeforeNextBatch(app, () => app.push(pushRequest([winner])));

  // The loser rewords q-1, drops q-2 and adds q-3; none of it may land.
  const loserQuestions = [question("q-1", { subject: "Loser?" }), question("q-3")];
  const reply = await app.push(pushRequest([snapshot({ snapshot_hash: HASH_C, questions: loserQuestions })]));
  assert.deepEqual(projectResult(reply), { key: KEY, result: "deferred", retry_after_seconds: 55 });
  assert.equal(projectRow(app.db).snapshot_hash, HASH_B);
  assert.equal(questionRow(app.db, "q-1").subject, "Winner?");
  assert.equal(questionRow(app.db, "q-2").closed_at, null);
  assert.equal(questionRow(app.db, "q-3"), undefined);
  assert.equal(app.accepted.length, 2);
});

// An identical rival in the same millisecond shares the loser's hash and received_at, so only the
// push's own id can tell the winner's project row from the loser's.
const identicalRaces = [
  {
    race: "create",
    before: async (_app: ReturnType<typeof machineApp>) => {},
    acceptedBefore: 0,
    notifyState: "suppressed",
  },
  {
    race: "update",
    before: async (app: ReturnType<typeof machineApp>) => {
      await app.push(pushRequest([snapshot()]));
      app.at(60);
    },
    acceptedBefore: 1,
    notifyState: "pending",
  },
];

for (const { race, before, acceptedBefore, notifyState } of identicalRaces) {
  test(`an identical push on the same clock that loses a race to ${race} a key is deferred, not a 500`, async (t) => {
    const app = machineApp();
    await before(app);
    t.mock.method(console, "log", () => {});
    const questions = [question("q-1"), question("q-2")];
    const identical = () => pushRequest([snapshot({ snapshot_hash: HASH_B, questions })]);
    commitBeforeNextBatch(app, () => app.push(identical()));

    const reply = await app.push(identical());
    assert.deepEqual(projectResult(reply), { key: KEY, result: "deferred", retry_after_seconds: 55 });
    assert.equal(projectRow(app.db).snapshot_hash, HASH_B);
    assert.equal(questionRow(app.db, "q-2").notify_state, notifyState);
    assert.equal(app.accepted.length, acceptedBefore + 1, "only the winner is accepted");
  });
}

test("an identical push is unchanged and writes no project or question row", async () => {
  const app = machineApp();
  await app.push(pushRequest([snapshot()]));
  const before = app.db.totalChanges();
  app.at(120);
  const reply = await app.push(pushRequest([snapshot()]));
  assert.deepEqual(projectResult(reply), { key: KEY, result: "unchanged", retry_after_seconds: null });
  // The one change is the machine row's last_push_at.
  assert.equal(app.db.totalChanges() - before, 1);
  assert.equal(projectRow(app.db).received_at, iso(0));
  assert.equal(app.accepted.length, 1);
});

test("a changed push inside the debounce window is deferred until 55 s have passed", async () => {
  const app = machineApp();
  await app.push(pushRequest([snapshot()]));
  const changed = snapshot({ snapshot_hash: HASH_B, state: "working", questions: [] });

  app.at(30);
  const before = app.db.totalChanges();
  const deferred = await app.push(pushRequest([changed]));
  assert.deepEqual(projectResult(deferred), { key: KEY, result: "deferred", retry_after_seconds: 25 });
  assert.equal(app.db.totalChanges() - before, 1, "only the machine row changes");
  assert.equal(projectRow(app.db).snapshot_hash, HASH_A);

  app.at(54.5);
  assert.equal(projectResult(await app.push(pushRequest([changed]))).retry_after_seconds, 1);

  app.at(56);
  const accepted = await app.push(pushRequest([changed]));
  assert.deepEqual(projectResult(accepted), { key: KEY, result: "accepted", retry_after_seconds: null });
  const project = projectRow(app.db);
  assert.equal(project.snapshot_hash, HASH_B);
  assert.equal(project.received_at, iso(56));
  assert.equal(project.state, "working");
  assert.equal(project.state_since, iso(56));
  assert.equal(app.accepted.at(-1)?.previousState, "needs_you");
});

test("state_since stays put while the state does", async () => {
  const app = machineApp();
  await app.push(pushRequest([snapshot()]));
  app.at(60);
  await app.push(pushRequest([snapshot({ snapshot_hash: HASH_B, state_reason: "still waiting" })]));
  const project = projectRow(app.db);
  assert.equal(project.received_at, iso(60));
  assert.equal(project.state_since, iso(0));
});

test("questions follow the snapshot: new ones are pending, edits apply, absent ones close and reopen", async () => {
  const app = machineApp();
  await app.push(pushRequest([snapshot()]));

  app.at(60);
  const edited = question("q-1", { subject: "Reworded?", recommended: null, blocking: false });
  await app.push(pushRequest([snapshot({ snapshot_hash: HASH_B, questions: [edited, question("q-2")] })]));
  const q1 = questionRow(app.db, "q-1");
  assert.equal(q1.subject, "Reworded?");
  assert.equal(q1.recommended, null);
  assert.equal(q1.blocking, 0);
  assert.equal(q1.first_seen_at, iso(0));
  assert.equal(questionRow(app.db, "q-2").notify_state, "pending");
  assert.deepEqual(app.accepted.at(-1)?.newQuestions.map((nq) => nq.id), ["q-2"]);

  app.at(120);
  await app.push(pushRequest([snapshot({ snapshot_hash: HASH_C, questions: [question("q-2")] })]));
  assert.equal(questionRow(app.db, "q-1").closed_at, iso(120));
  assert.equal(questionRow(app.db, "q-2").closed_at, null);

  app.at(180);
  await app.push(pushRequest([snapshot({ snapshot_hash: HASH_A, questions: [edited, question("q-2")] })]));
  const reopened = questionRow(app.db, "q-1");
  assert.equal(reopened.closed_at, null);
  assert.equal(reopened.first_seen_at, iso(0));
  assert.deepEqual(app.accepted.at(-1)?.newQuestions, []);
});

test("another machine pushing a stored key is deferred and logged, never taking the project over", async (t) => {
  const app = machineApp();
  await app.push(pushRequest([snapshot()]));
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));

  app.at(120);
  const reply = await app.push(pushRequest([snapshot({ snapshot_hash: HASH_B })], OTHER_MACHINE_ID));
  assert.deepEqual(projectResult(reply), { key: KEY, result: "deferred", retry_after_seconds: null });
  assert.equal(projectRow(app.db).machine_id, MACHINE_ID);
  assert.equal(projectRow(app.db).snapshot_hash, HASH_A);
  assert.deepEqual(logged.map((line) => JSON.parse(line)), [{ push: "project_owned_by_another_machine", key: KEY }]);
});

test("a request in another protocol version is 400 unsupported_version listing version 1", async () => {
  const app = machineApp();
  for (const reply of [await app.push({ ...pushRequest([]), v: 2 }), await app.ack({ v: "1", results: [] })]) {
    assert.equal(reply.status, 400);
    assertValid("Error", reply);
    assert.equal(reply.body.error, "unsupported_version");
    assert.deepEqual(reply.body.supported, [1]);
  }
});

test("a body that fails the schema is 400 invalid_request naming the first bad path", async () => {
  const app = machineApp();
  const cases: Array<[string, (body: unknown) => Promise<Reply>]> = [
    ["examples/invalid/push-request-extra-property.json", app.push],
    ["examples/invalid/ack-request-no-results.json", app.ack],
  ];
  for (const [path, send] of cases) {
    const example = readJson(path);
    const reply = await send(example.value);
    assert.equal(reply.status, 400, path);
    assertValid("Error", reply);
    assert.equal(reply.body.error, "invalid_request");
    assert.match(reply.body.message, new RegExp(`at ${example.expect_path}:`), path);
  }
  assert.equal(machineRow(app.db), undefined, "an invalid push writes nothing");
});

test("an invalid_request message never echoes the offending value", async () => {
  const app = machineApp();
  const secretish = "do-not-echo-this-value";
  const reply = await app.push(pushRequest([snapshot({ state: secretish })]));
  assert.equal(reply.status, 400);
  assert.match(reply.body.message, /at \/projects\/0\/state:/);
  assert.ok(!reply.body.message.includes(secretish));
});

test("the machine routes keep the 256 KiB body limit", async () => {
  const app = machineApp();
  const oversized = JSON.stringify({ ...pushRequest([]), padding: "x".repeat(256 * 1024) });
  const reply = await app.call("POST", "/api/v1/machine/push", oversized);
  assert.equal(reply.status, 413);
  assertValid("Error", reply);
});

/**
 * A pushed project with question q-1 (and `moreQuestions`), an accept of q-1 and a pause action, both
 * created at 0 s.
 */
async function appWithPendingAnswerAndAction(moreQuestions: Json[] = []) {
  const app = machineApp();
  await app.push(pushRequest([snapshot({ questions: [question("q-1"), ...moreQuestions] })]));
  app.db.sqlite
    .prepare(
      `INSERT INTO answers (id, question_id, kind, text, client_key, created_at, apply_after)
       VALUES (?, 'q-1', 'accept', 'Yes.', 'client-1', ?, ?)`,
    )
    .run(ANSWER_ID, iso(0), iso(10));
  app.db.sqlite
    .prepare(
      `INSERT INTO actions (id, project_key, kind, client_key, created_at, apply_after)
       VALUES (?, ?, 'pause', 'client-2', ?, ?)`,
    )
    .run(ACTION_ID, KEY, iso(0), iso(10));
  return app;
}

const answerRow = (app: ReturnType<typeof machineApp>, id: string) =>
  app.db.sqlite.prepare("SELECT * FROM answers WHERE id = ?").get(id) as Json;
const deliveredAt = (app: ReturnType<typeof machineApp>, table: string, id: string) =>
  (app.db.sqlite.prepare(`SELECT delivered_at FROM ${table} WHERE id = ?`).get(id) as Json).delivered_at;

function pulled(reply: Reply) {
  assert.equal(reply.status, 200, JSON.stringify(reply.body));
  assertValid("PullResponse", reply);
  assert.equal(reply.body.poll_after_seconds, 30);
  return { answers: reply.body.answers.map((a: Json) => a.id), actions: reply.body.actions.map((a: Json) => a.id) };
}

test("pull hides answers and actions inside the undo window, then delivers them, stamping delivery once", async () => {
  const app = await appWithPendingAnswerAndAction();
  app.at(9);
  assert.deepEqual(pulled(await app.pull()), { answers: [], actions: [] });
  assert.equal(deliveredAt(app, "answers", ANSWER_ID), null);

  app.at(10);
  const reply = await app.pull();
  assert.deepEqual(pulled(reply), { answers: [ANSWER_ID], actions: [ACTION_ID] });
  assert.deepEqual(reply.body.answers[0], {
    id: ANSWER_ID,
    project_key: KEY,
    question_id: "q-1",
    kind: "accept",
    text: "Yes.",
    created_at: iso(0),
  });
  assert.deepEqual(reply.body.actions[0], { id: ACTION_ID, project_key: KEY, kind: "pause", created_at: iso(0) });
  assert.equal(deliveredAt(app, "answers", ANSWER_ID), iso(10));
  assert.equal(deliveredAt(app, "actions", ACTION_ID), iso(10));

  // Unacked rows are delivered again (at least once), but their first delivery time stands.
  app.at(40);
  assert.deepEqual(pulled(await app.pull()), { answers: [ANSWER_ID], actions: [ACTION_ID] });
  assert.equal(deliveredAt(app, "answers", ANSWER_ID), iso(10));
  assert.equal(deliveredAt(app, "actions", ACTION_ID), iso(10));
});

test("an old orch's pull refuses each due accept, settled as rejected for the page, and still gets the rest", async () => {
  const app = await appWithPendingAnswerAndAction([question("q-2")]);
  const typedId = "00000000-0000-4000-8000-000000000004";
  app.db.sqlite
    .prepare(
      `INSERT INTO answers (id, question_id, kind, text, client_key, created_at, apply_after)
       VALUES (?, 'q-2', 'typed', 'Not yet.', 'client-3', ?, ?)`,
    )
    .run(typedId, iso(0), iso(10));
  app.at(9);
  assert.deepEqual(pulled(await app.pullFromOldOrch()), { answers: [], actions: [] });
  assert.equal(answerRow(app, ANSWER_ID).outcome, null, "an accept inside its undo window is left alone");

  app.at(10);
  assert.deepEqual(pulled(await app.pullFromOldOrch()), { answers: [typedId], actions: [ACTION_ID] });
  const refused = answerRow(app, ANSWER_ID);
  assert.equal(refused.outcome, "rejected");
  assert.match(refused.outcome_detail, /^orch on the Mac is too old to accept a recommendation/);
  assert.equal(refused.delivered_at, iso(10));
  assert.equal(refused.settled_at, iso(10));
  assert.deepEqual(pulled(await app.pull()).answers, [typedId], "a refused accept stays refused for a new orch");
});

test("a cancelled answer or action is never pulled", async () => {
  const app = await appWithPendingAnswerAndAction();
  app.db.sqlite.prepare("UPDATE answers SET cancelled_at = ?").run(iso(5));
  app.db.sqlite.prepare("UPDATE actions SET cancelled_at = ?").run(iso(5));
  app.at(60);
  assert.deepEqual(pulled(await app.pull()), { answers: [], actions: [] });
});

/** Runs `step` once, just before or just after the next batch, as if it committed on either side of it. */
function aroundNextBatch(app: ReturnType<typeof machineApp>, when: "before" | "after", step: () => Promise<void>) {
  const batch = app.db.batch.bind(app.db);
  let ran = false;
  app.db.batch = async (statements) => {
    if (ran) return batch(statements);
    ran = true;
    if (when === "before") await step();
    const results = await batch(statements);
    if (when === "after") await step();
    return results;
  };
}

const lifecycleRows = [
  { table: "answers", id: ANSWER_ID, other: "actions", otherId: ACTION_ID },
  { table: "actions", id: ACTION_ID, other: "answers", otherId: ANSWER_ID },
] as const;

// The Undo's request time is 9 s, inside the window, but its UPDATE reaches D1 around the 10 s pull.
for (const { table, id, other, otherId } of lifecycleRows) {
  const undo = (app: ReturnType<typeof machineApp>) =>
    cancelWithinWindow(app.db as unknown as CountedDb, table, id, new Date(iso(9)));

  test(`an Undo of ${table} that commits just before the pull is honoured: the row is never delivered`, async () => {
    const app = await appWithPendingAnswerAndAction();
    let undone = false;
    aroundNextBatch(app, "before", async () => void (undone = await undo(app)));
    app.at(10);
    const reply = pulled(await app.pull());
    assert.equal(undone, true);
    assert.deepEqual(reply[table], []);
    assert.equal(deliveredAt(app, table, id), null);
    assert.deepEqual(reply[other], [otherId]);
  });

  test(`an Undo of ${table} that reaches D1 just after the pull is refused: the delivery stands`, async () => {
    const app = await appWithPendingAnswerAndAction();
    let undone: boolean | undefined;
    aroundNextBatch(app, "after", async () => void (undone = await undo(app)));
    app.at(10);
    const reply = pulled(await app.pull());
    assert.equal(undone, false);
    assert.deepEqual(reply[table], [id]);
    assert.equal(deliveredAt(app, table, id), iso(10));
    const row = app.db.sqlite.prepare(`SELECT cancelled_at FROM ${table} WHERE id = ?`).get(id) as Json;
    assert.equal(row.cancelled_at, null);
  });
}

test("pull refreshes the machine's last_pull_at at most once a minute", async () => {
  const app = machineApp();
  await app.push(pushRequest([]));
  await app.pull();
  assert.equal(machineRow(app.db).last_pull_at, iso(0));
  app.at(30);
  const before = app.db.totalChanges();
  await app.pull();
  assert.equal(machineRow(app.db).last_pull_at, iso(0));
  assert.equal(app.db.totalChanges(), before, "a pull inside the minute writes nothing");
  app.at(60);
  await app.pull();
  assert.equal(machineRow(app.db).last_pull_at, iso(60));
});

test("ack settles each answer or action once; a repeat or an unknown id settles nothing", async () => {
  const app = await appWithPendingAnswerAndAction();
  app.at(10);
  await app.pull();
  const results = [
    { id: ANSWER_ID, type: "answer", outcome: "applied", detail: "" },
    { id: ACTION_ID, type: "action", outcome: "superseded", detail: "already paused" },
  ];

  app.at(15);
  const first = await app.ack({ v: 1, results });
  assert.equal(first.status, 200);
  assertValid("AckResponse", first);
  assert.deepEqual(first.body, { v: 1, settled: 2 });

  app.at(20);
  const repeat = await app.ack({ v: 1, results: [{ ...results[0], outcome: "rejected", detail: "late" }] });
  assertValid("AckResponse", repeat);
  assert.deepEqual(repeat.body, { v: 1, settled: 0 });
  const answer = app.db.sqlite.prepare("SELECT * FROM answers WHERE id = ?").get(ANSWER_ID) as Json;
  assert.deepEqual([answer.outcome, answer.outcome_detail, answer.settled_at], ["applied", "", iso(15)]);
  const action = app.db.sqlite.prepare("SELECT * FROM actions WHERE id = ?").get(ACTION_ID) as Json;
  assert.deepEqual([action.outcome, action.outcome_detail, action.settled_at], ["superseded", "already paused", iso(15)]);

  const unknown = await app.ack({
    v: 1,
    results: [{ id: "00000000-0000-4000-8000-00000000000f", type: "answer", outcome: "applied", detail: "" }],
  });
  assert.deepEqual(unknown.body, { v: 1, settled: 0 });

  app.at(60);
  assert.deepEqual(pulled(await app.pull()), { answers: [], actions: [] });
});

test("an ack settles nothing the Mac has not pulled", async () => {
  const app = await appWithPendingAnswerAndAction();
  app.at(10);
  const results = [{ id: ANSWER_ID, type: "answer", outcome: "applied", detail: "" }];
  assert.deepEqual((await app.ack({ v: 1, results })).body, { v: 1, settled: 0 });
  assert.deepEqual(pulled(await app.pull()).answers, [ANSWER_ID]);
  assert.deepEqual((await app.ack({ v: 1, results })).body, { v: 1, settled: 1 });
});

test("an ack settles only rows of its own type", async () => {
  const app = await appWithPendingAnswerAndAction();
  app.at(10);
  await app.pull();
  const reply = await app.ack({ v: 1, results: [{ id: ANSWER_ID, type: "action", outcome: "applied", detail: "" }] });
  assert.deepEqual(reply.body, { v: 1, settled: 0 });
  assert.deepEqual(pulled(await app.pull()).answers, [ANSWER_ID]);
});
