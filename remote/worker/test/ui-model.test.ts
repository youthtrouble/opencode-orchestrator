// The page's view-model rules (public/model.js), tested without a DOM.
import test from "node:test";
import assert from "node:assert/strict";
import * as model from "../public/model.js";

const NOW = Date.parse("2026-10-20T12:00:00Z");
const secondsBefore = (seconds: number) => new Date(NOW - seconds * 1000).toISOString();

function question(id: string, at: string, blocking: boolean) {
  return { id, at, blocking, from: "conductor", subject: id, body: "", recommended: null, answer: null };
}

test("the Needs-you stack puts blocking questions first, then the oldest, across projects", () => {
  const projects = [
    { key: "a", name: "alpha", hidden: false, questions: [question("a-new-info", secondsBefore(10), false)] },
    {
      key: "b",
      name: "beta",
      hidden: true,
      questions: [question("b-new-block", secondsBefore(20), true), question("b-old-info", secondsBefore(90), false)],
    },
    { key: "c", name: "gamma", hidden: false, questions: [question("c-old-block", secondsBefore(60), true)] },
  ];
  const stack = model.needsYouStack(projects);
  assert.deepEqual(
    stack.map((card: { question: { id: string } }) => card.question.id),
    ["c-old-block", "b-new-block", "b-old-info", "a-new-info"],
  );
  assert.deepEqual(stack[1], { projectKey: "b", projectName: "beta", question: projects[1].questions[0] });
});

test("relative times read in the largest whole unit", () => {
  assert.equal(model.relativeTime(secondsBefore(2), NOW), "just now");
  assert.equal(model.relativeTime(secondsBefore(-30), NOW), "just now", "a time ahead of now is not negative");
  assert.equal(model.relativeTime(secondsBefore(20), NOW), "20 s ago");
  assert.equal(model.relativeTime(secondsBefore(5 * 60 + 59), NOW), "5 min ago");
  assert.equal(model.relativeTime(secondsBefore(3 * 3600), NOW), "3 h ago");
  assert.equal(model.relativeTime(secondsBefore(50 * 3600), NOW), "2 d ago");
});

test("the Mac is fresh up to 3 min since its last pull, stale after, and never seen without one", () => {
  assert.deepEqual(model.macSeen({ last_pull_at: secondsBefore(20) }, NOW), { label: "20 s ago", stale: false });
  assert.deepEqual(model.macSeen({ last_pull_at: secondsBefore(180) }, NOW), { label: "3 min ago", stale: false });
  assert.deepEqual(model.macSeen({ last_pull_at: secondsBefore(181) }, NOW), { label: "3 min ago", stale: true });
  assert.deepEqual(model.macSeen({ last_pull_at: null }, NOW), { label: "never", stale: true });
  assert.deepEqual(model.macSeen(null, NOW), { label: "never", stale: true });
});

test("the undo countdown runs on the server's clock, not the device's", () => {
  const serverTime = "2026-10-20T12:00:00Z";
  const deviceNow = NOW - 120_000; // the phone's clock is two minutes slow
  const offset = model.serverOffsetMs(serverTime, deviceNow);
  assert.equal(offset, 120_000);
  const applyAfter = "2026-10-20T12:00:10.250Z";
  assert.equal(model.undoSecondsLeft(applyAfter, deviceNow, offset), 11);
  assert.equal(model.undoSecondsLeft(applyAfter, deviceNow + 1_000, offset), 10);
  assert.equal(model.undoSecondsLeft(applyAfter, deviceNow + 10_250, offset), 0);
  assert.equal(model.undoSecondsLeft(applyAfter, deviceNow + 60_000, offset), 0, "never negative");
  assert.equal(model.undoSecondsLeft(applyAfter, deviceNow, 0), 131, "the device clock alone would be wrong");
  assert.equal(model.countdownText(9), "Sending in 9 s");
});

test("each answer status has the page's wording", () => {
  assert.equal(model.writeStatusLabel("sent", null), "Delivered to the Mac");
  assert.equal(model.writeStatusLabel("applied", null), "The conductor has it");
  assert.equal(model.writeStatusLabel("superseded", null), "Already answered on the Mac");
  assert.equal(
    model.writeStatusLabel("superseded", "acked in the terminal"),
    "Already answered on the Mac: acked in the terminal",
  );
  assert.equal(
    model.writeStatusLabel("rejected", "project not found on this machine"),
    "project not found on this machine",
  );
  assert.equal(model.writeStatusLabel("rejected", null), "Rejected");
  assert.equal(model.writeStatusLabel("undo_window", null), "Waiting for the Mac to collect it");
  assert.equal(model.pendingActionLabel("pause"), "Pausing at the next step");
  assert.equal(model.pendingActionLabel("resume"), "Resuming at the next step");
});

test("hidden projects are split from shown ones, keeping the server's order", () => {
  const projects = [
    { key: "a", hidden: false },
    { key: "b", hidden: true },
    { key: "c", hidden: false },
  ];
  const { shown, hidden } = model.partitionByVisibility(projects);
  assert.deepEqual(
    shown.map((p: { key: string }) => p.key),
    ["a", "c"],
  );
  assert.deepEqual(
    hidden.map((p: { key: string }) => p.key),
    ["b"],
  );
  assert.deepEqual(model.partitionByVisibility(projects.map((p) => ({ ...p, hidden: false }))).hidden, []);
});

test("progress reads as done of total, or nothing without progress", () => {
  assert.equal(model.progressText({ done: 3, total: 12 }), "3 of 12");
  assert.equal(model.progressText(null), null);
});

test("a body past six lines or the character cap is clamped to a preview", () => {
  const short = "one\ntwo\nthree";
  assert.deepEqual(model.clampBody(short), { preview: short, clamped: false });
  const sixLines = ["1", "2", "3", "4", "5", "6"].join("\n");
  assert.deepEqual(model.clampBody(sixLines), { preview: sixLines, clamped: false });
  const eightLines = `${sixLines}\n7\n8`;
  assert.deepEqual(model.clampBody(eightLines), { preview: `${sixLines}…`, clamped: true });
  const paragraph = "word ".repeat(200);
  const clamped = model.clampBody(paragraph);
  assert.equal(clamped.clamped, true);
  assert.ok(clamped.preview.length <= model.BODY_MAX_CHARS + 1);
  assert.ok(clamped.preview.endsWith("…"));
});

test("budget chips show each vendor's level and 5 h use, or unknown", () => {
  const budget = {
    anthropic: { level: "amber", five_hour_pct: 72.5, weekly_pct: 40, resets_at: null },
    openai: { level: "green", five_hour_pct: null, weekly_pct: null, resets_at: null },
  };
  assert.deepEqual(
    model.budgetChips(budget).map((chip: { text: string }) => chip.text),
    ["anthropic amber 73%", "openai green"],
  );
  assert.deepEqual(
    model.budgetChips(null).map((chip: { level: string }) => chip.level),
    ["unknown", "unknown"],
  );
});

test("OpenAI credits get a chip of their own; the window chip keeps its level", () => {
  const budget = {
    anthropic: null,
    openai: {
      level: "red",
      five_hour_pct: 88,
      weekly_pct: 100,
      resets_at: null,
      credits: { balance: "215.68", has_credits: true, unlimited: false, plan_limit_reached: true, banked_resets: 3 },
    },
  };
  assert.deepEqual(
    model.budgetChips(budget).map((chip: { level: string; text: string }) => [chip.level, chip.text]),
    [
      ["unknown", "anthropic unknown"],
      ["red", "openai red 88%"],
      ["credits", "openai credits 215.68 left; plan limit reached; 3 banked resets (window resets, not money)"],
    ],
  );
});

test("Resume is offered for a paused project and Pause otherwise", () => {
  assert.equal(model.controlActionFor("paused"), "resume");
  for (const state of ["working", "needs_you", "failed", "done", "idle"])
    assert.equal(model.controlActionFor(state), "pause");
});

test("CI lines name the run and its outcome", () => {
  const run = { repo: "example/app", workflow: "ci", status: "completed", conclusion: "failure", url: null, at: "" };
  assert.equal(model.ciLine(run), "example/app ci: failure");
  assert.equal(model.ciLine({ ...run, status: "in_progress", conclusion: null }), "example/app ci: in progress");
  assert.equal(model.ciLine({ ...run, workflow: "" }), "example/app: failure");
});

test("only a known state has a state class", () => {
  for (const state of ["needs_you", "working", "paused", "failed", "done", "idle"])
    assert.equal(model.stateClass(state), `state-${state}`);
  for (const state of ["rebooting", "", "toString", "__proto__"]) assert.equal(model.stateClass(state), "", state);
});

test("a CI run's tone is red only for a real failure, and neutral for anything without an outcome", () => {
  const tone = (status: string, conclusion: string | null) =>
    model.ciTone({ repo: "example/app", workflow: "ci", status, conclusion, url: null, at: "" });
  assert.equal(tone("completed", "success"), "ok");
  for (const conclusion of ["failure", "timed_out", "startup_failure"])
    assert.equal(tone("completed", conclusion), "fail");
  for (const status of ["in_progress", "queued", "waiting", "pending", "requested"])
    assert.equal(tone(status, null), "running");
  for (const conclusion of ["cancelled", "skipped", "neutral", "action_required", "stale", null])
    assert.equal(tone("completed", conclusion), "neutral", String(conclusion));
  assert.equal(tone("expected", null), "neutral", "a status the page does not know");
});

test("failures carry the server's message; only a login redirect, a 401 or an expired token asks for a sign-in", () => {
  const failure = (status: number, error: object | null, opaqueRedirect = false) =>
    model.failureMessage({ status, opaqueRedirect, error });
  const forbidden = (reason?: string) => failure(403, { error: "forbidden", message: "forbidden", reason });
  assert.equal(failure(0, null, true), "Signed out. Reload to sign in");
  assert.equal(failure(401, null), "Signed out. Reload to sign in");
  assert.equal(forbidden("jwt_invalid:ERR_JWT_EXPIRED"), "Signed out. Reload to sign in");
  assert.equal(failure(0, null), "Cannot reach the server");
  assert.equal(failure(502, null), "Request failed (502)");
  assert.equal(failure(404, { error: "not_found", message: "" }), "Request failed (404)");
  assert.equal(model.undoFailureMessage(409, { error: "too_late", message: "too late" }), "Already sent");
  assert.equal(model.undoFailureMessage(409, { error: "conflict", message: "busy" }), "busy");
});

test("a 403 says what is wrong by its reason code, never asking for a sign-in that cannot help", () => {
  const forbidden = (reason?: string) =>
    model.failureMessage({ status: 403, opaqueRedirect: false, error: { error: "forbidden", message: "forbidden", reason } });
  assert.equal(forbidden("browser_write_foreign_origin"), "This address can't send answers. Open the address in the banner");
  assert.equal(forbidden("browser_email_mismatch"), "Signed in as an address that isn't an owner");
  assert.equal(forbidden("jwt_missing"), "This address is not behind Cloudflare Access");
  assert.equal(
    forbidden("jwt_invalid:ERR_JWT_CLAIM_VALIDATION_FAILED"),
    "Access sign-in doesn't match ACCESS_AUD or ACCESS_TEAM_DOMAIN",
  );
  assert.equal(forbidden("browser_not_app_token"), "Not allowed (browser_not_app_token)");
  assert.equal(forbidden("constructor"), "Not allowed (constructor)");
  assert.equal(forbidden(undefined), "Not allowed", "a 403 without a reason, such as a machine route's");
  for (const reason of ["browser_write_foreign_origin", "browser_email_mismatch", "jwt_missing", "constructor", undefined]) {
    assert.doesNotMatch(forbidden(reason), /sign in/i, reason);
  }
});

// ---- writes and summaries -----------------------------------------------------------------------

const SERVER_TIME = "2026-10-20T12:00:00Z";
const keys = () => {
  let n = 0;
  return () => `key-${++n}`;
};

function summaryWith(projectQuestions: object[], pendingAction: object | null = null) {
  return {
    v: 1,
    server_time: SERVER_TIME,
    projects: [
      { key: "alpha", name: "alpha", hidden: false, pending_action: pendingAction, questions: projectQuestions },
    ],
  };
}

const ok = (data: unknown) => ({ ok: true, status: 201, data });
const networkDown = { ok: false, status: 0, opaqueRedirect: false, error: null, retryable: true };

test("a stored answer shows Undo at once, and a failed summary afterwards keeps it with its deadline", () => {
  const deviceNow = NOW - 120_000; // the phone's clock is two minutes slow
  let view = model.applySummaryResult(
    { summary: null, offsetMs: 0, banner: null },
    ok(summaryWith([question("q1", secondsBefore(60), true)])),
    deviceNow,
  );
  const sending = model.writeToSend(undefined, { kind: "accept" }, keys()).write;
  const stored = {
    id: "00000000-0000-4000-8000-000000000001",
    kind: "accept",
    text: null,
    status: "undo_window",
    apply_after: "2026-10-20T12:00:10Z",
    detail: null,
  };
  const settled = model.settleWrite(sending, ok({ v: 1, answer: stored }));
  assert.deepEqual(settled, { write: null, row: stored });

  view = { ...view, summary: model.withAnswer(view.summary, "q1", settled.row) };
  view = model.applySummaryResult(view, networkDown, deviceNow + 1_000);

  assert.equal(view.banner, "Cannot reach the server");
  const shown = model.needsYouStack(view.summary.projects)[0].question.answer;
  assert.deepEqual(shown, stored);
  assert.equal(model.canUndo(shown, deviceNow + 1_000, view.offsetMs), true);
  assert.equal(model.undoSecondsLeft(shown.apply_after, deviceNow + 1_000, view.offsetMs), 9);
  assert.equal(model.canUndo(shown, deviceNow + 10_000, view.offsetMs), false, "closed on the server's clock");
});

test("a stored pause is pending on its project while its window is open", () => {
  const summary = summaryWith([]);
  const action = { id: "a1", kind: "pause", apply_after: SERVER_TIME, status: "undo_window", detail: null };
  assert.deepEqual(model.withPendingAction(summary, "alpha", action).projects[0].pending_action, {
    id: "a1",
    kind: "pause",
    apply_after: SERVER_TIME,
  });
  assert.equal(model.withPendingAction(summary, "alpha", { ...action, status: "sent" }), summary);
});

test("an unconfirmed reply can only be retried as it was sent, with the same key", () => {
  const sent = model.writeToSend(undefined, { kind: "typed", text: "ship it" }, keys()).write;
  const { write: unconfirmed } = model.settleWrite(sent, networkDown);
  assert.deepEqual(unconfirmed, { clientKey: "key-1", body: { kind: "typed", text: "ship it" }, phase: "unconfirmed" });

  assert.deepEqual(model.writeToSend(unconfirmed, { kind: "typed", text: "ship it today" }, keys()), {
    refused: "Retry the unconfirmed one first",
  });
  assert.deepEqual(model.writeToSend(unconfirmed, { kind: "accept" }, keys()), {
    refused: "Retry the unconfirmed one first",
  });
  assert.deepEqual(model.writeToSend(unconfirmed, { kind: "typed", text: "ship it" }, keys()), {
    write: { ...unconfirmed, phase: "sending" },
  });
  assert.equal(model.unconfirmedText(unconfirmed.body), "Your reply may not have reached the server");
});

test("an unconfirmed Accept cannot turn into a reply, and a pause cannot turn into a resume", () => {
  const accept = { clientKey: "key-1", body: { kind: "accept" }, phase: "unconfirmed" };
  assert.ok("refused" in model.writeToSend(accept, { kind: "typed", text: "no" }, keys()));
  assert.equal(model.writeToSend(accept, { kind: "accept" }, keys()).write.clientKey, "key-1");
  const pause = { clientKey: "key-2", body: { kind: "pause" }, phase: "unconfirmed" };
  assert.ok("refused" in model.writeToSend(pause, { kind: "resume" }, keys()));
});

test("a write still sending refuses a second one", () => {
  const sending = model.writeToSend(undefined, { kind: "accept" }, keys()).write;
  assert.deepEqual(model.writeToSend(sending, { kind: "accept" }, keys()), { refused: "Still sending" });
});

// The page applies a summary the way app.js does: Object.assign over its own state.
function pageAfterSummary(writes: Map<string, object>, result: object) {
  const page = { summary: null, offsetMs: 0, banner: "Cannot reach the server", writes };
  return Object.assign(page, model.applySummaryResult(page, result, NOW));
}

test("a summary without the row keeps the unconfirmed write, so the next tap replays its key", () => {
  const sent = model.writeToSend(undefined, { kind: "accept" }, keys()).write;
  const { write: unconfirmed } = model.settleWrite(sent, networkDown);
  const page = pageAfterSummary(
    new Map([["answer:q1", unconfirmed]]),
    ok(summaryWith([question("q1", secondsBefore(60), true)])),
  );
  assert.equal(page.banner, null);
  assert.equal(page.writes.get("answer:q1"), unconfirmed);
  assert.ok("refused" in model.writeToSend(unconfirmed, { kind: "typed", text: "no" }, keys()));
  assert.equal(model.writeToSend(unconfirmed, { kind: "accept" }, () => "key-2").write.clientKey, "key-1");
});

test("a summary that already shows an answer still leaves the write to its retry", () => {
  const unconfirmed = { clientKey: "key-1", body: { kind: "accept" }, phase: "unconfirmed" };
  const answered = { ...question("q1", secondsBefore(60), true), answer: { id: "someone", status: "sent" } };
  const page = pageAfterSummary(new Map([["answer:q1", unconfirmed]]), ok(summaryWith([answered])));
  assert.equal(page.writes.get("answer:q1"), unconfirmed, "the summary cannot say which key that answer was");
});

test("a delayed POST: the retry with the same key returns the stored row, or a 409 that settles it", () => {
  const unconfirmed = { clientKey: "key-1", body: { kind: "accept" }, phase: "unconfirmed" };
  const retry = model.writeToSend(unconfirmed, { kind: "accept" }, () => "key-2").write;
  assert.equal(retry.clientKey, "key-1");
  const landed = {
    id: "a1",
    kind: "accept",
    text: null,
    status: "undo_window",
    apply_after: SERVER_TIME,
    detail: null,
  };
  assert.deepEqual(model.settleWrite(retry, { ...ok({ v: 1, answer: landed }), status: 200 }), {
    write: null,
    row: landed,
  });
  const lost = { ok: false, status: 409, opaqueRedirect: false, error: { error: "conflict" }, retryable: false };
  assert.deepEqual(model.settleWrite(retry, lost), { write: null, row: null });
  assert.equal(model.settleWrite(retry, networkDown).write.clientKey, "key-1", "still unconfirmed");
});

test("a failed summary keeps unconfirmed writes for an exact retry", () => {
  const unconfirmed = { clientKey: "key-1", body: { kind: "accept" }, phase: "unconfirmed" };
  const page = pageAfterSummary(new Map([["answer:q1", unconfirmed]]), networkDown);
  assert.equal(page.writes.get("answer:q1"), unconfirmed);
});

test("write responses: a 4xx settles, a 5xx or an unreadable success leaves the write unconfirmed", () => {
  const sending = { clientKey: "key-1", body: { kind: "accept" }, phase: "sending" };
  const conflict = { ok: false, status: 409, opaqueRedirect: false, error: null, retryable: false };
  assert.deepEqual(model.settleWrite(sending, conflict), { write: null, row: null });
  const unavailable = { ...conflict, status: 503, retryable: true };
  assert.equal(model.settleWrite(sending, unavailable).write.phase, "unconfirmed");
  assert.equal(model.settleWrite(sending, ok(null)).write.phase, "unconfirmed", "truncated body");
  assert.equal(model.settleWrite(sending, ok({ v: 1, answer: { id: "x" } })).write.phase, "unconfirmed");
});

test("an unreadable first summary shows the banner and leaves the page empty", () => {
  const empty = { summary: null, offsetMs: 0, banner: null };
  for (const data of [null, { v: 1 }, { v: 1, server_time: "soon", projects: [] }, "<html>"]) {
    const view = model.applySummaryResult(empty, ok(data), NOW);
    assert.deepEqual(view, { ...empty, banner: model.UNREADABLE_SUMMARY }, JSON.stringify(data));
  }
});

test("an unreadable later summary keeps the last good one and its clock offset", () => {
  const good = summaryWith([question("q1", secondsBefore(60), true)]);
  const before = model.applySummaryResult({ summary: null, offsetMs: 0, banner: null }, ok(good), NOW - 5_000);
  assert.equal(before.offsetMs, 5_000);
  for (const data of [null, { v: 1, server_time: SERVER_TIME, projects: [{ key: "alpha" }] }]) {
    const after = model.applySummaryResult(before, ok(data), NOW);
    assert.equal(after.summary, good);
    assert.equal(after.offsetMs, 5_000);
    assert.equal(after.banner, model.UNREADABLE_SUMMARY);
  }
});
