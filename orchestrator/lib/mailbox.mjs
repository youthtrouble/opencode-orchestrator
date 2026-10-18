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

export function post(projectRoot, { from, to = "*", kind = "note", subject, body = "", refs = [], thread = null }) {
  if (!from || !subject) throw new Error("post needs from and subject");
  const msg = { id: crypto.randomUUID(), at: new Date().toISOString(), from, to, kind, subject, body, refs, thread };
  appendJsonl(path.join(mailboxDir(projectRoot), "messages.jsonl"), msg);
  return msg;
}

// Accepts a full id or any unique prefix (list prints 8-char prefixes).
export function resolveId(projectRoot, idOrPrefix) {
  const msgs = readJsonl(path.join(mailboxDir(projectRoot), "messages.jsonl"));
  const exact = msgs.find((m) => m.id === idOrPrefix);
  if (exact) return exact.id;
  const matches = msgs.filter((m) => m.id.startsWith(idOrPrefix));
  if (matches.length === 1) return matches[0].id;
  throw new Error(matches.length ? `ambiguous message id prefix ${idOrPrefix}` : `no message with id ${idOrPrefix}`);
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
