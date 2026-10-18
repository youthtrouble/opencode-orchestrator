#!/usr/bin/env node
// Delegate a bounded task to Claude Code (print mode) on the Claude subscription, structured result back.
//
//   claude-exec.mjs [--model claude-opus-5-5|opus|sonnet|haiku|fable] [--effort low|medium|high|xhigh|max]
//                   [--permission-mode acceptEdits|auto|plan|dontAsk] [--allowed-tools "Read,Edit,Bash(go *)"]
//                   [--max-turns N] [--max-budget-usd X] [--cwd DIR] [--resume SESSION_ID] [--agent NAME]
//                   [--append-system-prompt TEXT] [--json-schema FILE] [--timeout-min 60] [--force] [--quiet]
//                   [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]
//
// Prints one JSON object on stdout: { ok, session_id, final_message, usage, model_usage, cost_usd,
// num_turns, permission_denials, duration_ms, record }. Exit codes: 0 ok, 1 error, 75 budget refused.
import { spawn } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import readline from "node:readline";
import { parseArgs, readPrompt, writeRunRecord } from "../lib/run-record.mjs";
import { budgetGate } from "../lib/budget.mjs";
import { record as incident } from "../lib/incident.mjs";
import { getUsage } from "../lib/usage.mjs";

const { opts, positional } = parseArgs(process.argv.slice(2), {
  valueKeys: ["model", "effort", "permission-mode", "allowed-tools", "disallowed-tools", "max-turns", "max-budget-usd", "cwd", "resume", "agent", "append-system-prompt", "json-schema", "timeout-min", "prompt", "prompt-file", "add-dir"],
  flagKeys: ["force", "quiet", "help"],
});
if (opts.help) { console.log(fs.readFileSync(new URL(import.meta.url)).toString().split("\n").slice(1, 12).join("\n")); process.exit(0); }
let prompt;
try { prompt = readPrompt(opts, positional); } catch (e) { console.error(e.message); process.exit(2); }
const cwd = path.resolve(opts.cwd || process.cwd());
const model = opts.model || "claude-opus-5-5";
const timeoutMs = Number(opts["timeout-min"] || 60) * 60 * 1000;
const log = (s) => { if (!opts.quiet) console.error(`[claude] ${s}`); };

const isFrontier = /fable/i.test(model);
const gate = await budgetGate("anthropic", { force: Boolean(opts.force), frontier: isFrontier });
if (!gate.allowed) { try { incident({ component: "bridge", kind: "limit", summary: `${path.basename(process.argv[1])} refused: budget red`, context: { resets_at: gate.resets_at || null }, project: cwd }); } catch { /* ignore */ } console.log(JSON.stringify({ ok: false, reason: "budget_red", ...gate })); process.exit(75); }

const args = ["-p", "--output-format", "stream-json", "--verbose", "--permission-mode", opts["permission-mode"] || "auto", "--permission-prompts", "none"];
if (opts.resume) args.push("--resume", opts.resume); else args.push("--model", model);
if (opts.effort) args.push("--effort", opts.effort);
if (opts["allowed-tools"]) args.push("--allowedTools", opts["allowed-tools"]);
if (opts["disallowed-tools"]) args.push("--disallowedTools", opts["disallowed-tools"]);
if (opts["max-turns"]) args.push("--max-turns", String(opts["max-turns"]));
if (opts["max-budget-usd"]) args.push("--max-budget-usd", String(opts["max-budget-usd"]));
if (opts.agent) args.push("--agent", opts.agent);
if (opts["append-system-prompt"]) args.push("--append-system-prompt", opts["append-system-prompt"]);
if (opts["json-schema"]) args.push("--json-schema", fs.readFileSync(path.resolve(opts["json-schema"]), "utf8"));
if (opts["add-dir"]) args.push("--add-dir", path.resolve(opts["add-dir"]));
args.push("--", prompt);

const started = Date.now();
log(`start model=${opts.resume ? "(resumed)" : model}${opts.effort ? " effort=" + opts.effort : ""} mode=${opts["permission-mode"] || "acceptEdits"} cwd=${cwd}`);
const child = spawn("claude", args, { cwd, stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, CLAUDECODE: undefined } });
let stderrBuf = ""; child.stderr.on("data", (c) => { stderrBuf += c; });
const timer = setTimeout(() => { log("timeout; killing"); child.kill("SIGTERM"); }, timeoutMs);

const result = { ok: false, harness: "claude", session_id: opts.resume || null, model, effort: opts.effort || null, cwd, started_at: new Date(started).toISOString(), final_message: "", usage: null, model_usage: null, cost_usd: null, num_turns: null, permission_denials: [], tools_used: {}, errors: [], structured_output: null };
const rl = readline.createInterface({ input: child.stdout });
rl.on("line", (line) => {
  let ev; try { ev = JSON.parse(line); } catch { return; }
  if (ev.session_id && !result.session_id) result.session_id = ev.session_id;
  if (ev.type === "system" && ev.subtype === "init") log(`session ${ev.session_id} model=${ev.model}`);
  else if (ev.type === "assistant") {
    for (const block of ev.message?.content || []) {
      if (block.type === "text" && block.text) log(`message: ${block.text.slice(0, 160).replace(/\n/g, " ")}`);
      else if (block.type === "tool_use") { result.tools_used[block.name] = (result.tools_used[block.name] || 0) + 1; log(`tool: ${block.name}${block.input?.command ? " " + String(block.input.command).slice(0, 100) : block.input?.file_path ? " " + block.input.file_path : ""}`); }
    }
  } else if (ev.type === "result") {
    result.ok = !ev.is_error; result.final_message = ev.result || ""; result.usage = ev.usage || null; result.model_usage = ev.modelUsage || null;
    result.cost_usd = ev.total_cost_usd ?? null; result.num_turns = ev.num_turns ?? null; result.permission_denials = ev.permission_denials || []; result.structured_output = ev.structured_output ?? null;
    if (ev.is_error) result.errors.push(ev.subtype || "error");
  }
});
const code = await new Promise((resolve) => child.on("close", resolve));
clearTimeout(timer);
result.exit_code = code; result.duration_ms = Date.now() - started;
if (code !== 0 && result.errors.length === 0) result.errors.push(`claude exited ${code}: ${stderrBuf.trim().split("\n").slice(-3).join(" | ")}`);
if (code !== 0) result.ok = false;
const LIMIT_TEXT = /hit your (session|usage|weekly) limit|usage limit reached|rate limit/i;
if (!result.ok && LIMIT_TEXT.test(result.final_message || "")) {
  result.reason = "budget_red"; result.errors = ["usage_limit"];
  try { const { getUsage } = await import("../lib/usage.mjs"); const u = await getUsage({ refresh: true }); result.resets_at = u.claude?.session?.resets_at || null; } catch { /* unknown reset */ }
  try { incident({ component: "bridge", kind: "limit", summary: "claude run ended on a session-limit message", context: { model: result.model, resets_at: result.resets_at || null }, project: cwd }); } catch { /* ignore */ }
  console.log(JSON.stringify(result)); process.exit(75);
}
result.record = writeRunRecord(cwd, { ...result, prompt_sha256: crypto.createHash("sha256").update(prompt).digest("hex"), prompt_head: prompt.slice(0, 400) });
getUsage({ refresh: true }).catch(() => {});
log(`done ok=${result.ok} turns=${result.num_turns} in ${Math.round(result.duration_ms / 1000)}s; record ${result.record}`);
if (!result.ok) { try { incident({ component: "bridge", kind: /limit|rate/i.test(result.errors.join(" ")) ? "limit" : "crash", summary: `${result.harness} bridge failed: ${result.errors.join("; ").slice(0, 160)}`, detail: result.errors.join("\n"), context: { model: result.model || null, exit_code: result.exit_code }, project: cwd }); } catch { /* ignore */ } }
console.log(JSON.stringify(result));
process.exit(result.ok ? 0 : 1);
