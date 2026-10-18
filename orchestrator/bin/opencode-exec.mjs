#!/usr/bin/env node
// Run one headless opencode turn (used to launch/steer the OpenAI-native conductor as a worker lane).
//   opencode-exec.mjs [--agent conductor] [--cwd DIR] [--resume SESSION] [--timeout-min 120] [--quiet] [--prompt TEXT | --prompt-file F | stdin]
// Prints compact JSON like the other bridges: { ok, session_id, final_message, duration_ms, record }.
import { spawn } from "node:child_process";
import crypto from "node:crypto";
import path from "node:path";
import readline from "node:readline";
import { parseArgs, readPrompt, writeRunRecord } from "../lib/run-record.mjs";
import { budgetGate } from "../lib/budget.mjs";

const { opts, positional } = parseArgs(process.argv.slice(2), { valueKeys: ["agent", "cwd", "resume", "timeout-min", "prompt", "prompt-file", "model"], flagKeys: ["quiet", "force", "help"] });
let prompt; try { prompt = readPrompt(opts, positional); } catch (e) { console.error(e.message); process.exit(2); }
const cwd = path.resolve(opts.cwd || process.cwd());
const log = (s) => { if (!opts.quiet) console.error(`[opencode] ${s}`); };
const gate = await budgetGate("openai", { force: Boolean(opts.force) });
if (!gate.allowed) { console.log(JSON.stringify({ ok: false, reason: "budget_red", ...gate })); process.exit(75); }
const args = ["run", "--format", "json", "--auto", "--agent", opts.agent || "conductor"];
if (opts.resume) args.push("--session", opts.resume);
if (opts.model) args.push("--model", opts.model);
args.push(prompt);
const started = Date.now();
const child = spawn("opencode", args, { cwd, stdio: ["ignore", "pipe", "pipe"] });
const timer = setTimeout(() => child.kill("SIGTERM"), Number(opts["timeout-min"] || 120) * 60000);
const result = { ok: false, harness: "opencode", session_id: opts.resume || null, agent: opts.agent || "conductor", cwd, started_at: new Date(started).toISOString(), final_message: "", errors: [] };
let text = ""; let stderr = "";
child.stderr.on("data", (c) => (stderr += c));
readline.createInterface({ input: child.stdout }).on("line", (line) => {
  let ev; try { ev = JSON.parse(line); } catch { return; }
  if (ev.sessionID && !result.session_id) result.session_id = ev.sessionID;
  const p = ev.part || {};
  if (p.type === "text" && p.text) { text = p.text; log(`message: ${p.text.slice(0, 160).replace(/\n/g, " ")}`); }
  if (p.type === "tool") log(`tool: ${p.tool}`);
});
const code = await new Promise((r) => child.on("close", r));
clearTimeout(timer);
result.final_message = text; result.ok = code === 0; result.exit_code = code; result.duration_ms = Date.now() - started;
if (code !== 0) result.errors.push(stderr.trim().split("\n").slice(-3).join(" | "));
result.record = writeRunRecord(cwd, { ...result, prompt_sha256: crypto.createHash("sha256").update(prompt).digest("hex"), prompt_head: prompt.slice(0, 400) });
console.log(JSON.stringify(result));
process.exit(result.ok ? 0 : 1);
