import test from "node:test";
import assert from "node:assert/strict";
import { DisabledNotifier, EmailNotifier, notifierFor } from "../src/notify/notifier.ts";
import { DAILY_EMAIL_CAP, NOTIFY_QUERIES_PER_PUSH, notifyOnAccepted } from "../src/notify/triggers.ts";
import { countingDb, QUERIES_PER_INVOCATION, QueryCounter } from "../src/queries.ts";
import { ORIGIN } from "./support/access.ts";
import { budgeted, QueryBudget } from "./support/budget.ts";
import { iso, KEY, question, RecordingNotifier, snapshot, START, trainsApp, type Json, type TrainsApp } from "./support/trains.ts";

const email = (line: string) => ({ subject: line, text: `${line}\n${ORIGIN}\n` });
const NEEDS_YOU = email("Work Trains: example-app needs you");
const PAUSED = email("Work Trains: example-app is paused");

/** A machine past its first push, whose backlog is suppressed; `clock` steps 60 s per later push. */
async function primedApp(notifier = new RecordingNotifier(), queryLimit = Infinity) {
  const app = trainsApp({ notifier, queryLimit });
  await app.push([snapshot({ key: "f".repeat(32), state: "idle", questions: [] })]);
  let clock = 0;
  const pushLater = (projects: Json[]) => {
    clock += 60;
    app.at(clock);
    return app.push(projects);
  };
  return { app, notifier, pushLater };
}

const notifyState = (app: TrainsApp, id: string) =>
  (app.db.sqlite.prepare("SELECT notify_state FROM questions WHERE id = ?").get(id) as Json).notify_state;

test("a new blocking question notifies once, however many pushes still carry it", async () => {
  const { app, notifier, pushLater } = await primedApp();
  await pushLater([snapshot()]);
  await pushLater([snapshot({ state_reason: "still waiting" })]);
  assert.deepEqual(notifier.sent, [NEEDS_YOU]);
  assert.equal(notifyState(app, "q-1"), "sent");
});

test("a non-blocking question never notifies", async () => {
  const { app, notifier, pushLater } = await primedApp();
  await pushLater([snapshot({ state: "working", questions: [question("q-1", { blocking: false })] })]);
  await pushLater([snapshot({ state: "working", questions: [question("q-1", { blocking: true })] })]);
  assert.deepEqual(notifier.sent, []);
  assert.equal(notifyState(app, "q-1"), "suppressed", "never owed a notification");
});

test("a machine's first push notifies none of its backlog of questions", async () => {
  const notifier = new RecordingNotifier();
  const app = trainsApp({ notifier });
  await app.push([snapshot()]);
  app.at(60);
  await app.push([snapshot({ questions: [question("q-1"), question("q-2")] })]);
  assert.deepEqual(notifier.sent, [NEEDS_YOU], "only q-2, new after the first push");
  assert.equal(notifyState(app, "q-1"), "suppressed");
});

test("paused notifies once per entry: staying paused is silent, working then paused notifies again", async () => {
  const { notifier, pushLater } = await primedApp();
  const paused = () => snapshot({ state: "paused", questions: [] });
  await pushLater([paused()]);
  await pushLater([{ ...paused(), state_reason: "still paused" }]);
  assert.deepEqual(notifier.sent, [PAUSED]);
  await pushLater([snapshot({ state: "working", questions: [] })]);
  await pushLater([paused()]);
  assert.deepEqual(notifier.sent, [PAUSED, PAUSED]);
});

test("failed notifies, and moving from paused to failed notifies again", async () => {
  const { notifier, pushLater } = await primedApp();
  await pushLater([snapshot({ state: "paused", questions: [] })]);
  await pushLater([snapshot({ state: "failed", questions: [] })]);
  assert.deepEqual(notifier.sent.map((sent) => sent.subject), [PAUSED.subject, "Work Trains: example-app failed"]);
});

test(`at most ${DAILY_EMAIL_CAP} emails a UTC day; the rest are marked suppressed; the next day sends`, async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const { app, notifier, pushLater } = await primedApp();
  const many = (prefix: string) => Array.from({ length: 12 }, (_, i) => question(`${prefix}-${i}`));
  await pushLater([snapshot({ questions: many("a") })]);
  await pushLater([snapshot({ questions: [...many("a"), ...many("b")].slice(0, 20) })]);
  await pushLater([snapshot({ key: "e".repeat(32), questions: many("c") })]);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP);
  assert.equal(notifyState(app, "c-11"), "suppressed");
  assert.equal(logged.length, 12);
  assert.deepEqual(JSON.parse(logged[0]), { notify: "daily_cap_reached", key: "e".repeat(32) });

  app.at(24 * 60 * 60);
  await app.push([snapshot({ key: "d".repeat(32), questions: [question("d-1")] })]);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP + 1);
});

test("a failed send marks the question failed and is never retried", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const { app, notifier, pushLater } = await primedApp();
  notifier.failing = true;
  const reply = await pushLater([snapshot()]);
  assert.equal(reply.status, 200, "a failed notification never fails the push");
  assert.equal(notifyState(app, "q-1"), "failed");
  assert.deepEqual(JSON.parse(logged[0]), { notify: "send_failed", key: KEY, detail: "Error" });

  notifier.failing = false;
  await pushLater([snapshot({ state_reason: "still waiting" })]);
  assert.deepEqual(notifier.sent, []);
});

test("no notification carries question text", async () => {
  const { notifier, pushLater } = await primedApp();
  const secretish = { subject: "SUBJECT-TEXT?", body: "BODY-TEXT", recommended: "RECOMMENDED-TEXT", from: "FROM-TEXT" };
  await pushLater([snapshot({ questions: [question("q-1", secretish)] })]);
  await pushLater([snapshot({ state: "paused", questions: [question("q-1", secretish)] })]);
  assert.equal(notifier.sent.length, 2);
  for (const sent of notifier.sent) {
    for (const text of Object.values(secretish)) assert.ok(!JSON.stringify(sent).includes(text), text);
  }
});

test("a control character in the project name cannot break the subject line", async () => {
  const { notifier, pushLater } = await primedApp();
  await pushLater([snapshot({ name: "evil\r\nBcc: someone", questions: [question("q-1")] })]);
  assert.equal(notifier.sent[0].subject, "Work Trains: evil Bcc: someone needs you");
});

test("email goes through the send_email binding to NOTIFY_TO from NOTIFY_FROM", async () => {
  const messages: unknown[] = [];
  const binding = { send: async (message: unknown) => void messages.push(message) } as unknown as SendEmail;
  const ctx = (env: Json, cfg: Json) => ({ env, cfg }) as never;
  const configured = { notifyFrom: "trains@example.com", notifyTo: "owner@example.com" };

  const email = notifierFor(ctx({ EMAIL: binding }, configured));
  assert.ok(email instanceof EmailNotifier);
  await email.send({ subject: "s", text: "t" });
  assert.deepEqual(messages, [{ to: "owner@example.com", from: "trains@example.com", subject: "s", text: "t" }]);

  assert.ok(notifierFor(ctx({}, configured)) instanceof DisabledNotifier);
  assert.ok(notifierFor(ctx({ EMAIL: binding }, { notifyFrom: null, notifyTo: null })) instanceof DisabledNotifier);
});

test("with email off the Worker logs `disabled` once and still claims, so turning it on replays nothing", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const app = trainsApp({ notifier: new DisabledNotifier() });
  await app.push([snapshot({ key: "f".repeat(32), state: "idle", questions: [] })]);
  app.at(60);
  await app.push([snapshot({ questions: [question("q-1"), question("q-2")] })]);
  assert.deepEqual(logged.map((line) => JSON.parse(line)), [{ notify: "disabled" }]);
  assert.equal(notifyState(app, "q-1"), "sent");
});

const questions = (prefix: string, count: number) =>
  Array.from({ length: count }, (_, i) => question(`${prefix}-${i}`));
const countByNotifyState = (app: TrainsApp) =>
  Object.fromEntries(
    (app.db.sqlite.prepare("SELECT notify_state, count(*) AS n FROM questions GROUP BY notify_state").all() as Json[])
      .map((row) => [row.notify_state, row.n]),
  );

test("the notifier's D1 queries do not grow with the number of notifications a push sends", async () => {
  const pushCost = async (questionCount: number) => {
    const { app, notifier, pushLater } = await primedApp(undefined, QUERIES_PER_INVOCATION);
    app.budget.peak = 0;
    await pushLater([snapshot({ questions: questions("q", questionCount) })]);
    assert.equal(notifier.sent.length, questionCount);
    return app.budget.peak;
  };
  // The push itself writes one statement per question; everything beyond that is the notifier's.
  assert.equal((await pushCost(DAILY_EMAIL_CAP)) - (await pushCost(1)), DAILY_EMAIL_CAP - 1);
});

test("a push too big for one invocation still leaves the notifier its queries, so what lands is claimed at once", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const { app, notifier, pushLater } = await primedApp(undefined, QUERIES_PER_INVOCATION);
  const reply = await pushLater([
    snapshot({ key: "a".repeat(32), questions: questions("a", 20) }),
    snapshot({ key: "b".repeat(32), questions: questions("b", 20) }),
    snapshot({ key: "c".repeat(32), questions: questions("c", 1) }),
  ]);
  assert.deepEqual(reply.body!.projects.map((project: Json) => project.result), ["accepted", "deferred", "accepted"]);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP, "a-* and c-0 landed, and the notifier ran in the same push");
  assert.deepEqual(countByNotifyState(app), { pending: 1, sent: DAILY_EMAIL_CAP });
  assert.ok(!logged.some((line) => JSON.parse(line).notify === "deferred"));
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
});

// A push keeps NOTIFY_QUERIES_PER_PUSH in hand for the notifier, so the notifier's own guard is reached
// only past that reserve; these tests run the hook directly in an invocation that has spent the rest.
const PRIMED_KEY = "f".repeat(32);

/** Stores `count` open blocking questions owed a notification, as earlier pushes would have left them. */
function owe(app: TrainsApp, prefix: string, count: number) {
  const insert = app.db.sqlite.prepare(
    `INSERT INTO questions (id, project_key, asked_at, asked_by, subject, body, recommended, blocking, first_seen_at)
     VALUES (?, ?, ?, 'conductor', 'Question?', 'Context.', NULL, 1, ?)`,
  );
  for (let i = 0; i < count; i++) insert.run(`${prefix}-${i}`, PRIMED_KEY, iso(i), iso(i));
}

/** Runs the notifier as the push hook would, in an invocation with only `queriesLeft` of its queries left. */
async function notifyWithQueriesLeft(app: TrainsApp, queriesLeft: number): Promise<void> {
  const spent = QUERIES_PER_INVOCATION - queriesLeft;
  const queries = new QueryCounter();
  queries.count(spent);
  const budget = new QueryBudget(QUERIES_PER_INVOCATION);
  budget.take(spent);
  const tasks: Promise<unknown>[] = [];
  const ctx = {
    db: countingDb(budgeted(app.db, budget) as never, queries),
    queries,
    now: START,
    cfg: { publicOrigin: ORIGIN },
    waitUntil: (task: Promise<unknown>) => void tasks.push(task),
  };
  await notifyOnAccepted(() => app.notifier).run(ctx as never, []);
  await Promise.all(tasks);
}

const notifyUntilNonePending = async (app: TrainsApp) => {
  for (let run = 1; countByNotifyState(app).pending !== undefined; run++) {
    assert.ok(run <= 2, "a run claims up to a day's cap, so 38 owed questions take two");
    await notifyWithQueriesLeft(app, NOTIFY_QUERIES_PER_PUSH);
  }
};

const CLAIMS_ONLY_WITH_ROOM = "the notifier claims nothing unless its final write fits too";

test(`${CLAIMS_ONLY_WITH_ROOM}, so failed sends never stay marked sent`, async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const { app, notifier } = await primedApp(undefined, QUERIES_PER_INVOCATION);
  owe(app, "q", 38);
  notifier.failing = true;

  await notifyWithQueriesLeft(app, NOTIFY_QUERIES_PER_PUSH - 1);
  assert.deepEqual(countByNotifyState(app), { pending: 38 }, "deferred: a claim here would strand its final write");
  assert.deepEqual(JSON.parse(logged[0]), { notify: "deferred", queries_left: NOTIFY_QUERIES_PER_PUSH - 1 });

  await notifyUntilNonePending(app);
  assert.deepEqual(countByNotifyState(app), { failed: DAILY_EMAIL_CAP, suppressed: 38 - DAILY_EMAIL_CAP });
  assert.ok(!logged.some((line) => JSON.parse(line).notify === "error"), "no final write was cut off");
});

test(`${CLAIMS_ONLY_WITH_ROOM}, so capped questions end suppressed`, async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const { app, notifier } = await primedApp(undefined, QUERIES_PER_INVOCATION);
  owe(app, "e", DAILY_EMAIL_CAP);
  await notifyWithQueriesLeft(app, NOTIFY_QUERIES_PER_PUSH);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP, "today's cap is spent");
  owe(app, "q", 38);

  await notifyWithQueriesLeft(app, NOTIFY_QUERIES_PER_PUSH - 1);
  assert.equal(countByNotifyState(app).pending, 38);
  await notifyUntilNonePending(app);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP);
  assert.deepEqual(countByNotifyState(app), { sent: DAILY_EMAIL_CAP, suppressed: 38 }, "only e-* was sent");
  assert.ok(!logged.some((line) => JSON.parse(line).notify === "error"), "no final write was cut off");
});
