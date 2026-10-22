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

// Reset times are local clock times: every case pins the locale and the zone, never the machine's.
const UTC_GB = { locale: "en-GB", timeZone: "UTC" };
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const inMs = (ms: number) => new Date(NOW + ms).toISOString();
const IN_2H10 = inMs(2 * HOUR + 10 * MINUTE); // 14:10 UTC, NOW being Tue 20 Oct 12:00 UTC
const MONDAY_0804 = "2026-10-26T08:04:00Z";

test("a reset reads as relative within a day and as a local weekday and 24-hour time after it", () => {
  const cases: [string, string][] = [
    [inMs(-1000), "due"],
    [inMs(0), "due"],
    [inMs(30_000), "in < 1 min"],
    [inMs(MINUTE), "in 1 min"],
    [inMs(MINUTE + 1000), "in 2 min"],
    [inMs(41 * MINUTE + 10_000), "in 42 min"],
    [inMs(HOUR), "in 1 h"],
    [inMs(2 * HOUR), "in 2 h"],
    [IN_2H10, "in 2 h 10 min"],
    [inMs(24 * HOUR - 1000), "in 24 h"],
    [inMs(24 * HOUR), "Wed 12:00"],
    [MONDAY_0804, "Mon 08:04"],
    [inMs(6 * 24 * HOUR), "Mon 26 Oct 12:00"],
    ["2026-11-02T08:04:00Z", "Mon 2 Nov 08:04"],
  ];
  for (const [resetsAt, phrase] of cases) assert.equal(model.resetPhrase(resetsAt, NOW, UTC_GB), phrase, resetsAt);
});

test("a reset's clock time is the viewer's zone, 24-hour even where the locale's own clock is 12-hour", () => {
  assert.equal(model.resetPhrase(MONDAY_0804, NOW, { locale: "en-US", timeZone: "America/Los_Angeles" }), "Mon 01:04");
  assert.equal(model.resetPhrase("2026-10-21T23:30:00Z", NOW, { locale: "en-GB", timeZone: "Asia/Tokyo" }), "Thu 08:30");
});

type Chip = { vendor: string; level: string; text: string; detail: string[] };
const chipsOf = (budget: unknown): Chip[] => model.budgetChips(budget, NOW, UTC_GB);

// The relative part counts real elapsed time and the clock part is the wall clock at the reset, so on
// the day the clocks go back the two disagree by an hour, and the repeated hour reads as itself.
test("on a day the clocks change, a reset's relative time is elapsed time and its clock is the local one", () => {
  const londonDetail = (resetsAt: string) =>
    model.budgetChips(
      { anthropic: { level: "green", five_hour_pct: 10, weekly_pct: null, resets_at: resetsAt, window: "five_hour" }, openai: null },
      Date.parse("2026-10-24T23:00:00Z"), // Sun 25 Oct 00:00 BST; at 01:00 UTC 02:00 BST becomes 01:00 GMT
      { locale: "en-GB", timeZone: "Europe/London" },
    )[0].detail[0];
  assert.equal(londonDetail("2026-10-25T00:30:00Z"), "5-hour 10% · resets 01:30 (in 1 h 30 min)");
  assert.equal(londonDetail("2026-10-25T01:30:00Z"), "5-hour 10% · resets 01:30 (in 2 h 30 min)");
  assert.equal(londonDetail("2026-10-25T03:00:00Z"), "5-hour 10% · resets 03:00 (in 4 h)");

  const losAngeles = { locale: "en-US", timeZone: "America/Los_Angeles" };
  const midnightPdt = Date.parse("2026-11-01T07:00:00Z"); // Sun 1 Nov 00:00 PDT; at 09:00 UTC 02:00 PDT becomes 01:00 PST
  assert.equal(model.resetPhrase("2026-11-01T10:30:00Z", midnightPdt, losAngeles), "in 3 h 30 min");
  assert.equal(model.resetPhrase("2026-11-02T16:00:00Z", Date.parse("2026-10-28T12:00:00Z"), losAngeles), "Mon 08:00");
});

// The schema checks an ISO time's shape only, so a time that is not a date can reach the page.
test("a reset time that is not a date, or no clock to measure it by, says nothing rather than fail", () => {
  for (const resetsAt of ["garbage", "", "2026-13-45T99:99:99Z"]) {
    assert.equal(model.resetPhrase(resetsAt, NOW, UTC_GB), null, resetsAt);
    const [anthropic] = model.budgetChips(
      { anthropic: { level: "amber", five_hour_pct: 80, weekly_pct: 41, resets_at: resetsAt, weekly_resets_at: resetsAt, window: "five_hour" }, openai: null },
      NOW,
      UTC_GB,
    );
    assert.equal(anthropic.text, "Anthropic 80%", resetsAt);
    assert.deepEqual(anthropic.detail, ["5-hour 80%", "Weekly 41%"], resetsAt);
  }
  assert.equal(model.resetPhrase(IN_2H10, Number.NaN, UTC_GB), null);
});

test("with no budget, or a vendor the Mac could not read, a chip says there is no reading", () => {
  const unread = { level: "unknown", five_hour_pct: null, weekly_pct: null, resets_at: null, window: "five_hour" };
  for (const budget of [null, { anthropic: null, openai: null }, { anthropic: unread, openai: unread }]) {
    assert.deepEqual(chipsOf(budget), [
      { vendor: "anthropic", level: "unknown", text: "Anthropic · no reading", detail: ["No recent budget reading from the Mac"] },
      { vendor: "openai", level: "unknown", text: "OpenAI · no reading", detail: ["No recent budget reading from the Mac"] },
    ]);
  }
});

test("a chip reads the 5-hour window when it drives the level, and opens to both windows", () => {
  const [anthropic] = chipsOf({
    anthropic: { level: "green", five_hour_pct: 28, weekly_pct: 41, resets_at: IN_2H10, weekly_resets_at: MONDAY_0804, window: "five_hour" },
    openai: null,
  });
  assert.deepEqual(anthropic, {
    vendor: "anthropic",
    level: "green",
    text: "Anthropic 28% · resets in 2 h 10 min",
    detail: ["5-hour 28% · resets 14:10 (in 2 h 10 min)", "Weekly 41% · resets Mon 08:04"],
  });
});

test("a Mac that sends no window or weekly reset is read as the 5-hour window", () => {
  const [anthropic] = chipsOf({
    anthropic: { level: "red", five_hour_pct: 72.5, weekly_pct: 100, resets_at: IN_2H10 },
    openai: null,
  });
  assert.equal(anthropic.text, "Anthropic 73% · resets in 2 h 10 min");
  assert.deepEqual(anthropic.detail, ["5-hour 73% · resets 14:10 (in 2 h 10 min)", "Weekly 100%"]);
});

test("a chip reads the weekly window when the Mac says it drives the level (weekly red, or both red)", () => {
  for (const fiveHourPct of [64, 95]) {
    const [, openai] = chipsOf({
      anthropic: null,
      openai: { level: "red", five_hour_pct: fiveHourPct, weekly_pct: 100, resets_at: IN_2H10, weekly_resets_at: MONDAY_0804, window: "weekly" },
    });
    assert.equal(openai.level, "red");
    assert.equal(openai.text, "OpenAI weekly 100% · resets Mon 08:04");
    assert.deepEqual(openai.detail, [`5-hour ${fiveHourPct}% · resets 14:10 (in 2 h 10 min)`, "Weekly 100% · resets Mon 08:04"]);
  }
});

test("a reset already past reads as due, and a window without a reset or percentage says only what it has", () => {
  const [anthropic, openai] = chipsOf({
    anthropic: { level: "red", five_hour_pct: 90, weekly_pct: null, resets_at: inMs(-5 * MINUTE), window: "five_hour" },
    openai: { level: "amber", five_hour_pct: null, weekly_pct: 70, resets_at: null, window: "five_hour" },
  });
  assert.equal(anthropic.text, "Anthropic 90% · resets due");
  assert.deepEqual(anthropic.detail, ["5-hour 90% · resets due", "Weekly"]);
  assert.equal(openai.text, "OpenAI");
  assert.deepEqual(openai.detail, ["5-hour", "Weekly 70%"]);
});

test("OpenAI credits show on its chip as a balance or unlimited, and in full when it opens", () => {
  const credits = (fields: object) => ({
    balance: "195.62",
    has_credits: true,
    unlimited: false,
    plan_limit_reached: true,
    banked_resets: 2,
    ...fields,
  });
  const openaiWith = (extra: object) =>
    chipsOf({
      anthropic: null,
      openai: { level: "red", five_hour_pct: 64, weekly_pct: 100, resets_at: null, weekly_resets_at: MONDAY_0804, window: "weekly", ...extra },
    })[1];
  const present = openaiWith({ credits: credits({}) });
  assert.equal(present.text, "OpenAI weekly 100% · resets Mon 08:04 · credits 195.62");
  assert.equal(present.detail.at(-1), "credits 195.62 left; plan limit reached; 2 banked resets (window resets, not money)");
  assert.equal(openaiWith({ credits: credits({ unlimited: true, balance: null }) }).text, "OpenAI weekly 100% · resets Mon 08:04 · credits unlimited");
  const spent = openaiWith({ credits: credits({ has_credits: false, balance: null, banked_resets: 0 }) });
  assert.equal(spent.text, "OpenAI weekly 100% · resets Mon 08:04");
  assert.equal(spent.detail.at(-1), "no credits; plan limit reached");
  const absent = openaiWith({});
  assert.equal(absent.text, "OpenAI weekly 100% · resets Mon 08:04");
  assert.deepEqual(absent.detail, ["5-hour 64%", "Weekly 100% · resets Mon 08:04"]);
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

test("only a question that says ask: action is an action; one without ask is a decision", () => {
  assert.equal(model.isAction({ ask: "action" }), true);
  assert.equal(model.isAction({ ask: "decision" }), false);
  assert.equal(model.isAction({}), false);
});

test("\"I've done it\" answers with typed text that marks the claim unverified", () => {
  assert.deepEqual(model.OWNER_REPORTS_DONE, { kind: "typed", text: "Owner reports done (unverified)." });
  assert.ok(Object.isFrozen(model.OWNER_REPORTS_DONE));
});
