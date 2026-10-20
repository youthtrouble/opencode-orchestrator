#!/usr/bin/env node
// mailbox.mjs post --from A [--to B] [--kind release] --subject S [--body text|-] [--ref path]... [--thread id]
// mailbox.mjs list [--to B] [--unacked-by B] [--kind k] [--since iso] [--limit n] [--json]
// mailbox.mjs ack --id ID --by B [--note text]
// mailbox.mjs reply ID-or-prefix "your answer"      (posts a threaded note from "human" and acks the message;
//                                                     a reply to a human gate's question also clears the gate)
// mailbox.mjs inbox                                  (unanswered questions addressed to you, with the reply command)
import fs from "node:fs";
import path from "node:path";
import tty from "node:tty";
import { post, list, ack, resolveId, mailboxDir, UnknownMessageError } from "../lib/mailbox.mjs";
import { clearGate } from "../lib/flip.mjs";
import { requireProject } from "../lib/project.mjs";

// A reply to message `id` answers the human gate when the gate's question (stored as a full id or a
// prefix) resolves to that same message, the way the reply's own id was resolved; a question that
// resolves to no single message is reported and answers nothing. Answering clears the gate and queues
// the conductor's relaunch. The reply has already succeeded: a failure here is reported on stderr and
// leaves the exit code alone.
function clearGateAnsweredBy(root, id) {
  const flip = readFlipForGate(root);
  const question = flip?.awaiting_human?.question;
  if (typeof question !== "string" || !question) return;
  let gateId;
  try { gateId = resolveId(root, question); } catch (e) {
    console.error(`could not match the human gate's question: ${e.message}`);
    return;
  }
  if (gateId !== id) return;
  try {
    const door = clearGate(root, flip);
    console.log(door ? `cleared the human gate; the supervisor relaunches the ${door} conductor within a minute` : "cleared the human gate");
  } catch (e) {
    console.error(`could not clear the human gate: ${e.message}`);
  }
}

// flip.json, or null when there is none. A file that cannot be read or parsed is reported, not skipped silently.
function readFlipForGate(root) {
  let text;
  try { text = fs.readFileSync(path.join(root, ".orchestrator", "flip.json"), "utf8"); } catch (e) {
    if (e.code !== "ENOENT") console.error(`could not check for a human gate: ${e.message}`);
    return null;
  }
  try { return JSON.parse(text); } catch {
    console.error("could not check for a human gate: .orchestrator/flip.json is not valid JSON");
    return null;
  }
}

const [cmd, ...rest] = process.argv.slice(2);
const opts = { ref: [] }; const flags = new Set();
for (let i = 0; i < rest.length; i++) {
  const a = rest[i];
  if (!a.startsWith("--")) continue;
  const key = a.slice(2);
  const next = rest[i + 1];
  if (next === undefined || next.startsWith("--")) { flags.add(key); continue; }
  if (key === "ref") opts.ref.push(next); else opts[key] = next;
  i++;
}
const root = path.resolve(opts.root || process.env.ORCHESTRATOR_PROJECT || process.cwd());
if (["list", "ack", "reply", "inbox"].includes(cmd)) requireProject(root, "--root");

// An unknown id usually means the shell is in another project: name the mailbox searched.
function exitOnUnknownId(action) {
  try { return action(); } catch (e) {
    if (!(e instanceof UnknownMessageError)) throw e;
    console.error(`${e.message} in ${mailboxDir(root)}; if it belongs to another project, cd there or pass --root <project dir>`);
    process.exit(1);
  }
}
if (cmd === "post") {
  let body = opts.body || "";
  if (body === "-") body = fs.readFileSync(0, "utf8");
  const m = post(root, { from: opts.from, to: opts.to || "*", kind: opts.kind || "note", subject: opts.subject, body, refs: opts.ref, thread: opts.thread || null });
  console.log(JSON.stringify({ id: m.id, at: m.at }));
} else if (cmd === "list") {
  const msgs = list(root, { to: opts.to || null, unackedBy: opts["unacked-by"] || null, kind: opts.kind || null, since: opts.since || null, limit: Number(opts.limit || 50) });
  if (flags.has("json")) console.log(JSON.stringify(msgs, null, 2));
  else for (const m of msgs) console.log(`${m.at}  ${m.kind.padEnd(8)} ${m.from} -> ${m.to}  ${m.subject}  [${m.id.slice(0, 8)}]${m.acked_by.length ? "  acked:" + m.acked_by.join(",") : ""}${m.refs.length ? "\n    refs: " + m.refs.join(", ") : ""}${m.body ? "\n    " + m.body.split("\n").slice(0, 6).join("\n    ") : ""}`);
} else if (cmd === "reply") {
  const [idArg, ...words] = rest.filter((a) => !a.startsWith("--"));
  // tty.isatty, not process.stdin.isTTY, which makes a stdin pipe non-blocking (see readPrompt).
  const body = words.join(" ") || (tty.isatty(0) ? "" : fs.readFileSync(0, "utf8"));
  if (!idArg || !body) { console.error('usage: mailbox.mjs reply <id-or-prefix> "your answer"'); process.exit(2); }
  const id = exitOnUnknownId(() => resolveId(root, idArg));
  const orig = list(root, { limit: 100000 }).find((m) => m.id === id);
  const m = post(root, { from: "human", to: orig?.from || "*", kind: "note", subject: `Re: ${orig?.subject || id}`.slice(0, 140), body, thread: id });
  ack(root, { id, by: "human", note: "replied" });
  console.log(`replied (${m.id.slice(0, 8)}) and acknowledged ${id.slice(0, 8)}; the conductor reads it on its next turn`);
  clearGateAnsweredBy(root, id);
} else if (cmd === "inbox") {
  const qs = list(root, { kind: "question", unackedBy: "human", limit: 50 }).filter((m) => m.to === "human" || m.to === "*");
  if (!qs.length) { console.log("No open questions for you."); process.exit(0); }
  for (const m of qs) console.log(`[${m.id.slice(0, 8)}] ${m.at.slice(0, 16)}Z from ${m.from}: ${m.subject}\n    ${String(m.body).slice(0, 400).replace(/\n/g, " ")}\n    reply: node ~/.config/opencode/orchestrator/bin/mailbox.mjs reply ${m.id.slice(0, 8)} "your answer"\n`);
} else if (cmd === "ack") {
  console.log(JSON.stringify(exitOnUnknownId(() => ack(root, { id: opts.id, by: opts.by, note: opts.note || "" }))));
} else { console.error("commands: post | list | ack | reply | inbox"); process.exit(2); }
