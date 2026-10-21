import test from "node:test";
import assert from "node:assert/strict";
import { ORIGIN } from "./support/access.ts";
import { clientKey, iso, KEY, question, snapshot, trainsApp, type Json, type TrainsApp } from "./support/trains.ts";

const OTHER_KEY = "fedcba9876543210fedcba9876543210";
const UNKNOWN_ID = "00000000-0000-4000-8000-0000000000ff";

/** A pushed project (needs_you) with question q-1 (recommended "Yes.") and q-2 (no recommendation). */
async function appWithQuestions(env: Json = {}) {
  const app = trainsApp({ env });
  const questions = [question("q-1"), question("q-2", { recommended: null })];
  assert.equal((await app.push([snapshot({ questions })])).status, 200);
  return app;
}

const answerRow = (app: TrainsApp, id: string) =>
  app.db.sqlite.prepare("SELECT * FROM answers WHERE id = ?").get(id) as Json;

test("an empty Worker summarises to no machine, no budget and no projects, naming PUBLIC_ORIGIN", async () => {
  const reply = await trainsApp().summary();
  assert.equal(reply.status, 200);
  assert.deepEqual(reply.body, {
    v: 1,
    server_time: iso(0),
    machine: null,
    budget: null,
    undo_seconds: 10,
    public_origin: ORIGIN,
    projects: [],
  });
});

test("the summary orders needs_you, failed, paused, working, done, idle, newest first within a state", async () => {
  const app = trainsApp();
  const states = ["idle", "working", "done", "paused", "failed", "needs_you"];
  await app.push(states.map((state, i) => snapshot({ key: String(i).repeat(32), state, questions: [] })));
  app.at(60);
  await app.push([snapshot({ key: "a".repeat(32), state: "working", questions: [] })]);

  const { body } = await app.summary();
  assert.deepEqual(
    body!.projects.map((project: Json) => [project.state, project.key[0]]),
    [
      ["needs_you", "5"],
      ["failed", "4"],
      ["paused", "3"],
      ["working", "a"],
      ["working", "1"],
      ["done", "2"],
      ["idle", "0"],
    ],
  );
});

test("the summary carries the machine, the snapshot fields, open questions and hidden projects", async () => {
  const app = await appWithQuestions();
  await app.setHidden(KEY, true);
  await app.pull();

  const { body } = await app.summary();
  assert.deepEqual(body!.machine, { orch_version: "0.9.0", last_pull_at: iso(0), last_push_at: iso(0) });
  assert.deepEqual(body!.budget, { anthropic: null, openai: null });
  const [project] = body!.projects;
  assert.equal(project.hidden, true);
  assert.equal(project.state_reason, "1 blocking question");
  assert.deepEqual(project.run, { kind: "none", since: null, last_activity_at: null });
  assert.equal(project.pending_action, null);
  assert.deepEqual(project.questions[0], { ...question("q-1"), at: "2026-10-20T11:00:00Z", answer: null });
  assert.deepEqual(project.questions.map((q: Json) => q.id), ["q-1", "q-2"]);
});

test("the summary leaves closed questions out", async () => {
  const app = await appWithQuestions();
  app.at(60);
  await app.push([snapshot({ questions: [question("q-2", { recommended: null })] })]);
  const { body } = await app.summary();
  assert.deepEqual(body!.projects[0].questions.map((q: Json) => q.id), ["q-2"]);
});

test("the summary carries an action question's ask and leaves it out for a decision; a re-push updates it", async () => {
  const app = trainsApp();
  const action = question("q-3", { recommended: null, ask: "action" });
  await app.push([snapshot({ questions: [action, question("q-1")] })]);
  const asks = async () => (await app.summary()).body!.projects[0].questions.map((q: Json) => [q.id, q.ask]);
  assert.deepEqual(await asks(), [["q-1", undefined], ["q-3", "action"]]);

  app.at(60);
  await app.push([snapshot({ questions: [{ ...action, ask: "decision" }, question("q-1")] })]);
  assert.deepEqual(await asks(), [["q-1", undefined], ["q-3", "decision"]]);
});

test("an action question has no recommendation, so accept is refused and a typed answer goes through", async () => {
  const app = trainsApp();
  await app.push([snapshot({ questions: [question("q-3", { recommended: null, ask: "action" })] })]);
  assert.equal((await app.answer("q-3", { kind: "accept", client_key: clientKey(1) })).status, 400);
  const typed = await app.answer("q-3", { kind: "typed", text: "Owner reports done (unverified).", client_key: clientKey(2) });
  assert.equal(typed.status, 201);
  assert.equal(typed.body!.answer.text, "Owner reports done (unverified).");
});

test("undo_seconds comes from UNDO_SECONDS, defaults to 10 and is clamped to 1..60", async () => {
  for (const [configured, expected] of [[undefined, 10], ["abc", 10], ["0", 1], ["25", 25], ["999", 60]] as const) {
    const app = trainsApp({ env: { UNDO_SECONDS: configured } });
    assert.equal((await app.summary()).body!.undo_seconds, expected, String(configured));
  }
});

test("accept stores the recommendation the Worker holds, ignoring client text, and opens the undo window", async () => {
  const app = await appWithQuestions({ UNDO_SECONDS: "25" });
  const reply = await app.answer("q-1", { kind: "accept", text: "Something else.", client_key: clientKey(1) });
  assert.equal(reply.status, 201);
  const { id, ...answer } = reply.body!.answer;
  assert.deepEqual(answer, { kind: "accept", text: "Yes.", status: "undo_window", apply_after: iso(25), detail: null });
  assert.equal(answerRow(app, id).client_key, clientKey(1));
});

test("accept on a question with no recommendation is 400 invalid_request", async () => {
  const app = await appWithQuestions();
  const reply = await app.answer("q-2", { kind: "accept", client_key: clientKey(1) });
  assert.equal(reply.status, 400);
  assert.deepEqual(reply.body, {
    error: "invalid_request",
    message: "this question has no recommendation; reply instead",
  });
});

test("a typed answer is stored trimmed, without control characters other than newline and tab", async () => {
  const app = await appWithQuestions();
  const text = "  Use\u0007 the flag,\r\n\tthen ship.\u0000 ";
  const reply = await app.answer("q-2", { kind: "typed", text, client_key: clientKey(1) });
  assert.equal(reply.status, 201);
  assert.equal(reply.body!.answer.text, "Use the flag,\n\tthen ship.");
});

test("a typed answer without text, with only blanks, or over 4000 UTF-16 units is 400 invalid_request", async () => {
  const app = await appWithQuestions();
  // 2001 astral characters pass the schema's 4000 code points but are 4002 UTF-16 units.
  const bodies = [{}, { text: " \u0001\n " }, { text: "\u{1F600}".repeat(2001) }];
  for (const [i, body] of bodies.entries()) {
    const reply = await app.answer("q-1", { kind: "typed", client_key: clientKey(i + 1), ...body });
    assert.equal(reply.status, 400, JSON.stringify(body).slice(0, 40));
    assert.equal(reply.body!.error, "invalid_request");
  }
  const longest = await app.answer("q-1", { kind: "typed", text: "x".repeat(4000), client_key: clientKey(9) });
  assert.equal(longest.status, 201);
});

test("an answer to an unknown question is 404, to a closed one 409 conflict", async () => {
  const app = await appWithQuestions();
  assert.equal((await app.answer("q-9", { kind: "accept", client_key: clientKey(1) })).status, 404);
  app.at(60);
  await app.push([snapshot({ questions: [] })]);
  const closed = await app.answer("q-1", { kind: "accept", client_key: clientKey(2) });
  assert.equal(closed.status, 409);
  assert.equal(closed.body!.error, "conflict");
});

test("a retried tap returns the same answer with 200; a second, distinct tap is 409 conflict", async () => {
  const app = await appWithQuestions();
  const first = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  app.at(2);
  const retried = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  assert.equal(retried.status, 200);
  assert.deepEqual(retried.body, first.body);

  const typed = "a reply that must not be echoed";
  const second = await app.answer("q-1", { kind: "typed", text: typed, client_key: clientKey(2) });
  assert.equal(second.status, 409);
  assert.equal(second.body!.error, "conflict");
  assert.ok(second.body!.message.includes(first.body!.answer.id));
  assert.ok(!second.body!.message.includes(typed) && !second.body!.message.includes("Yes."));
  assert.equal((app.db.sqlite.prepare("SELECT count(*) AS n FROM answers").get() as Json).n, 1);
});

test("a retry after the question closed still returns the stored answer with 200", async () => {
  const app = await appWithQuestions();
  const first = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  app.at(60);
  await app.push([snapshot({ questions: [question("q-2", { recommended: null })] })]);
  const retried = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  assert.deepEqual([retried.status, retried.body!.answer.id], [200, first.body!.answer.id]);
});

test("a retried typed answer matches on its cleaned text; another answer under its client_key is 409", async () => {
  const app = await appWithQuestions();
  const first = await app.answer("q-1", { kind: "typed", text: "Ship it.", client_key: clientKey(1) });
  const retried = await app.answer("q-1", { kind: "typed", text: " Ship it.\u0007 ", client_key: clientKey(1) });
  assert.deepEqual([retried.status, retried.body!.answer.id], [200, first.body!.answer.id]);
  for (const body of [{ kind: "typed", text: "Hold it." }, { kind: "accept" }]) {
    const reused = await app.answer("q-1", { ...body, client_key: clientKey(1) });
    assert.deepEqual([reused.status, reused.body!.error], [409, "conflict"], JSON.stringify(body));
  }
});

test("a client_key already used for another question is 409 conflict", async () => {
  const app = await appWithQuestions();
  await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  const reply = await app.answer("q-2", { kind: "typed", text: "No.", client_key: clientKey(1) });
  assert.equal(reply.status, 409);
  assert.equal(reply.body!.error, "conflict");
});

test("undo works inside the window only; an undone answer is never pulled and frees the question", async () => {
  const app = await appWithQuestions();
  const { body } = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  const answerId = body!.answer.id;

  app.at(5);
  const undone = await app.undoAnswer(answerId);
  assert.equal(undone.status, 200);
  assert.deepEqual([undone.body!.answer.status, undone.body!.answer.detail], ["rejected", "undone"]);
  assert.equal(answerRow(app, answerId).cancelled_at, iso(5));
  const again = await app.undoAnswer(answerId);
  assert.deepEqual([again.status, again.body!.error], [409, "too_late"]);

  const replacement = await app.answer("q-1", { kind: "typed", text: "No.", client_key: clientKey(2) });
  assert.equal(replacement.status, 201);
  const replacementId = replacement.body!.answer.id;

  app.at(16);
  const late = await app.undoAnswer(replacementId);
  assert.deepEqual([late.status, late.body!.error], [409, "too_late"]);
  assert.equal(late.body!.message, "the undo window has passed");
  const pulled = await app.pull();
  assert.deepEqual(pulled.body!.answers.map((answer: Json) => answer.id), [replacementId]);
});

test("undo at +11 s of a 10 s window is 409 too_late; an unknown answer is 404", async () => {
  const app = await appWithQuestions();
  const { body } = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  app.at(10);
  assert.equal((await app.undoAnswer(body!.answer.id)).status, 409, "the window is open only while now < apply_after");
  app.at(11);
  assert.equal((await app.undoAnswer(body!.answer.id)).status, 409);
  assert.equal((await app.undoAnswer(UNKNOWN_ID)).status, 404);
});

test("the summary shows an answer's live status: undo_window, then sent, then the Mac's outcome", async () => {
  const app = await appWithQuestions();
  const { body } = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  const status = async () => {
    const answer = (await app.summary()).body!.projects[0].questions[0].answer;
    return [answer.status, answer.detail];
  };
  assert.deepEqual(await status(), ["undo_window", null]);
  app.at(10);
  assert.deepEqual(await status(), ["sent", null], "past its window, not yet pulled");
  await app.pull();
  assert.deepEqual(await status(), ["sent", null]);
  await app.ack([{ id: body!.answer.id, type: "answer", outcome: "superseded", detail: "answered in the terminal" }]);
  assert.deepEqual(await status(), ["superseded", "answered in the terminal"]);
});

test("pause opens an undoable action the summary shows; a retry is 200, a second tap 409", async () => {
  const app = await appWithQuestions();
  const reply = await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  assert.equal(reply.status, 201);
  const { id, ...action } = reply.body!.action;
  assert.deepEqual(action, { kind: "pause", apply_after: iso(10), status: "undo_window", detail: null });
  assert.deepEqual((await app.summary()).body!.projects[0].pending_action, { id, kind: "pause", apply_after: iso(10) });

  const retried = await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  assert.deepEqual([retried.status, retried.body!.action.id], [200, id]);
  const second = await app.act(KEY, { kind: "resume", client_key: clientKey(2) });
  assert.deepEqual([second.status, second.body!.error], [409, "conflict"]);
});

test("a retry after the Mac paused the project still returns the stored pause with 200", async () => {
  const app = await appWithQuestions();
  const first = await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  app.at(60);
  await app.push([snapshot({ state: "paused", questions: [] })]);
  const retried = await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  assert.deepEqual([retried.status, retried.body!.action.id], [200, first.body!.action.id]);
});

test("a resume under the client_key of a pause is 409 conflict", async () => {
  const app = await appWithQuestions();
  await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  const reused = await app.act(KEY, { kind: "resume", client_key: clientKey(1) });
  const message = "this client_key was used for a pause";
  assert.deepEqual([reused.status, reused.body!.error, reused.body!.message], [409, "conflict", message]);
});

test("an action undoes inside the window only; once undone the project takes a new one", async () => {
  const app = await appWithQuestions();
  const { body } = await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  app.at(5);
  const undone = await app.undoAction(body!.action.id);
  assert.equal(undone.status, 200);
  assert.deepEqual([undone.body!.action.status, undone.body!.action.detail], ["rejected", "undone"]);
  assert.equal((await app.summary()).body!.projects[0].pending_action, null);

  const next = await app.act(KEY, { kind: "pause", client_key: clientKey(2) });
  assert.equal(next.status, 201);
  app.at(16);
  assert.deepEqual((await app.undoAction(next.body!.action.id)).body!.error, "too_late");
  assert.equal((await app.undoAction(UNKNOWN_ID)).status, 404);
  assert.deepEqual((await app.pull()).body!.actions.map((action: Json) => action.id), [next.body!.action.id]);
});

test("pause on a paused project and resume on one that is neither paused nor needs_you are 409 conflict", async () => {
  const app = trainsApp();
  await app.push([
    snapshot({ state: "paused", questions: [] }),
    snapshot({ key: OTHER_KEY, state: "working", questions: [] }),
  ]);
  const paused = await app.act(KEY, { kind: "pause", client_key: clientKey(1) });
  assert.deepEqual([paused.status, paused.body!.error, paused.body!.message], [409, "conflict", "already paused"]);
  const resumeWorking = await app.act(OTHER_KEY, { kind: "resume", client_key: clientKey(2) });
  assert.deepEqual([resumeWorking.status, resumeWorking.body!.error], [409, "conflict"]);

  assert.equal((await app.act(KEY, { kind: "resume", client_key: clientKey(3) })).status, 201);
  assert.equal((await app.act(OTHER_KEY, { kind: "pause", client_key: clientKey(4) })).status, 201);
});

test("resume is accepted on a needs_you project, which may be gated", async () => {
  const app = await appWithQuestions();
  assert.equal((await app.act(KEY, { kind: "resume", client_key: clientKey(1) })).status, 201);
});

test("an action on an unknown project is 404; a client_key used on another project is 409", async () => {
  const app = trainsApp();
  await app.push([snapshot({ questions: [] }), snapshot({ key: OTHER_KEY, state: "working", questions: [] })]);
  assert.equal((await app.act("9".repeat(32), { kind: "pause", client_key: clientKey(1) })).status, 404);
  await app.act(KEY, { kind: "pause", client_key: clientKey(2) });
  const reused = await app.act(OTHER_KEY, { kind: "pause", client_key: clientKey(2) });
  assert.deepEqual([reused.status, reused.body!.error], [409, "conflict"]);
});

test("visibility hides and shows a project with 204; an unknown project is 404, a bad body 400", async () => {
  const app = await appWithQuestions();
  const hide = await app.setHidden(KEY, true);
  assert.deepEqual([hide.status, hide.body], [204, null]);
  assert.equal((await app.summary()).body!.projects[0].hidden, true);
  await app.setHidden(KEY, false);
  assert.equal((await app.summary()).body!.projects[0].hidden, false);

  assert.equal((await app.setHidden(OTHER_KEY, true)).status, 404);
  const bad = await app.setHidden(KEY, "yes");
  assert.deepEqual([bad.status, bad.body!.error], [400, "invalid_request"]);
});

test("browser request bodies are checked against the schema", async () => {
  const app = await appWithQuestions();
  const cases = [
    app.answer("q-1", { kind: "accept", client_key: "NOT-A-UUID" }),
    app.answer("q-1", { kind: "maybe", client_key: clientKey(1) }),
    app.act(KEY, { kind: "stop", client_key: clientKey(1) }),
  ];
  for (const reply of await Promise.all(cases)) {
    assert.deepEqual([reply.status, reply.body!.error], [400, "invalid_request"]);
  }
});
