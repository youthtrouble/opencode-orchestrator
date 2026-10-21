// A durable, append-only mailbox between agents/harnesses working on one project.
// Messages live in <project>/.orchestrator/mailbox/messages.jsonl; acks in acks.jsonl.
// Any harness (opencode agent, Claude Code, Codex, a human) can post, list and ack.
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

export function mailboxDir(projectRoot) {
  return path.join(projectRoot, ".orchestrator", "mailbox");
}

function appendJsonl(file, obj) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.appendFileSync(file, JSON.stringify(obj) + "\n");
}

function readJsonl(file) {
  if (!fs.existsSync(file)) return [];
  return fs.readFileSync(file, "utf8").split("\n").filter(Boolean).map((l) => { try { return JSON.parse(l); } catch { return null; } }).filter(Boolean);
}

// Thrown when a question carries no recommended answer: the human must be able to accept one as it stands.
export class QuestionNeedsRecommendationError extends Error {
  constructor() { super("a question needs a recommended answer"); }
}

// Thrown when a question's recommended answer cannot stand: an action question takes none, and a decision's
// recommendation must not report work as done (incident 2026-10-21: an Accept told the conductor that setup
// scripts had run when nothing had).
export class RecommendationRefusedError extends Error {}

// What a question asks of the human: a `decision` they may accept as recommended (also when `ask` is absent,
// as on every question posted before it), or an `action` they must perform, whose steps the body carries.
export const ASKS = ["decision", "action"];

// A recommended answer reads as a completion claim when, trimmed (ASCII case aside), it opens with:
//   - `completed`, `all done`, `i ran`, `i've done` (either apostrophe), `i have done` or `i did`, as whole words;
//   - `done` followed by `:` `,` `.` `!` `—`, the end of the text, or a clause cue;
//   - `ran`, `created`, `installed`, `finished`, `deployed` or `rebuilt` followed by `:` `,` or a clause cue;
// where a clause cue is spaces or tabs then the word it, the, that, this, successfully, ok or all.
// So "Done: token created" and "Ran the script" are refused while "Done is better than perfect" and "Ran Kirsh"
// pass. It is a guard rail for the obvious slip, not a guarantee: "Yes, done" passes. The defences that hold are
// that an action question offers no Accept and that the conductor verifies what the owner reports.
// The /i is ASCII-only without the u flag, and \b is an ASCII word boundary; package mailbox in Go matches alike.
const CLAUSE_CUE = String.raw`[ \t]+(?:it|the|that|this|successfully|ok|all)\b`;
const COMPLETION_CLAIM = new RegExp(
  String.raw`^(?:(?:completed|all done|i ran|i['’]ve done|i have done|i did)\b|done(?:[:,.!—]|$|${CLAUSE_CUE})|(?:ran|created|installed|finished|deployed|rebuilt)(?:[:,]|${CLAUSE_CUE}))`,
  "i",
);

export function readsAsCompletionClaim(recommended) {
  return COMPLETION_CLAIM.test(recommended.trim());
}

// A question also records the answer its poster recommends and whether it blocks the poster (default true);
// an action question records `ask` instead of a recommendation. Other kinds ignore all three, so their lines
// stay as they were.
export function post(projectRoot, { from, to = "*", kind = "note", subject, body = "", refs = [], thread = null, recommended, blocking = true, ask }) {
  if (!from || !subject) throw new Error("post needs from and subject");
  const msg = { id: crypto.randomUUID(), at: new Date().toISOString(), from, to, kind, subject, body, refs, thread };
  if (kind === "question") Object.assign(msg, questionFields({ recommended, blocking, ask }));
  appendJsonl(path.join(mailboxDir(projectRoot), "messages.jsonl"), msg);
  return msg;
}

// A decision is written as before, without `ask`, so absent, empty and "decision" mean one thing (an empty ask is
// a decision in Go too, where it is Draft.Ask's zero value).
function questionFields({ recommended, blocking, ask }) {
  if (ask === undefined || ask === "") ask = "decision";
  if (!ASKS.includes(ask)) throw new Error(`a question asks for a decision or an action, not ${ask}`);
  if (ask === "action") {
    if (recommended !== undefined) throw new RecommendationRefusedError("an action question takes no recommended answer; its body carries the steps");
    return { blocking, ask };
  }
  if (typeof recommended !== "string" || !recommended) throw new QuestionNeedsRecommendationError();
  if (readsAsCompletionClaim(recommended)) throw new RecommendationRefusedError("a recommended answer is a decision to accept, not a report that work is done");
  return { recommended, blocking };
}

// Thrown when no message matches an id or prefix, so a command can name the mailbox it searched.
export class UnknownMessageError extends Error {}

// Accepts a full id or any unique prefix (list prints 8-char prefixes).
export function resolveId(projectRoot, idOrPrefix) {
  const msgs = readJsonl(path.join(mailboxDir(projectRoot), "messages.jsonl"));
  const exact = msgs.find((m) => m.id === idOrPrefix);
  if (exact) return exact.id;
  const matches = msgs.filter((m) => m.id.startsWith(idOrPrefix));
  if (matches.length === 1) return matches[0].id;
  if (matches.length) throw new Error(`ambiguous message id prefix ${idOrPrefix}`);
  throw new UnknownMessageError(`no message with id ${idOrPrefix}`);
}

export function ack(projectRoot, { id, by, note = "" }) {
  id = resolveId(projectRoot, id);
  const entry = { id, by, at: new Date().toISOString(), note };
  appendJsonl(path.join(mailboxDir(projectRoot), "acks.jsonl"), entry);
  return entry;
}

export function list(projectRoot, { to = null, unackedBy = null, kind = null, since = null, limit = 50 } = {}) {
  const dir = mailboxDir(projectRoot);
  const msgs = readJsonl(path.join(dir, "messages.jsonl"));
  const acks = readJsonl(path.join(dir, "acks.jsonl"));
  const ackedBy = new Map();
  for (const a of acks) { if (!ackedBy.has(a.id)) ackedBy.set(a.id, new Set()); ackedBy.get(a.id).add(a.by); }
  let out = msgs;
  if (to) out = out.filter((m) => m.to === "*" || m.to === to);
  if (kind) out = out.filter((m) => m.kind === kind);
  if (since) out = out.filter((m) => m.at > since);
  if (unackedBy) out = out.filter((m) => !(ackedBy.get(m.id)?.has(unackedBy)));
  return out.slice(-limit).map((m) => ({ ...m, acked_by: [...(ackedBy.get(m.id) || [])] }));
}

export const KINDS = ["note", "brief", "release", "review", "closure", "question", "handoff", "wake", "blocked"];
