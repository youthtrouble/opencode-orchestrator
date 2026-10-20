#!/usr/bin/env node
// Conductor flip: decide and execute moving the project's conductor between the two front doors.
//   flip.mjs decide --active opencode|claude [--cwd DIR]        -> JSON {action: flip|handback|stay|pause, to, reason}
//   flip.mjs run --to opencode|claude [--cwd DIR] [--from X] [--reason TEXT]
//   flip.mjs pause [--active opencode|claude] [--reason TEXT] [--brief FILE] [--cwd DIR]
//   flip.mjs status [--cwd DIR]
// `run` posts a handoff to the mailbox, records .orchestrator/flip.json, and starts the other door's conductor
// headless as worker lane "conductor" (lane lock => one driver per project). Adds .orchestrator/ to the repository's info/exclude.
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { parseArgs } from "../lib/run-record.mjs";
import { getRouting } from "../lib/usage.mjs";
import { clearGate, decideFlip, ensureGitExclude, otherDoor, writeFlip } from "../lib/flip.mjs";
import { post } from "../lib/mailbox.mjs";
import { requireProject } from "../lib/project.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const { opts, positional } = parseArgs(process.argv.slice(2), { valueKeys: ["active", "to", "from", "cwd", "reason", "resume", "question", "brief"], flagKeys: ["force", "clear", "no-relaunch"] });
const cmd = positional[0];
const root = path.resolve(opts.cwd || process.cwd());
const FLIP = path.join(root, ".orchestrator", "flip.json");
const readFlip = () => { try { return JSON.parse(fs.readFileSync(FLIP, "utf8")); } catch { return null; } };
// Reporting or clearing a gate needs an existing project; setting one (and run, pause) may create it.
if (cmd === "status" || (cmd === "gate" && opts.clear)) requireProject(root, "--cwd");

if (cmd === "pause") {
  // A conductor that stopped because both vendors are red records the pause; the supervisor relaunches
  // the right door automatically as soon as either window is usable again. --brief (a both-red kickoff)
  // names the brief that relaunch starts from instead of the generic continue brief.
  const active = opts.active || "opencode";
  fs.mkdirSync(path.dirname(FLIP), { recursive: true });
  const prev = readFlip() || {};
  const paused = { at: new Date().toISOString(), door: active, reason: opts.reason || "both vendors red" };
  if (typeof opts.brief === "string" && opts.brief) paused.brief = opts.brief;
  writeFlip(root, { ...prev, paused });
  try { post(root, { from: `conductor-${active}`, to: "supervisor", kind: "handoff", subject: "conductor paused: both vendors red", body: "Supervisor will relaunch when a window resets.", refs: [".orchestrator/STATE.md"] }); } catch { /* optional */ }
  console.log(JSON.stringify({ paused: true, door: active }));
  process.exit(0);
}
if (cmd === "gate") {
  // A headless conductor stopped at a HUMAN gate (push, deploy, spend, product question). Unlike `pause`
  // (budget), the supervisor must NOT relaunch until a human clears it (`gate --clear`, a mailbox reply
  // to the gate's question, kickoff, or `run`). Clearing queues the relaunch; kickoff passes
  // --no-relaunch because it launches the conductor itself, which also drops any queued launch or pause.
  let prev = readFlip() || {};
  fs.mkdirSync(path.dirname(FLIP), { recursive: true });
  if (opts.clear) {
    const door = clearGate(root, prev, { relaunch: !opts["no-relaunch"] });
    console.log(JSON.stringify(door ? { awaiting_human: false, relaunch: door } : { awaiting_human: false }));
    process.exit(0);
  }
  const active = opts.active || "opencode";
  if (Array.isArray(prev)) prev = {}; // an array would drop the gate when stringified
  prev.awaiting_human = { at: new Date().toISOString(), door: active, reason: opts.reason || "human approval required", question: opts.question || null };
  delete prev.paused;
  writeFlip(root, prev);
  try { post(root, { from: `conductor-${active}`, to: "human", kind: "handoff", subject: `awaiting human: ${(opts.reason || "approval").slice(0, 80)}`, body: "Supervisor will not relaunch until cleared: node ~/.config/opencode/orchestrator/bin/flip.mjs gate --clear (or a reply to the gate's question, or kickoff/flip run).", refs: [".orchestrator/STATE.md"] }); } catch { /* optional */ }
  console.log(JSON.stringify({ awaiting_human: true, door: active }));
  process.exit(0);
}
if (cmd === "status") { console.log(JSON.stringify(readFlip() || { state: "never flipped" }, null, 2)); process.exit(0); }
if (cmd === "decide") {
  if (!opts.active) { console.error("--active opencode|claude required"); process.exit(2); }
  const r = await getRouting({});
  const prev = readFlip();
  const d = decideFlip({ active: opts.active, postures: r.resolved.postures, registry: r.registry, lastFlipAtMs: prev?.at ? Date.parse(prev.at) : null });
  console.log(JSON.stringify(d));
  process.exit(0);
}
if (cmd === "run") {
  const to = opts.to; if (!["opencode", "claude"].includes(to)) { console.error("--to opencode|claude required"); process.exit(2); }
  const from = opts.from || otherDoor(to);
  const prev = readFlip();
  ensureGitExclude(root);
  // A brief a kickoff saved (named by its pause, or by a launch the lane lock left pending) is
  // launched instead of the generic one, and kept in a pending launch until a launch succeeds.
  const kickoffBrief = savedBrief(prev);
  const brief = kickoffBrief ?? writeContinueBrief(to, from);
  const record = { at: new Date().toISOString(), from, to, reason: opts.reason || null, previous: prev ? { at: prev.at, to: prev.to } : null };
  writeFlip(root, record); // clears paused and awaiting_human markers
  try { post(root, { from: `conductor-${from}`, to: `conductor-${to}`, kind: "handoff", subject: `conductor flip ${from} -> ${to}`, body: opts.reason || "vendor budget flip", refs: [".orchestrator/STATE.md", brief] }); } catch { /* mailbox optional */ }
  const args = [path.join(HERE, "worker.mjs"), "start", "--brief", brief, "--lane", "conductor", "--harness", to, "--agent", "conductor", "--breadth", "broad", "--cwd", root, "--detach"];
  if (to === "claude") args.push("--permission-mode", "auto");
  if (opts.resume) args.push("--resume", opts.resume);
  if (opts.force) args.push("--force");
  const r = spawnSync(process.execPath, args, { cwd: root, encoding: "utf8" });
  const out = (r.stdout || r.stderr || "").trim();
  if (r.status !== 0 && /lane locked/.test(out)) {
    // The caller is usually the active conductor itself, still holding the lane lock. Record a pending
    // launch; the supervisor starts the target door as soon as the lock is released.
    const pendingLaunch = { to, from, reason: opts.reason || null, resume: opts.resume || null, at: new Date().toISOString() };
    if (kickoffBrief) pendingLaunch.brief = kickoffBrief;
    writeFlip(root, { ...record, pending_launch: pendingLaunch });
    console.log(JSON.stringify({ flipped: false, pending: true, to, from, note: "lane locked by the current conductor; supervisor will launch after release" }));
    process.exit(0);
  }
  console.log(JSON.stringify({ flipped: r.status === 0, to, from, launcher: out.slice(0, 400) }));
  process.exit(r.status === 0 ? 0 : 1);
}
console.error("commands: decide | run | pause | gate [--clear [--no-relaunch]] | status"); process.exit(2);

// The brief a kickoff saved: the one its pause names (`flip pause --brief`), else the one a pending
// launch kept, or null. Only an object record has one, and a brief whose file is gone falls back to
// the generic brief so the relaunch still starts a conductor.
function savedBrief(prev) {
  for (const brief of [prev?.paused?.brief, prev?.pending_launch?.brief]) {
    if (typeof brief === "string" && brief && fs.existsSync(path.resolve(root, brief))) return brief;
  }
  return null;
}

// Writes the generic brief a flipped-to conductor starts from and returns its path relative to the project.
function writeContinueBrief(to, from) {
  const briefsDir = path.join(root, ".orchestrator", "briefs"); fs.mkdirSync(briefsDir, { recursive: true });
  const brief = path.join(briefsDir, "conductor-continue.md");
  fs.writeFileSync(brief, `# Continue as conductor (${to} door)\n\nYou are taking over this project from the ${from} conductor (${opts.reason || "vendor budget flip"}).\nRead .orchestrator/STATE.md and the mailbox, verify recorded facts against the tree, then execute the exact next action and keep following the loop.\nAt each brief boundary run: node ~/.config/opencode/orchestrator/bin/flip.mjs decide --active ${to}\nIf it says handback or flip: update STATE.md, post a handoff, run flip.mjs run --to <door> --from ${to}, and stop.\nYou cannot ask the human interactively: put questions in the mailbox (kind question) and continue with independent work, or stop at human gates.\nHEADLESS RULE: ending your turn ends this session and kills background subagents. Run every subagent in the FOREGROUND and wait; any background agents from a previous headless session are gone, re-run them in the foreground. Do not finish until the loop is complete or you have handed off with flip.mjs run/pause, or stopped at a human gate with flip.mjs gate (never pause for a human gate).\n`);
  return path.relative(root, brief);
}
