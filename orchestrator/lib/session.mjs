// Conductor session heartbeats: every conductor, interactive or headless, keeps one small record in
// <project>/.orchestrator/sessions/<door>.json, so a dashboard can tell a human chatting with a conductor
// from an idle project. go/internal/session is the Go twin; both write identical bytes.
import fs from "node:fs";
import path from "node:path";
import { replaceFile } from "./replace-file.mjs";

// In byte order, which is the order `show` prints them in.
export const DOORS = ["claude", "opencode"];
export const STATES = ["working", "waiting", "done", "ended"];

// The Claude Code hook events a heartbeat follows; any other event writes nothing. A Map, so an event
// named like an Object.prototype key ("constructor") maps to nothing.
export const HOOK_EVENT_STATES = new Map([
  ["UserPromptSubmit", "working"],
  ["SessionStart", "working"],
  ["Stop", "waiting"],
  ["SessionEnd", "ended"],
]);

// Limits in UTF-16 code units, the unit JS strings count in.
const NOW_MAX_UNITS = 200;
const SLICE_MAX_UNITS = 40;

const PROGRESS_PATTERN = /^(\d+)\/(\d+)$/;

// A failure the command reports as one line (`session <command>: <message>`) and exit 1.
export class SessionError extends Error {}

function sessionsDir(root) {
  return path.join(root, ".orchestrator", "sessions");
}

function relativeSessionFile(door) {
  return `.orchestrator/sessions/${door}.json`;
}

// "N/M" with M >= 1 as {done: N, total: M}, else null.
export function parseProgress(text) {
  const match = PROGRESS_PATTERN.exec(text);
  if (!match || Number(match[2]) < 1) return null;
  return { done: Number(match[1]), total: Number(match[2]) };
}

// Parses the JSON a Claude Code hook writes to stdin. Only session_id, cwd and hook_event_name are read;
// the prompt and transcript_path are never looked at, so they cannot be stored or printed.
export function parseHookInput(text) {
  let input;
  try { input = JSON.parse(text); } catch {
    throw new SessionError("hook input is not valid JSON");
  }
  if (!isPlainObject(input)) throw new SessionError("hook input is not a JSON object");
  return {
    event: input.hook_event_name,
    sessionId: typeof input.session_id === "string" ? input.session_id : undefined,
    cwd: typeof input.cwd === "string" ? input.cwd : "",
  };
}

// Writes the door's heartbeat, merged over the previous one: a field given as undefined keeps its previous
// value, null clears it; state is always given and updated_at always refreshed. Returns whether a previous
// file that was not a JSON object was replaced, so the caller can say so.
export function writeBeat(root, door, { state, sessionId, now, slice, progress }, at = new Date()) {
  const file = path.join(sessionsDir(root), `${door}.json`);
  ensureSessionsDir(root);
  const previous = readPrevious(file, door);
  const kept = keptFields(previous.record);
  const record = {
    door,
    session_id: merge(sessionId, kept.session_id, wellFormed),
    state,
    now: merge(now, kept.now, (text) => cut(text, NOW_MAX_UNITS)),
    slice: merge(slice, kept.slice, (text) => cut(text, SLICE_MAX_UNITS)),
    progress: merge(progress, kept.progress, (given) => given),
    updated_at: at.toISOString(),
  };
  try { replaceFile(file, JSON.stringify(record, null, 2) + "\n"); } catch (err) {
    throw new SessionError(`could not write ${relativeSessionFile(door)}: ${err.code}`);
  }
  return { replacedUnreadable: previous.unreadable };
}

// undefined keeps the previous value, null clears it, anything else is normalised and stored.
function merge(given, kept, normalise) {
  if (given === undefined) return kept;
  return given === null ? null : normalise(given);
}

// The parent is created with default permissions, as every state-creating command creates it; the
// sessions directory itself is owner-only, an existing one included.
function ensureSessionsDir(root) {
  try {
    fs.mkdirSync(path.join(root, ".orchestrator"), { recursive: true });
    fs.mkdirSync(sessionsDir(root), { recursive: true, mode: 0o700 });
  } catch (err) {
    throw new SessionError(`could not create .orchestrator/sessions: ${err.code}`);
  }
  tightenOwnDir(sessionsDir(root));
}

// Chmods dir to 0700 when this user owns it and it grants group or other access, as Go's
// nodefs.MkdirPrivate does. Best-effort by design: the 0600 file modes are the real protection, and a
// directory that cannot be tightened must not stop a heartbeat. A symlink is left alone, since chmod
// would tighten its target.
function tightenOwnDir(dir) {
  try {
    const info = fs.lstatSync(dir);
    if (info.isSymbolicLink() || (info.mode & 0o077) === 0 || info.uid !== process.getuid()) return;
    fs.chmodSync(dir, 0o700);
  } catch {
    // Ignored on purpose; see above.
  }
}

// The previous record, or null when there is none. A file that is not a JSON object is marked unreadable
// (and replaced by the write); one that cannot be read at all fails the beat.
function readPrevious(file, door) {
  let text;
  try { text = fs.readFileSync(file, "utf8"); } catch (err) {
    if (err.code === "ENOENT") return { record: null, unreadable: false };
    throw new SessionError(`could not read ${relativeSessionFile(door)}: ${err.code}`);
  }
  let record;
  try { record = JSON.parse(text); } catch {
    return { record: null, unreadable: true };
  }
  return isPlainObject(record) ? { record, unreadable: false } : { record: null, unreadable: true };
}

// The previous values worth keeping: each field only when it has its own shape, else null.
function keptFields(previous) {
  const text = (value) => (typeof value === "string" ? wellFormed(value) : null);
  const progress = previous?.progress;
  const keptProgress = isPlainObject(progress) && typeof progress.done === "number" && typeof progress.total === "number"
    ? { done: progress.done, total: progress.total }
    : null;
  return { session_id: text(previous?.session_id), now: text(previous?.now), slice: text(previous?.slice), progress: keptProgress };
}

// Every door's record as stored, by door, and the files that could not be read or parsed.
export function readSessions(root) {
  const sessions = {};
  const skipped = [];
  for (const door of DOORS) {
    let text;
    try { text = fs.readFileSync(path.join(sessionsDir(root), `${door}.json`), "utf8"); } catch (err) {
      if (err.code !== "ENOENT") skipped.push(`${relativeSessionFile(door)}: ${err.code}`);
      continue;
    }
    try { sessions[door] = JSON.parse(text); } catch {
      skipped.push(`${relativeSessionFile(door)}: not valid JSON`);
    }
  }
  return { sessions, skipped };
}

// A cut that splits a surrogate pair leaves half of it; toWellFormed turns that half into U+FFFD, which
// is what a Go string holds in its place, so both runtimes write the same bytes.
function cut(text, maxUnits) {
  return wellFormed(text.slice(0, maxUnits));
}

function wellFormed(text) {
  return text.toWellFormed();
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
