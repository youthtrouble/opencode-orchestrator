// A push under the Workers Free plan's 50 D1 queries per invocation (SI-3). The Mac sends up to 50
// projects a push with no idea of their D1 cost, so the Worker must fit a push to the limit itself:
// take what fits, answer `deferred` for the rest, and let the Mac send that next poll.
import test from "node:test";
import assert from "node:assert/strict";
import { POLL_AFTER_SECONDS } from "../src/machine/pull.ts";
import { DEBOUNCE_SECONDS } from "../src/machine/push.ts";
import { DAILY_EMAIL_CAP } from "../src/notify/triggers.ts";
import { QUERIES_PER_INVOCATION } from "../src/queries.ts";
import { MACHINE_ID, question, snapshot, trainsApp, type Json, type Reply, type TrainsApp } from "./support/trains.ts";


interface Backlog {
  projects: number;
  questions: number;
}

const projectKey = (index: number) => (index + 1).toString(16).padStart(32, "0");
/** A distinct hash per project and generation, so a generation's re-send is the same snapshot. */
const snapshotHash = (generation: number, index: number) =>
  (generation * 1000 + index + 1).toString(16).padStart(64, "0");

/** Every project of the backlog, generation `generation`, each with its own blocking questions. */
function backlog({ projects, questions }: Backlog, generation: number): Json[] {
  return Array.from({ length: projects }, (_, index) =>
    snapshot({
      key: projectKey(index),
      name: `project-${index}`,
      state: questions === 0 ? "idle" : "needs_you",
      snapshot_hash: snapshotHash(generation, index),
      questions: Array.from({ length: questions }, (_, n) => question(`g${generation}-p${index}-q${n}`)),
    }),
  );
}

interface Delivery {
  statuses: number[];
  /** The most queries any one of the pushes ran. */
  peak: number;
  /** What the Mac still had to send when it gave up. */
  undelivered: number;
}

/**
 * Sends `projects` as the Mac's loop does, one push per poll: a project the Worker took (accepted or
 * unchanged) is done, one it deferred goes again next poll, and after a failed push all of them go
 * again. Gives up after `maxPushes`.
 */
async function deliver(app: TrainsApp, clock: { seconds: number }, projects: Json[], maxPushes = 60) {
  const delivery: Delivery = { statuses: [], peak: 0, undelivered: projects.length };
  let due = projects;
  while (due.length > 0 && delivery.statuses.length < maxPushes) {
    app.budget.peak = 0;
    const reply = await app.push(due);
    delivery.statuses.push(reply.status);
    delivery.peak = Math.max(delivery.peak, app.budget.peak);
    due = due.filter((project) => !wasTaken(reply, project.key));
    clock.seconds += POLL_AFTER_SECONDS;
    app.at(clock.seconds);
  }
  delivery.undelivered = due.length;
  return delivery;
}

function wasTaken(reply: Reply, key: string): boolean {
  if (reply.status !== 200) return false;
  const result = reply.body!.projects.find((project: Json) => project.key === key);
  return result.result !== "deferred";
}

const OTHER_MACHINE_ID = "00000000-0000-4000-8000-000000000002";

const countRows = (app: TrainsApp, sql: string) => (app.db.sqlite.prepare(sql).get() as { n: number }).n;
const firstPushAt = (app: TrainsApp, machineId = MACHINE_ID) =>
  (app.db.sqlite.prepare("SELECT first_push_at FROM machines WHERE id = ?").get(machineId) as Json).first_push_at;

const backlogs: Backlog[] = [10, 24, 50].flatMap((projects) =>
  [0, 3, 20].map((questions) => ({ projects, questions })),
);

for (const size of backlogs) {
  const label = `${size.projects} projects x ${size.questions} questions`;

  test(`a first push of ${label} lands whole within the query limit, its backlog suppressed`, async (t) => {
    t.mock.method(console, "log", () => {});
    const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
    const delivery = await deliver(app, { seconds: 0 }, backlog(size, 1));
    t.diagnostic(`pushes ${delivery.statuses.join(",")} peak ${delivery.peak}`);

    assert.equal(delivery.undelivered, 0, "the Mac delivered every project");
    assert.ok(delivery.statuses.every((status) => status === 200), `statuses ${delivery.statuses}`);
    assert.ok(delivery.peak <= QUERIES_PER_INVOCATION);
    assert.equal(countRows(app, "SELECT count(*) AS n FROM projects"), size.projects);
    assert.equal(countRows(app, "SELECT count(*) AS n FROM questions WHERE notify_state = 'suppressed'"),
      size.projects * size.questions, "a first push's backlog never notifies, however many pushes it takes");
    assert.notEqual(firstPushAt(app), null);
  });

  test(`a steady-state push of ${label} lands whole within the query limit and notifies`, async (t) => {
    t.mock.method(console, "log", () => {});
    const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
    const clock = { seconds: 0 };
    await deliver(app, clock, backlog({ projects: size.projects, questions: 0 }, 1));
    clock.seconds += 60;
    app.at(clock.seconds);

    const delivery = await deliver(app, clock, backlog(size, 2));
    t.diagnostic(`pushes ${delivery.statuses.join(",")} peak ${delivery.peak}`);

    assert.equal(delivery.undelivered, 0, "the Mac delivered every project");
    assert.ok(delivery.statuses.every((status) => status === 200), `statuses ${delivery.statuses}`);
    assert.ok(delivery.peak <= QUERIES_PER_INVOCATION);
    assert.equal(countRows(app, "SELECT count(*) AS n FROM questions"), size.projects * size.questions);
    assert.equal(app.notifier.sent.length, Math.min(DAILY_EMAIL_CAP, size.projects * size.questions));
  });
}

// By the push's own arithmetic a first push costs 3 queries and a new question-less project 2, and the
// push keeps 1 for the first-push stamp and 5 for the notifier: 3 + 21 * 2 + 5 = 50.
const LARGEST_WHOLE_FIRST_PUSH = 21;

test(`the largest first push of question-less projects that fits whole is ${LARGEST_WHOLE_FIRST_PUSH}`, async (t) => {
  t.mock.method(console, "log", () => {});
  const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
  const reply = await app.push(backlog({ projects: LARGEST_WHOLE_FIRST_PUSH, questions: 0 }, 1));
  assert.equal(reply.status, 200);
  assert.ok(reply.body!.projects.every((project: Json) => project.result === "accepted"));
  assert.notEqual(firstPushAt(app), null);
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
});

test("one project past that is deferred to the next poll, and the first push ends only when it lands", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
  const projects = backlog({ projects: LARGEST_WHOLE_FIRST_PUSH + 1, questions: 0 }, 1);

  const reply = await app.push(projects);
  assert.equal(reply.status, 200);
  const results = reply.body!.projects as Json[];
  assert.deepEqual(results.at(-1), { key: projectKey(LARGEST_WHOLE_FIRST_PUSH), result: "deferred", retry_after_seconds: 1 });
  assert.ok(results.slice(0, -1).every((project) => project.result === "accepted"));
  assert.equal(firstPushAt(app), null, "a deferred project has not landed, so the backlog window stays open");
  assert.deepEqual(logged.map((line) => JSON.parse(line)), [{ push: "query_budget", deferred: 1 }]);

  app.at(POLL_AFTER_SECONDS);
  const retried = await app.push([projects.at(-1)!]);
  assert.equal(retried.body!.projects[0].result, "accepted");
  assert.notEqual(firstPushAt(app), null);
});

test("a debounced first-sync continuation keeps its retry's blocking question suppressed", async (t) => {
  t.mock.method(console, "log", () => {});
  const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
  const projects = backlog({ projects: LARGEST_WHOLE_FIRST_PUSH + 1, questions: 0 }, 1);
  const opening = await app.push(projects);
  assert.equal(opening.status, 200);
  assert.equal(opening.body!.projects.at(-1).result, "deferred");
  assert.equal(firstPushAt(app), null);

  const changed = { ...projects[0], snapshot_hash: snapshotHash(2, 0), questions: [question("continued-q")] };
  app.at(30);
  const continuation = await app.push([changed, projects.at(-1)!]);
  assert.equal(continuation.status, 200);
  assert.deepEqual(continuation.body!.projects.map((project: Json) => project.result), ["deferred", "accepted"]);
  assert.equal(continuation.body!.projects[0].retry_after_seconds, 25);
  assert.equal(firstPushAt(app), null, "a non-budget deferral also keeps the first-sync window open");

  app.at(60);
  const retried = await app.push([changed]);
  assert.equal(retried.status, 200);
  assert.equal(retried.body!.projects[0].result, "accepted");
  assert.notEqual(firstPushAt(app), null, "the first sync completes when the deferred project lands");
  assert.equal(countRows(app, "SELECT count(*) AS n FROM questions WHERE notify_state = 'suppressed'"), 1);
  assert.deepEqual(app.notifier.sent, []);
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
});

test("a key owned by another machine does not hold a first sync open", async (t) => {
  t.mock.method(console, "log", () => {});
  const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
  const [shared, own] = backlog({ projects: 2, questions: 0 }, 1);
  await app.push([shared], OTHER_MACHINE_ID);

  app.at(60);
  const first = await app.push([shared, own]);
  assert.equal(first.status, 200);
  assert.deepEqual(first.body!.projects.map((project: Json) => [project.result, project.retry_after_seconds]),
    [["deferred", null], ["accepted", null]]);
  assert.notEqual(firstPushAt(app), null, "a deferral with no retry never lands, so it cannot end the first sync");

  app.at(60 + DEBOUNCE_SECONDS);
  const later = { ...own, snapshot_hash: snapshotHash(2, 1), questions: [question("after-first-sync")] };
  assert.equal((await app.push([shared, later])).status, 200);
  assert.equal(countRows(app, "SELECT count(*) AS n FROM questions WHERE notify_state = 'suppressed'"), 0);
  assert.equal(app.notifier.sent.length, 1, "a question after the first sync notifies");
});

// The notifier works from what D1 says is owed, not from what the push that landed it accepted, so a
// project that landed in a push that then failed is still notified: by the next push that accepts.
test("a blocking question that landed in a failed push is notified by the retry", async (t) => {
  t.mock.method(console, "log", () => {});
  const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
  await app.push([snapshot({ key: "f".repeat(32), state: "idle", questions: [] })]);
  app.at(60);
  const landed = snapshot({ key: "a".repeat(32), snapshot_hash: "a".repeat(64), questions: [question("q-a")] });
  // A duplicate question id fails this project's batch after the first project committed.
  const broken = snapshot({ key: "b".repeat(32), questions: [question("q-b"), question("q-b")] });
  assert.equal((await app.push([landed, broken])).status, 500);
  assert.deepEqual(app.notifier.sent, []);

  app.at(60 + POLL_AFTER_SECONDS);
  const retried = await app.push([landed, snapshot({ key: "b".repeat(32), questions: [question("q-b")] })]);
  assert.deepEqual(retried.body!.projects.map((project: Json) => project.result), ["unchanged", "accepted"]);
  assert.equal(app.notifier.sent.length, 2, "q-a, from the failed push, and q-b");
});

// The costliest project a push can carry: 20 open questions all replaced by 20 new ones, on a first push.
// The push's 2, its read, its question load and a batch of 41 make 45, and the stamp 46: it fits only by
// taking the notifier's 5, which a push lets the first project it lands do, so none is deferred for good.
test("the costliest project a push can carry still lands, even on a first push", async (t) => {
  t.mock.method(console, "log", () => {});
  const app = trainsApp({ queryLimit: QUERIES_PER_INVOCATION });
  const [costly, ...rest] = backlog({ projects: 12, questions: 0 }, 1);
  const withQuestions = (generation: number) =>
    ({ ...costly, snapshot_hash: snapshotHash(generation, 0), questions: backlog({ projects: 1, questions: 20 }, generation)[0].questions });
  const opening = await app.push([withQuestions(1), ...rest]);
  assert.equal(opening.body!.projects.at(-1).result, "deferred", "the first push is not over yet");

  app.at(60);
  const replaced = await app.push([withQuestions(2), rest.at(-1)!]);
  assert.deepEqual(replaced.body!.projects.map((project: Json) => project.result), ["accepted", "deferred"]);
  assert.ok(app.budget.peak <= QUERIES_PER_INVOCATION);
  assert.equal(countRows(app, "SELECT count(*) AS n FROM questions WHERE closed_at IS NULL"), 20);

  app.at(60 + POLL_AFTER_SECONDS);
  assert.equal((await app.push([rest.at(-1)!])).body!.projects[0].result, "accepted");
  assert.notEqual(firstPushAt(app), null);
  assert.equal(countRows(app, "SELECT count(*) AS n FROM questions WHERE notify_state <> 'suppressed'"), 0);
});
