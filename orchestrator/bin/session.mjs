#!/usr/bin/env node
// Conductor session heartbeat in .orchestrator/sessions/<door>.json (see lib/session.mjs).
// session.mjs beat --door opencode|claude [--state working|waiting|done|ended] [--now TEXT] [--slice ID]
//                  [--progress N/M] [--session ID] [--hook] [--cwd DIR]
// session.mjs show [--cwd DIR]
// beat keeps every field not given and clears --now, --slice or --session given as "" (--progress
// must always be N/M). --hook is Claude Code hook mode: the
// hook's JSON on stdin names the session, the project (its cwd, unless --cwd) and, through its event, the
// state. Hook mode never exits 2, which Claude Code reads as "block", and never creates .orchestrator/.
import fs from "node:fs";
import path from "node:path";
import { isProject, requireProject } from "../lib/project.mjs";
import { DOORS, STATES, HOOK_EVENT_STATES, SessionError, parseHookInput, parseProgress, readSessions, writeBeat } from "../lib/session.mjs";

const OPTIONS = {
  beat: { values: ["door", "state", "now", "slice", "progress", "session", "cwd"], flags: ["hook"] },
  show: { values: ["cwd"], flags: [] },
};

// A bad command line: exit 2, or 1 in hook mode.
class UsageError extends Error {}

// Every token must be a known option; a value option takes the next token, which may not start with "--".
function parseOptions(args, { values, flags }) {
  const opts = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i].startsWith("--") ? args[i].slice(2) : null;
    if (flags.includes(key)) { opts[key] = true; continue; }
    if (!values.includes(key)) throw new UsageError(`unknown argument ${args[i]}`);
    const value = args[i + 1];
    if (value === undefined || value.startsWith("--")) throw new UsageError(`--${key} needs a value`);
    opts[key] = value;
    i++;
  }
  return opts;
}

// A text option given as "" clears its field (null); one not given keeps it (undefined).
const clearable = (value) => (value === "" ? null : value);

function beatFields(opts) {
  if (!DOORS.includes(opts.door)) throw new UsageError("--door opencode|claude required");
  if (opts.state !== undefined && !STATES.includes(opts.state)) throw new UsageError(`--state must be ${STATES.join("|")}`);
  const progress = opts.progress === undefined ? undefined : parseProgress(opts.progress);
  if (progress === null) throw new UsageError("--progress must be N/M");
  return { state: opts.state ?? "working", sessionId: clearable(opts.session), now: clearable(opts.now), slice: clearable(opts.slice), progress };
}

function beat(opts) {
  const fields = beatFields(opts);
  const root = path.resolve(opts.cwd || process.cwd());
  reportReplaced(writeBeat(root, opts.door, fields), opts.door);
}

// The hook's event sets the state, so --state is refused rather than silently overridden.
function hookBeat(opts) {
  if (opts.state !== undefined) throw new UsageError("--state cannot be combined with --hook");
  const fields = beatFields(opts);
  const hook = parseHookInput(readHookInput());
  const state = HOOK_EVENT_STATES.get(hook.event);
  if (!state) return;
  const dir = opts.cwd || hook.cwd;
  if (!dir) throw new SessionError("hook input has no cwd");
  // Checked before any filesystem call, whose own NUL error differs between Node and Go.
  if (dir.includes("\0")) throw new SessionError("hook input cwd contains a NUL character");
  const root = path.resolve(dir);
  requireHookProject(root);
  reportReplaced(writeBeat(root, opts.door, { ...fields, state, sessionId: hook.sessionId ?? fields.sessionId }), opts.door);
}

function readHookInput() {
  try { return fs.readFileSync(0, "utf8"); } catch (err) {
    throw new SessionError(`could not read the hook input: ${err.code}`);
  }
}

// A chat in a directory that is not a project must not create project state.
function requireHookProject(root) {
  let project;
  try { project = isProject(root); } catch (err) {
    throw new SessionError(`could not check ${root}/.orchestrator: ${err.code}`);
  }
  if (!project) throw new SessionError(`${root} has no .orchestrator/ directory`);
}

function reportReplaced({ replacedUnreadable }, door) {
  if (replacedUnreadable) console.error(`session beat: replaced .orchestrator/sessions/${door}.json, which was not a JSON object`);
}

function show(opts) {
  const root = path.resolve(opts.cwd || process.cwd());
  requireProject(root, "--cwd");
  const { sessions, skipped } = readSessions(root);
  for (const reason of skipped) console.error(`session show: skipped ${reason}`);
  console.log(JSON.stringify(sessions, null, 2));
}

const [cmd, ...rest] = process.argv.slice(2);
if (!Object.hasOwn(OPTIONS, cmd ?? "")) { console.error("commands: beat | show"); process.exit(2); }
const hookMode = cmd === "beat" && rest.includes("--hook");
try {
  const opts = parseOptions(rest, OPTIONS[cmd]);
  if (cmd === "show") show(opts);
  else if (hookMode) hookBeat(opts);
  else beat(opts);
} catch (err) {
  if (!(err instanceof UsageError || err instanceof SessionError)) throw err;
  console.error(`session ${cmd}: ${err.message}`);
  process.exit(err instanceof UsageError && !hookMode ? 2 : 1);
}
