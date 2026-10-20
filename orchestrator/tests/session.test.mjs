import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const sessionCli = fileURLToPath(new URL("../bin/session.mjs", import.meta.url));
const isoMillis = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;

function tempDir(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "session-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function tempProject(t) {
  const project = tempDir(t);
  fs.mkdirSync(path.join(project, ".orchestrator"));
  return project;
}

function session(cwd, args, input = "") {
  return spawnSync(process.execPath, [sessionCli, ...args], { cwd, input, encoding: "utf8" });
}

function record(project, door = "claude") {
  return JSON.parse(fs.readFileSync(path.join(project, ".orchestrator", "sessions", `${door}.json`), "utf8"));
}

function hookInput(project, event, extra = {}) {
  return JSON.stringify({ session_id: "s1", cwd: project, hook_event_name: event, ...extra });
}

// Every file under dir, as one string, to search for text that must never be stored.
function allFileText(dir) {
  return fs.readdirSync(dir, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => fs.readFileSync(path.join(entry.parentPath, entry.name), "utf8"))
    .join("\n");
}

test("beat writes the record with its keys in order, owner-only, and creates .orchestrator/", { skip: process.platform === "win32" }, (t) => {
  const project = tempDir(t);
  const run = session(project, ["beat", "--door", "claude", "--now", "reviewing", "--slice", "wt3", "--progress", "1/3", "--session", "abc"]);
  assert.equal(run.status, 0, run.stderr);
  assert.equal(run.stdout, "");
  const written = record(project);
  assert.deepEqual(Object.keys(written), ["door", "session_id", "state", "now", "slice", "progress", "updated_at"]);
  assert.deepEqual({ ...written, updated_at: "" }, { door: "claude", session_id: "abc", state: "working", now: "reviewing", slice: "wt3", progress: { done: 1, total: 3 }, updated_at: "" });
  assert.match(written.updated_at, isoMillis);
  const dir = path.join(project, ".orchestrator", "sessions");
  assert.equal(fs.statSync(dir).mode & 0o777, 0o700);
  assert.equal(fs.statSync(path.join(dir, "claude.json")).mode & 0o777, 0o600);
  assert.deepEqual(fs.readdirSync(dir), ["claude.json"], "no temp file is left behind");
});

test("beat keeps every field not given, clears a text field given as empty and defaults the state to working", (t) => {
  const project = tempProject(t);
  assert.equal(session(project, ["beat", "--door", "opencode", "--state", "done", "--now", "x", "--slice", "s", "--progress", "2/2", "--session", "id"]).status, 0);
  const first = record(project, "opencode");
  assert.equal(session(project, ["beat", "--door", "opencode", "--now", "", "--session", ""]).status, 0);
  const second = record(project, "opencode");
  assert.deepEqual({ ...second, updated_at: "" }, { door: "opencode", session_id: null, state: "working", now: null, slice: "s", progress: { done: 2, total: 2 }, updated_at: "" });
  assert.ok(second.updated_at >= first.updated_at);
});

test("beat cuts --now to 200 and --slice to 40 UTF-16 units, a split surrogate pair becoming U+FFFD", (t) => {
  const project = tempProject(t);
  const now = "a".repeat(199) + "😀tail";
  assert.equal(session(project, ["beat", "--door", "claude", "--now", now, "--slice", "s".repeat(50)]).status, 0);
  const written = record(project);
  assert.equal(written.now, "a".repeat(199) + "�");
  assert.equal(written.slice, "s".repeat(40));
});

test("beat replaces a previous record that is not a JSON object and says so", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.join(project, ".orchestrator", "sessions"));
  fs.writeFileSync(path.join(project, ".orchestrator", "sessions", "claude.json"), "[1]");
  const run = session(project, ["beat", "--door", "claude", "--state", "waiting"]);
  assert.equal(run.status, 0);
  assert.equal(run.stderr, "session beat: replaced .orchestrator/sessions/claude.json, which was not a JSON object\n");
  assert.equal(record(project).state, "waiting");
});

test("beat refuses a bad command line with one line and exit 2", (t) => {
  const project = tempProject(t);
  const cases = [
    [["beat"], "session beat: --door opencode|claude required\n"],
    [["beat", "--door", "codex"], "session beat: --door opencode|claude required\n"],
    [["beat", "--door", "claude", "--progress", "1/0"], "session beat: --progress must be N/M\n"],
    [["beat", "--door", "claude", "--progress", "one/2"], "session beat: --progress must be N/M\n"],
    [["beat", "--door", "claude", "--progress", ""], "session beat: --progress must be N/M\n"],
    [["beat", "--door", "claude", "--state", "busy"], "session beat: --state must be working|waiting|done|ended\n"],
    [["beat", "--door", "claude", "--now"], "session beat: --now needs a value\n"],
    [["beat", "--door", "claude", "--colour", "red"], "session beat: unknown argument --colour\n"],
    [["frob"], "commands: beat | show\n"],
    [[], "commands: beat | show\n"],
  ];
  for (const [args, stderr] of cases) {
    const run = session(project, args);
    assert.deepEqual([run.status, run.stderr], [2, stderr], args.join(" "));
  }
  assert.equal(fs.existsSync(path.join(project, ".orchestrator", "sessions")), false);
});

test("hook mode: a Stop event records waiting and the hook's session, and never stores the prompt", (t) => {
  const project = tempProject(t);
  const input = hookInput(project, "Stop", { prompt: "secret prompt", transcript_path: "/secret/transcript.jsonl" });
  const run = session(tempDir(t), ["beat", "--door", "claude", "--hook"], input);
  assert.deepEqual([run.status, run.stdout, run.stderr], [0, "", ""]);
  assert.equal(record(project).state, "waiting");
  assert.equal(record(project).session_id, "s1");
  assert.doesNotMatch(allFileText(project), /secret/);
});

test("hook mode maps each followed event to its state and writes nothing for any other", (t) => {
  const project = tempProject(t);
  for (const [event, state] of [["UserPromptSubmit", "working"], ["Stop", "waiting"], ["SessionStart", "working"], ["SessionEnd", "ended"]]) {
    assert.equal(session(project, ["beat", "--door", "claude", "--hook"], hookInput(project, event)).status, 0);
    assert.equal(record(project).state, state, event);
  }
  fs.rmSync(path.join(project, ".orchestrator", "sessions"), { recursive: true });
  for (const event of ["PreToolUse", "constructor", 5]) {
    const run = session(project, ["beat", "--door", "claude", "--hook"], hookInput(project, event));
    assert.deepEqual([run.status, run.stderr], [0, ""], String(event));
  }
  assert.equal(fs.existsSync(path.join(project, ".orchestrator", "sessions")), false);
});

test("beat tightens an existing sessions directory to owner-only", { skip: process.platform === "win32" }, (t) => {
  const project = tempProject(t);
  const dir = path.join(project, ".orchestrator", "sessions");
  fs.mkdirSync(dir);
  fs.chmodSync(dir, 0o755);
  assert.equal(session(project, ["beat", "--door", "claude"]).status, 0);
  assert.equal(fs.statSync(dir).mode & 0o777, 0o700);
});

test("hook mode refuses a cwd with a NUL character before touching the filesystem", (t) => {
  const project = tempProject(t);
  const run = session(project, ["beat", "--door", "claude", "--hook"], hookInput(`${project}\0x`, "Stop"));
  assert.deepEqual([run.status, run.stderr], [1, "session beat: hook input cwd contains a NUL character\n"]);
  assert.equal(fs.existsSync(path.join(project, ".orchestrator", "sessions")), false);
});

test("hook mode: --cwd overrides the hook's cwd", (t) => {
  const project = tempProject(t);
  const run = session(tempDir(t), ["beat", "--door", "claude", "--hook", "--cwd", project], hookInput("/nonexistent", "Stop"));
  assert.equal(run.status, 0, run.stderr);
  assert.equal(record(project).state, "waiting");
});

test("hook mode fails with exit 1, never 2, and creates nothing outside a project", (t) => {
  const outside = tempDir(t);
  const cases = [
    [hookInput(outside, "Stop"), [], `session beat: ${outside} has no .orchestrator/ directory\n`],
    ["not json", [], "session beat: hook input is not valid JSON\n"],
    ["[1]", [], "session beat: hook input is not a JSON object\n"],
    [JSON.stringify({ hook_event_name: "Stop" }), [], "session beat: hook input has no cwd\n"],
    [hookInput(outside, "Stop"), ["--door", "codex"], "session beat: --door opencode|claude required\n"],
    [hookInput(outside, "Stop"), ["--state", "done"], "session beat: --state cannot be combined with --hook\n"],
  ];
  for (const [input, args, stderr] of cases) {
    const run = session(outside, ["beat", "--door", "claude", "--hook", ...args], input);
    assert.deepEqual([run.status, run.stderr], [1, stderr], `${input} ${args.join(" ")}`);
  }
  assert.deepEqual(fs.readdirSync(outside), []);
});

test("show prints every door's record in byte order, or {}", (t) => {
  const project = tempProject(t);
  assert.deepEqual([session(project, ["show"]).stdout, session(project, ["show"]).status], ["{}\n", 0]);
  session(project, ["beat", "--door", "opencode"]);
  session(project, ["beat", "--door", "claude"]);
  const run = session(tempDir(t), ["show", "--cwd", project]);
  assert.equal(run.status, 0, run.stderr);
  const shown = JSON.parse(run.stdout);
  assert.deepEqual(Object.keys(shown), ["claude", "opencode"]);
  assert.deepEqual(shown.claude, record(project, "claude"));
  assert.equal(run.stdout, JSON.stringify(shown, null, 2) + "\n");
});

test("show skips an unparsable record and refuses to run outside a project", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.join(project, ".orchestrator", "sessions"));
  fs.writeFileSync(path.join(project, ".orchestrator", "sessions", "claude.json"), "{");
  const run = session(project, ["show"]);
  assert.deepEqual([run.status, run.stdout, run.stderr], [0, "{}\n", "session show: skipped .orchestrator/sessions/claude.json: not valid JSON\n"]);
  const outside = tempDir(t);
  const refused = session(outside, ["show"]);
  assert.equal(refused.status, 2);
  assert.match(refused.stderr, /^not an orchestrator project: .* has no \.orchestrator\/ directory; cd into the project or pass --cwd <project dir>\n$/);
  assert.deepEqual(fs.readdirSync(outside), []);
});
