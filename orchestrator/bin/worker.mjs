#!/usr/bin/env node
// Run ONE bounded brief on ONE harness as a tracked, lockable, resumable worker.
//
//   worker.mjs start --brief .orchestrator/briefs/B-01.md --harness codex|claude [--lane name]
//                    [--model M] [--effort E] [--sandbox S] [--permission-mode P] [--allowed-tools T]
//                    [--resume ID] [--breadth focused|standard|broad] [--cwd DIR] [--detach] [--force]
//   worker.mjs status [--lane name] [--cwd DIR]
//   worker.mjs stop --lane name [--cwd DIR]
//
// State: <project>/.orchestrator/workers/<lane>.json ; lock: <lane>.lock ; log: <project>/.orchestrator/logs/<lane>-<run>.log
// On completion the worker posts a mailbox message (kind release|blocked|deferred) so the other side is woken.
import { spawn } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "../lib/run-record.mjs";
import { post } from "../lib/mailbox.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const { opts, positional } = parseArgs(process.argv.slice(2), {
  valueKeys: ["brief", "harness", "lane", "model", "effort", "sandbox", "permission-mode", "allowed-tools", "resume", "breadth", "cwd", "max-turns", "timeout-min", "log-path", "run-id"],
  flagKeys: ["detach", "force", "help"],
});
const cmd = positional[0];
const root = path.resolve(opts.cwd || process.cwd());
const workersDir = path.join(root, ".orchestrator", "workers");
const logsDir = path.join(root, ".orchestrator", "logs");
const laneOf = () => opts.lane || (opts.brief ? path.basename(opts.brief).replace(/\.md$/, "") : null);
const statePath = (lane) => path.join(workersDir, `${lane}.json`);
const lockPath = (lane) => path.join(workersDir, `${lane}.lock`);

function readState(lane) { try { return JSON.parse(fs.readFileSync(statePath(lane), "utf8")); } catch { return null; } }
function alive(pid) { if (!pid) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } }
function publish(state, changes) {
  Object.assign(state, changes, { updated_at: new Date().toISOString() });
  try { fs.mkdirSync(workersDir, { recursive: true }); const tmp = statePath(state.lane) + ".tmp"; fs.writeFileSync(tmp, JSON.stringify(state, null, 2) + "\n"); fs.renameSync(tmp, statePath(state.lane)); state.state_storage = "available"; }
  catch (err) { state.state_storage = "unavailable"; state.last_state_write_error = err.code || String(err); } // never kill the author on ENOSPC
}

if (cmd === "status") {
  const lanes = opts.lane ? [opts.lane] : fs.existsSync(workersDir) ? fs.readdirSync(workersDir).filter((f) => f.endsWith(".json")).map((f) => f.replace(/\.json$/, "")) : [];
  const out = lanes.map((lane) => { const s = readState(lane) || { lane, status: "unknown" }; return { ...s, process_alive: alive(s.pid), lock_present: fs.existsSync(lockPath(lane)) }; });
  console.log(JSON.stringify(out, null, 2));
  process.exit(0);
}
if (cmd === "stop") {
  const lane = opts.lane; const s = readState(lane);
  if (!s || !alive(s.pid)) { console.log(JSON.stringify({ lane, stopped: false, reason: "no live worker" })); process.exit(0); }
  try { process.kill(-s.pid, "SIGINT"); } catch { process.kill(s.pid, "SIGINT"); }
  console.log(JSON.stringify({ lane, stopped: true, pid: s.pid }));
  process.exit(0);
}
if (cmd !== "start" || !opts.brief || !opts.harness) { console.error("usage: worker.mjs start --brief FILE --harness codex|claude [--lane L] [...] | status | stop --lane L"); process.exit(2); }

const brief = path.resolve(root, opts.brief);
if (!fs.existsSync(brief) || !brief.endsWith(".md")) { console.error("brief must be an existing .md file"); process.exit(2); }
const lane = laneOf();
const harness = opts.harness;
if (!["codex", "claude"].includes(harness)) { console.error("harness must be codex or claude"); process.exit(2); }

if (opts.detach) {
  fs.mkdirSync(logsDir, { recursive: true });
  const runId = crypto.randomUUID();
  const logPath = path.join(logsDir, `${lane}-${runId.slice(0, 8)}.log`);
  const fd = fs.openSync(logPath, "wx", 0o600);
  const args = [fileURLToPath(import.meta.url), ...process.argv.slice(2).filter((a) => a !== "--detach"), "--log-path", logPath, "--run-id", runId];
  const child = spawn(process.execPath, args, { cwd: root, detached: true, stdio: ["ignore", fd, fd] });
  child.unref();
  console.log(JSON.stringify({ lane, runner_pid: child.pid, log_path: logPath, run_id: runId }));
  process.exit(0);
}

// Claim the exclusive lane lock BEFORE starting any model.
fs.mkdirSync(workersDir, { recursive: true });
try { fs.writeFileSync(lockPath(lane), String(process.pid), { flag: "wx", mode: 0o600 }); }
catch { const prev = readState(lane); console.error(JSON.stringify({ error: "lane locked", lane, previous: prev && { status: prev.status, pid: prev.pid, alive: alive(prev.pid) }, hint: "inspect worker state; remove the lock only after confirming the process is gone" })); process.exit(3); }

const state = { lane, status: "starting", pid: process.pid, harness, brief: path.relative(root, brief), breadth: opts.breadth || "standard", run_id: opts["run-id"] || crypto.randomUUID(), session_id: opts.resume || null, started_at: new Date().toISOString(), log_path: opts["log-path"] || null, model: opts.model || null };
publish(state, {});

const preamble = [
  `You are a worker in an orchestrated multi-agent project at ${root}.`,
  `First read ${path.relative(root, brief)} (your bounded brief) and .orchestrator/STATE.md if it exists.`,
  `Check the mailbox for messages addressed to you: node ~/.config/opencode/orchestrator/bin/mailbox.mjs list --to ${lane} --unacked-by ${lane}`,
  `Do only the brief. When done, write your report where the brief says (default .orchestrator/reports/${lane}.md) including: outcome, files changed, exact checks run with results, what is NOT done, open questions.`,
  `Create a source manifest for the files you touched: node ~/.config/opencode/orchestrator/bin/manifest.mjs create --task ${lane} --author ${harness} --out .orchestrator/manifests/${lane}.json <paths>`,
  `Finally post a release message: node ~/.config/opencode/orchestrator/bin/mailbox.mjs post --from ${lane} --to coordinator --kind release --subject "${lane} released" --ref .orchestrator/reports/${lane}.md --ref .orchestrator/manifests/${lane}.json --body "<3-line summary>"`,
  `If blocked, post kind blocked instead and stop. Be honest: unrun checks are "not run", unmet criteria mean "partial". Never approve your own work, never bypass permissions, never touch another lane's files.`,
].join("\n");
const prompt = `${preamble}\n\n--- BRIEF ---\n${fs.readFileSync(brief, "utf8")}`;

const bridge = path.join(HERE, harness === "codex" ? "codex-exec.mjs" : "claude-exec.mjs");
const bridgeArgs = [bridge, "--cwd", root, "--timeout-min", opts["timeout-min"] || "120"];
if (opts.model) bridgeArgs.push("--model", opts.model);
if (opts.effort) bridgeArgs.push("--effort", opts.effort);
if (opts.resume) bridgeArgs.push("--resume", opts.resume);
if (opts.force) bridgeArgs.push("--force");
if (harness === "codex") bridgeArgs.push("--sandbox", opts.sandbox || "workspace-write");
else { bridgeArgs.push("--permission-mode", opts["permission-mode"] || "auto"); if (opts["allowed-tools"]) bridgeArgs.push("--allowed-tools", opts["allowed-tools"]); if (opts["max-turns"]) bridgeArgs.push("--max-turns", opts["max-turns"]); }

let child; let code = 1; let output = "";
const finish = (status, extra = {}) => publish(state, { status, ...extra });
process.on("SIGINT", () => { finish("interrupted"); if (child) try { process.kill(-child.pid, "SIGINT"); } catch { child.kill("SIGINT"); } });
process.on("SIGTERM", () => { finish("interrupted"); if (child) try { process.kill(-child.pid, "SIGTERM"); } catch { child.kill("SIGTERM"); } });
try {
  child = spawn(process.execPath, bridgeArgs, { cwd: root, stdio: ["pipe", "pipe", "pipe"], detached: true });
  child.stdin.end(prompt);
  publish(state, { status: "running", worker_pid: child.pid });
  child.stderr.on("data", (chunk) => {
    const text = String(chunk); process.stdout.write(text);
    const m = text.match(/(thread|session) ([0-9a-f-]{36})/); if (m) publish(state, { session_id: m[2] });
    const t = text.match(/tool: (\w+)|ran: /); publish(state, { last_progress_at: new Date().toISOString(), ...(t && t[1] ? { last_tool: t[1] } : {}) });
  });
  child.stdout.on("data", (c) => { output += c; });
  code = await new Promise((resolve) => child.on("close", resolve));
  let result = null; try { result = JSON.parse(output.trim().split("\n").pop()); } catch { /* bridge crashed before JSON */ }
  if (code === 75 || result?.reason === "budget_red") {
    finish("deferred", { deferred_reason: "budget_red", resume_after: result?.resets_at || null, result_summary: result?.note || "budget gate refused" });
    post(root, { from: lane, to: "coordinator", kind: "deferred", subject: `${lane} deferred: ${harness} budget is red`, body: `Resets ${result?.resets_at || "unknown"}. The supervisor will re-dispatch after reset if auto_redispatch is enabled.`, refs: [] });
  } else if (result?.ok) {
    finish("finished", { session_id: result.session_id || result.thread_id || state.session_id, exit_code: code, result_summary: (result.final_message || "").slice(0, 2000), record: result.record, usage: result.usage || null, cost_usd: result.cost_usd ?? null, permission_denials: result.permission_denials?.length ?? 0 });
  } else {
    finish("failed", { exit_code: code, errors: result?.errors || ["bridge produced no result"], record: result?.record || null, result_summary: (result?.final_message || "").slice(0, 2000) });
    post(root, { from: lane, to: "coordinator", kind: "blocked", subject: `${lane} failed on ${harness}`, body: (result?.errors || ["no result"]).join("; ").slice(0, 1500), refs: result?.record ? [path.relative(root, result.record)] : [] });
  }
} finally {
  try { fs.unlinkSync(lockPath(lane)); } catch { /* already gone */ }
}
console.log(JSON.stringify({ lane, status: state.status, session_id: state.session_id, exit_code: code }));
process.exit(state.status === "finished" || state.status === "deferred" ? 0 : 1);
