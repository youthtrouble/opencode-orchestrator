#!/usr/bin/env node
// mailbox.mjs post --from A [--to B] [--kind release] --subject S [--body text|-] [--ref path]... [--thread id]
// mailbox.mjs list [--to B] [--unacked-by B] [--kind k] [--since iso] [--limit n] [--json]
// mailbox.mjs ack --id ID --by B [--note text]
import fs from "node:fs";
import path from "node:path";
import { post, list, ack } from "../lib/mailbox.mjs";

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
if (cmd === "post") {
  let body = opts.body || "";
  if (body === "-") body = fs.readFileSync(0, "utf8");
  const m = post(root, { from: opts.from, to: opts.to || "*", kind: opts.kind || "note", subject: opts.subject, body, refs: opts.ref, thread: opts.thread || null });
  console.log(JSON.stringify({ id: m.id, at: m.at }));
} else if (cmd === "list") {
  const msgs = list(root, { to: opts.to || null, unackedBy: opts["unacked-by"] || null, kind: opts.kind || null, since: opts.since || null, limit: Number(opts.limit || 50) });
  if (flags.has("json")) console.log(JSON.stringify(msgs, null, 2));
  else for (const m of msgs) console.log(`${m.at}  ${m.kind.padEnd(8)} ${m.from} -> ${m.to}  ${m.subject}  [${m.id.slice(0, 8)}]${m.acked_by.length ? "  acked:" + m.acked_by.join(",") : ""}${m.refs.length ? "\n    refs: " + m.refs.join(", ") : ""}${m.body ? "\n    " + m.body.split("\n").slice(0, 6).join("\n    ") : ""}`);
} else if (cmd === "ack") {
  console.log(JSON.stringify(ack(root, { id: opts.id, by: opts.by, note: opts.note || "" })));
} else { console.error("commands: post | list | ack"); process.exit(2); }
