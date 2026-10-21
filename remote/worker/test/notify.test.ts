import test from "node:test";
import assert from "node:assert/strict";
import { DisabledNotifier, EmailNotifier, notifierFor } from "../src/notify/notifier.ts";
import { DAILY_EMAIL_CAP } from "../src/notify/triggers.ts";
import { QUERIES_PER_INVOCATION } from "../src/queries.ts";
import { ORIGIN } from "./support/access.ts";
import { KEY, question, RecordingNotifier, snapshot, trainsApp, type Json, type TrainsApp } from "./support/trains.ts";

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

test("what a push leaves owed under the query limit is claimed by later pushes, until none is pending", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const { app, notifier, pushLater } = await primedApp(undefined, QUERIES_PER_INVOCATION);
  const big = [
    snapshot({ key: "a".repeat(32), questions: questions("a", 20) }),
    snapshot({ key: "b".repeat(32), questions: questions("b", 20) }),
    snapshot({ key: "c".repeat(32), questions: questions("c", 1) }),
  ];
  // The push's own writes leave the notifier no room in this invocation, so it claims nothing.
  assert.equal((await pushLater(big)).status, 200);
  assert.deepEqual(countByNotifyState(app), { pending: 41 });
  assert.deepEqual(JSON.parse(logged[0]), { notify: "deferred", queries_left: 1 });

  const nudge = (reason: string) =>
    pushLater([snapshot({ key: "c".repeat(32), state_reason: reason, questions: questions("c", 1) })]);
  for (let push = 1; countByNotifyState(app).pending !== undefined; push++) {
    assert.ok(push <= 3, "every owed question reaches a terminal state within three more pushes");
    assert.equal((await nudge(`push ${push}`)).status, 200);
  }
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP);
  assert.deepEqual(countByNotifyState(app), { sent: DAILY_EMAIL_CAP, suppressed: 41 - DAILY_EMAIL_CAP });
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
});

// After priming, three new projects holding 38 questions cost the push 46 queries: room for the claim
// batch (4) but not for the write after it, which records what was not sent.
const pushLeavingFourQueries = (pushLater: (projects: Json[]) => Promise<unknown>) =>
  pushLater([
    snapshot({ key: "a".repeat(32), questions: questions("a", 20) }),
    snapshot({ key: "b".repeat(32), questions: questions("b", 17) }),
    snapshot({ key: "c".repeat(32), questions: questions("c", 1) }),
  ]);
const nudgeUntilNonePending = async (app: TrainsApp, pushLater: (projects: Json[]) => Promise<unknown>) => {
  for (let push = 1; countByNotifyState(app).pending !== undefined; push++) {
    assert.ok(push <= 3, "every owed question reaches a terminal state within three more pushes");
    await pushLater([snapshot({ key: "c".repeat(32), state_reason: `push ${push}`, questions: questions("c", 1) })]);
  }
};

const CLAIMS_ONLY_WITH_ROOM = "the notifier claims nothing unless its final write fits too";

test(`${CLAIMS_ONLY_WITH_ROOM}, so failed sends never stay marked sent`, async (t) => {
  t.mock.method(console, "log", () => {});
  const { app, notifier, pushLater } = await primedApp(undefined, QUERIES_PER_INVOCATION);
  notifier.failing = true;
  await pushLeavingFourQueries(pushLater);
  assert.equal(app.budget.used, QUERIES_PER_INVOCATION - 4, "the push left exactly the claim batch's 4 queries");
  assert.deepEqual(countByNotifyState(app), { pending: 38 }, "deferred: a claim here would strand its final write");

  await nudgeUntilNonePending(app, pushLater);
  assert.deepEqual(countByNotifyState(app), { failed: DAILY_EMAIL_CAP, suppressed: 38 - DAILY_EMAIL_CAP });
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
});

test(`${CLAIMS_ONLY_WITH_ROOM}, so capped questions end suppressed`, async (t) => {
  t.mock.method(console, "log", () => {});
  const { app, notifier, pushLater } = await primedApp(undefined, QUERIES_PER_INVOCATION);
  await pushLater([snapshot({ key: "e".repeat(32), questions: questions("e", DAILY_EMAIL_CAP) })]);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP, "today's cap is spent");
  await pushLeavingFourQueries(pushLater);
  assert.equal(countByNotifyState(app).pending, 38);

  await nudgeUntilNonePending(app, pushLater);
  assert.equal(notifier.sent.length, DAILY_EMAIL_CAP);
  assert.deepEqual(countByNotifyState(app), { sent: DAILY_EMAIL_CAP, suppressed: 38 }, "only e-* was sent");
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
});
