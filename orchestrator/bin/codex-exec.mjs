#!/usr/bin/env node
// Delegate a bounded task to the Codex CLI on the ChatGPT subscription and get a structured result.
//
//   codex-exec.mjs [--model gpt-6.1-sol] [--effort low|medium|high|xhigh|max|ultra]
//                  [--sandbox read-only|workspace-write|danger-full-access] [--cwd DIR]
//                  [--resume THREAD_ID] [--schema FILE] [--timeout-min 60] [--force] [--quiet]
//                  [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]
//
// Prints one JSON object on stdout: { ok, thread_id, final_message, usage, files_changed, commands,
// duration_ms, record } and streams progress lines to stderr. Exit codes: 0 ok, 1 turn failed,
// 75 budget gate refused, 2 usage error.
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import readline from "node:readline";
import { parseArgs, readPrompt, writeRunRecord } from "../lib/run-record.mjs";
import { budgetGate } from "../lib/budget.mjs";
import { record as incident } from "../lib/incident.mjs";
import { getUsage } from "../lib/usage.mjs";

const { opts, positional } = parseArgs(process.argv.slice(2), {
  valueKeys: ["model", "effort", "sandbox", "cwd", "resume", "schema", "timeout-min", "prompt", "prompt-file", "add-dir"],
  flagKeys: ["force", "quiet", "ephemeral", "help"],
});
if (opts.help) { console.log(fs.readFileSync(new URL(import.meta.url)).toString().split("\n").slice(1, 12).join("\n")); process.exit(0); }

let prompt;
try { prompt = readPrompt(opts, positional); } catch (e) { console.error(e.message); process.exit(2); }
const cwd = path.resolve(opts.cwd || process.cwd());
const model = opts.model || "gpt-6.1-sol";
const effort = opts.effort || "high";
const sandbox = opts.sandbox || "workspace-write";
const timeoutMs = Number(opts["timeout-min"] || 60) * 60 * 1000;
const log = (s) => { if (!opts.quiet) console.error(`[codex] ${s}`); };

const gate = await budgetGate("openai", { force: Boolean(opts.force), frontier: model.includes("astra") });
if (!gate.allowed) { try { incident({ component: "bridge", kind: "limit", summary: `${path.basename(process.argv[1])} refused: budget red`, context: { resets_at: gate.resets_at || null }, project: cwd }); } catch { /* ignore */ } console.log(JSON.stringify({ ok: false, reason: "budget_red", ...gate })); process.exit(75); }

const args = ["exec", "--json", "--skip-git-repo-check", "-C", cwd, "-s", sandbox, "-c", `model_reasoning_effort="${effort}"`];
if (opts.resume) args.push("resume", opts.resume); else args.push("-m", model);
if (opts.schema) args.push("--output-schema", path.resolve(opts.schema));
if (opts["add-dir"]) args.push("--add-dir", path.resolve(opts["add-dir"]));
if (opts.ephemeral) args.push("--ephemeral");
if (sandbox === "workspace-write") args.push("--approve-for-me");
args.push("-");

const started = Date.now();
const startedIso = new Date(started).toISOString();
log(`start model=${opts.resume ? "(resumed)" : model} effort=${effort} sandbox=${sandbox} cwd=${cwd}`);
const child = spawn("codex", args, { cwd, stdio: ["pipe", "pipe", "pipe"] });
child.stdin.end(prompt);
let stderrBuf = "";
child.stderr.setEncoding("utf8");
child.stderr.on("data", (c) => { stderrBuf += c; });
const timer = setTimeout(() => { log("timeout; killing"); child.kill("SIGTERM"); }, timeoutMs);

const result = { ok: false, harness: "codex", thread_id: opts.resume || null, model, effort, sandbox, cwd, started_at: startedIso, final_message: "", usage: null, files_changed: [], commands: [], errors: [] };
const rl = readline.createInterface({ input: child.stdout });
rl.on("line", (line) => {
  let ev; try { ev = JSON.parse(line); } catch { return; }
  switch (ev.type) {
    case "thread.started": result.thread_id = ev.thread_id; log(`thread ${ev.thread_id}`); break;
    case "item.completed": {
      const it = ev.item || {};
      if (it.type === "agent_message") { result.final_message = it.text || result.final_message; log(`message: ${(it.text || "").slice(0, 160).replace(/\n/g, " ")}`); }
      else if (it.type === "command_execution") { result.commands.push({ command: it.command, exit_code: it.exit_code }); log(`ran: ${(it.command || "").slice(0, 120)} -> ${it.exit_code}`); }
      else if (it.type === "file_change") { for (const c of it.changes || []) { result.files_changed.push({ path: c.path, kind: c.kind }); } log(`files: ${(it.changes || []).map((c) => c.path).join(", ")}`); }
      else if (it.type === "error") { result.errors.push(it.message); log(`error: ${it.message}`); }
      else if (it.type === "reasoning") { log(`thinking: ${(it.text || "").slice(0, 100).replace(/\n/g, " ")}`); }
      break;
    }
    case "turn.completed": result.usage = ev.usage || null; result.ok = true; break;
    case "turn.failed": result.errors.push(ev.error?.message || JSON.stringify(ev.error || ev)); result.ok = false; break;
    default: break;
  }
});

const code = await new Promise((resolve) => child.on("close", resolve));
clearTimeout(timer);
result.exit_code = code;
result.duration_ms = Date.now() - started;
if (code !== 0 && result.errors.length === 0) result.errors.push(`codex exited ${code}: ${stderrBuf.trim().split("\n").slice(-3).join(" | ")}`);
if (code !== 0) result.ok = false;
// A "completed" turn in which Codex ran nothing and explains that its tools failed is a tool-host outage,
// not a result. Codex reports these as prose, so detect them here and make the failure visible.
const NOOP_RE = /timed out negotiating with the code-mode host|could not run|unable to (run|execute)|tool (host|calls?) (failed|unavailable)|no tool/i;
if (result.ok && result.commands.length === 0 && result.files_changed.length === 0 && NOOP_RE.test(result.final_message || "")) {
  result.ok = false; result.reason = "tool_host_unavailable"; result.errors.push("codex completed the turn without executing any tool; its message reports tool-host failure (retry after ~60s)");
  try { incident({ component: "bridge", kind: "crash", summary: "codex turn completed with zero tool calls; code-mode host timeout reported in prose", detail: (result.final_message || "").slice(0, 400), context: { model, thread_id: result.thread_id }, project: cwd }); } catch { /* ignore */ }
}
result.record = writeRunRecord(cwd, { ...result, prompt_sha256: (await import("node:crypto")).createHash("sha256").update(prompt).digest("hex"), prompt_head: prompt.slice(0, 400) });
getUsage({ refresh: true }).catch(() => {});
log(`done ok=${result.ok} in ${Math.round(result.duration_ms / 1000)}s; record ${result.record}`);
if (!result.ok) { try { incident({ component: "bridge", kind: /limit|rate/i.test(result.errors.join(" ")) ? "limit" : "crash", summary: `${result.harness} bridge failed: ${result.errors.join("; ").slice(0, 160)}`, detail: result.errors.join("\n"), context: { model: result.model || null, exit_code: result.exit_code }, project: cwd }); } catch { /* ignore */ } }
console.log(JSON.stringify(result));
process.exit(result.ok ? 0 : 1);
