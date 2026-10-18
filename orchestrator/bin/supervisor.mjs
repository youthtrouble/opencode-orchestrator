#!/usr/bin/env node
// Watch every worker lane in a project; post durable events to the mailbox and wake the coordinator.
//
//   supervisor.mjs start [--cwd DIR]        detached daemon (one per project; pid lock)
//   supervisor.mjs serve [--cwd DIR]        foreground loop
//   supervisor.mjs status|stop [--cwd DIR]
//   supervisor.mjs ack --event ID [--cwd DIR]
//
// Wake channels are configured in <project>/.orchestrator/supervisor.json:
//   { "wake": [ {"type":"opencode-run","session":"<session-id>"}, {"type":"codex-queue","thread":"<uuid>"},
//               {"type":"claude-resume","session":"<uuid>"}, {"type":"command","argv":["..."]} ],
//     "auto_redispatch_deferred": true }
// Every event is ALWAYS written to the mailbox first (kind "wake"); wake channels are best-effort.
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "../lib/run-record.mjs";
import { observe, chooseEvent, pruneEvents, reminderText } from "../lib/supervise.mjs";
import { post } from "../lib/mailbox.mjs";
import { keepAwakeWrap } from "../lib/keepawake.mjs";
import { record as incident } from "../lib/incident.mjs";
import { loadRegistry } from "../lib/usage.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const { opts, positional } = parseArgs(process.argv.slice(2), { valueKeys: ["cwd", "event", "by"], flagKeys: [] });
const cmd = positional[0];
const root = path.resolve(opts.cwd || process.cwd());
const dir = path.join(root, ".orchestrator");
const workersDir = path.join(dir, "workers");
const CONFIG = path.join(dir, "supervisor.json");
const STATE = path.join(dir, "supervisor-state.json");
const ACKS = path.join(dir, "supervisor-acks.json");
const LOCK = path.join(dir, "supervisor.lock");
const STOP = path.join(dir, "supervisor.stop");
const LOG = path.join(dir, "logs", "supervisor.log");

const readJson = (f, d) => { try { return JSON.parse(fs.readFileSync(f, "utf8")); } catch { return d; } };
const writeJson = (f, v) => { fs.mkdirSync(path.dirname(f), { recursive: true }); const t = f + ".tmp"; fs.writeFileSync(t, JSON.stringify(v, null, 2) + "\n"); fs.renameSync(t, f); };
const nowSec = () => Math.floor(Date.now() / 1000);
const iso = (s) => new Date(s * 1000).toISOString();
const alive = (pid) => { if (!pid) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } };

function claimLock() {
  try { fs.writeFileSync(LOCK, String(process.pid), { flag: "wx" }); return true; }
  catch {
    const pid = Number(readText(LOCK));
    if (alive(pid)) return false;
    fs.writeFileSync(LOCK, String(process.pid)); // stale lock from a dead process
    return true;
  }
}
const readText = (f) => { try { return fs.readFileSync(f, "utf8").trim(); } catch { return ""; } };

function deliver(channel, text) {
  try {
    if (channel.type === "codex-queue") {
      const r = spawnSync("codex", ["queue", "--thread", channel.thread, "--message", text], { cwd: root, timeout: 30000, encoding: "utf8" });
      return r.status === 0 ? { status: "queued" } : r.error?.code === "ETIMEDOUT" ? { status: "delivery_uncertain", error: "timeout" } : { status: "delivery_failed", error: `exit ${r.status}` };
    }
    if (channel.type === "opencode-run") {
      // Steer the coordinator's opencode session without blocking on its reply.
      const child = spawn("opencode", ["run", "--session", channel.session, "--format", "json", text], { cwd: root, detached: true, stdio: "ignore" });
      child.unref();
      return { status: "queued" };
    }
    if (channel.type === "claude-resume") {
      const child = spawn(process.execPath, [path.join(HERE, "claude-exec.mjs"), "--cwd", root, "--resume", channel.session, "--permission-mode", channel.permission_mode || "auto", "--quiet", "--prompt", text], { cwd: root, detached: true, stdio: "ignore" });
      child.unref();
      return { status: "queued" };
    }
    if (channel.type === "command") {
      const r = spawnSync(channel.argv[0], [...channel.argv.slice(1), text], { cwd: root, timeout: 30000, encoding: "utf8" });
      return r.status === 0 ? { status: "queued" } : { status: "delivery_failed", error: `exit ${r.status}` };
    }
    return { status: "delivery_failed", error: `unknown channel ${channel.type}` };
  } catch (err) {
    return { status: "delivery_failed", error: err.code || String(err) };
  }
}

function tickAll(state) {
  const config = readJson(CONFIG, {});
  const acks = readJson(ACKS, {});
  const now = nowSec();
  state.events = pruneEvents(state.events || {}, now);
  state.lanes = state.lanes || {};
  const lanes = fs.existsSync(workersDir) ? fs.readdirSync(workersDir).filter((f) => f.endsWith(".json")).map((f) => f.replace(/\.json$/, "")) : [];
  for (const lane of lanes) {
    const worker = readJson(path.join(workersDir, `${lane}.json`), null);
    if (!worker) continue;
    worker.lane = lane;
    const obs = observe(worker, now, { alive: alive(worker.pid), lockPresent: fs.existsSync(path.join(workersDir, `${lane}.lock`)) });
    const laneState = state.lanes[lane] || {};
    const ev = chooseEvent(obs, { ...laneState, events: state.events }, acks, now);
    const next = laneState.run === obs.run && laneState.next_check ? laneState.next_check : now + obs.interval;
    state.lanes[lane] = { run: obs.run, phase: obs.phase, breadth: obs.breadth, quiet: obs.quiet, checked_at: iso(now), next_check: next, next_check_at: iso(next) };

    // Budget-deferred workers: re-dispatch automatically once the window has reset (opt-in, default on).
    if (obs.phase === "deferred" && config.auto_redispatch_deferred !== false && worker.resume_after && Date.parse(worker.resume_after) + 60000 <= Date.now() && !worker.redispatched_at) {
      const args = [path.join(HERE, "worker.mjs"), "start", "--brief", worker.brief, "--harness", worker.harness, "--lane", lane, "--breadth", worker.breadth || "standard", "--cwd", root, "--detach"];
      if (worker.model) args.push("--model", worker.model);
      if (worker.session_id) args.push("--resume", worker.session_id);
      const r = spawnSync(process.execPath, args, { cwd: root, encoding: "utf8" });
      worker.redispatched_at = new Date().toISOString();
      writeJson(path.join(workersDir, `${lane}.json`), worker);
      post(root, { from: "supervisor", to: "coordinator", kind: "note", subject: `${lane} re-dispatched after budget reset`, body: (r.stdout || r.stderr || "").trim().slice(0, 500) });
      continue;
    }
    if (!ev) continue;

    // Two-phase durable intent (F2): persist intent, then mark cli_invoked right before sending.
    const text = reminderText(ev.id, ev.reason, obs, root);
    state.events[ev.id] = { status: "intent_persisted", reason: ev.reason, lane, created_at: iso(now), retry_after: now + obs.interval };
    writeJson(STATE, state);
    let mailboxId = null;
    try { mailboxId = post(root, { from: "supervisor", to: "coordinator", kind: "wake", subject: `${lane}: ${ev.reason}`, body: text, thread: ev.id }).id; } catch { /* mailbox write failed; channels may still work */ }
    state.events[ev.id].status = "cli_invoked";
    state.events[ev.id].mailbox_id = mailboxId;
    writeJson(STATE, state);
    const results = (config.wake || []).map((ch) => ({ type: ch.type, ...deliver(ch, text) }));
    const anyQueued = results.some((r) => r.status === "queued");
    const anyUncertain = results.some((r) => r.status === "delivery_uncertain");
    state.events[ev.id].status = anyQueued || mailboxId ? "queued" : anyUncertain ? "delivery_uncertain" : "delivery_failed";
    state.events[ev.id].channels = results;
    state.lanes[lane].next_check = now + obs.interval;
    state.lanes[lane].next_check_at = iso(now + obs.interval);
    fs.appendFileSync(LOG, `${iso(now)} ${ev.id} ${state.events[ev.id].status} ${JSON.stringify(results)}\n`);
    try {
      if (state.events[ev.id].status === "delivery_failed") incident({ component: "supervisor", kind: "bug", summary: `wake delivery failed for ${ev.reason}`, detail: JSON.stringify(results), project: root });
      if (ev.reason === "stale_lock" || ev.reason === "process_exited") incident({ component: "supervisor", kind: "crash", summary: `worker lane ${lane}: ${ev.reason}`, project: root });
    } catch { /* never block */ }
  }
  // Conductor flip / hand-back: once a minute, when the project has a conductor lane or has flipped before.
  const flipFile = path.join(dir, "flip.json");
  const conductorLane = readJson(path.join(workersDir, "conductor.json"), null);
  if ((conductorLane || fs.existsSync(flipFile)) && now - (state.last_flip_check || 0) >= 60) {
    state.last_flip_check = now;
    const flip = readJson(flipFile, null);
    const activeDoor = conductorLane && !["finished", "failed", "interrupted", "deferred"].includes(conductorLane.status) && alive(conductorLane.pid)
      ? (conductorLane.harness === "claude" ? "claude" : "opencode") : (flip?.to || (config.home || "opencode"));
    const idle = !conductorLane || !alive(conductorLane.pid);
    const lockFree = !fs.existsSync(path.join(workersDir, "conductor.lock"));
    if (flip?.awaiting_human) {
      if (!state.awaiting_human_logged) { fs.appendFileSync(LOG, `${iso(now)} conductor awaiting human: ${flip.awaiting_human.reason}; no relaunch until cleared\n`); state.awaiting_human_logged = true; }
    } else if (state.awaiting_human_logged) { state.awaiting_human_logged = false; }
    if (flip?.awaiting_human) { /* human gate: do nothing */ }
    else if (idle && lockFree && flip?.pending_launch) {
      // A hand-off recorded while the previous conductor still held the lane: launch the target now.
      const pl = flip.pending_launch;
      const args = [path.join(HERE, "flip.mjs"), "run", "--to", pl.to, "--from", pl.from || activeDoor, "--reason", pl.reason || "pending launch", "--cwd", root];
      if (pl.resume) args.push("--resume", pl.resume);
      const r = spawnSync(process.execPath, args, { cwd: root, encoding: "utf8" });
      fs.appendFileSync(LOG, `${iso(now)} pending launch ${pl.from}->${pl.to}: ${(r.stdout || r.stderr || "").trim().slice(0, 200)}\n`);
      state.last_relaunch_at = now;
    } else if (idle) {
      const d = spawnSync(process.execPath, [path.join(HERE, "flip.mjs"), "decide", "--active", activeDoor, "--cwd", root], { encoding: "utf8" });
      let decision = null; try { decision = JSON.parse(d.stdout.trim().split("\n").pop()); } catch { /* usage unavailable */ }
      // Relaunch rules for an idle conductor: flip/handback launch the target door; a paused project
      // (both vendors were red) relaunches its own door as soon as decide no longer says pause.
      const paused = Boolean(flip?.paused);
      const handedOff = conductorLane && fs.existsSync(path.join(dir, "mailbox", "messages.jsonl")) && fs.readFileSync(path.join(dir, "mailbox", "messages.jsonl"), "utf8").split("\n").some((l) => { try { const m = JSON.parse(l); return m.kind === "handoff" && m.at >= conductorLane.started_at; } catch { return false; } });
      const finishedEarly = conductorLane && conductorLane.status === "finished" && !handedOff && !conductorLane.redispatched_at;
      const laneNeedsRestart = conductorLane && (["failed", "deferred", "interrupted"].includes(conductorLane.status) || finishedEarly) && !conductorLane.redispatched_at;
      const cooled = now - (state.last_relaunch_at || 0) >= 600; // one relaunch attempt per 10 minutes
      let target = null;
      if (decision && (decision.action === "flip" || decision.action === "handback")) target = decision.to;
      else if (decision && decision.action === "stay" && (paused || laneNeedsRestart)) target = activeDoor;
      if (target && !cooled) target = null;
      if (target) {
        state.last_relaunch_at = now;
        const resumeArgs = target === activeDoor && conductorLane?.session_id && (conductorLane.harness === "claude") === (target === "claude") ? ["--resume", conductorLane.session_id] : [];
        if (conductorLane) { conductorLane.redispatched_at = new Date().toISOString(); writeJson(path.join(workersDir, "conductor.json"), conductorLane); }
        const r = spawnSync(process.execPath, [path.join(HERE, "flip.mjs"), "run", "--to", target, "--from", activeDoor, "--reason", `${decision.action}: ${decision.reason}${finishedEarly ? " (previous conductor exited without handoff)" : ""}`, "--cwd", root, ...resumeArgs], { encoding: "utf8" });
        try { incident({ component: "flip", kind: "flip", summary: `conductor ${decision.action} ${activeDoor}->${target}`, detail: decision.reason, project: root }); } catch { /* never block */ }
        fs.appendFileSync(LOG, `${iso(now)} relaunch ${activeDoor}->${target} (${decision.action}${paused ? ", was paused" : ""}): ${(r.stdout || r.stderr || "").trim().slice(0, 200)}\n`);
      }
    }
  }
  state.checked_at = iso(now);
  state.pid = process.pid;
  state.status = "running";
  return state;
}

if (cmd === "status") { console.log(JSON.stringify(readJson(STATE, { status: "not_started" }), null, 2)); process.exit(0); }
if (cmd === "stop") { fs.mkdirSync(dir, { recursive: true }); fs.writeFileSync(STOP, ""); console.log("stop requested; workers are unaffected"); process.exit(0); }
if (cmd === "ack") {
  const state = readJson(STATE, {});
  if (!opts.event || !state.events?.[opts.event]) { console.error("unknown event id"); process.exit(1); }
  const acks = readJson(ACKS, {}); acks[opts.event] = { by: opts.by || "coordinator", at: new Date().toISOString() }; writeJson(ACKS, acks);
  console.log("acknowledged (receipt, not acceptance)"); process.exit(0);
}
if (cmd === "start") {
  fs.mkdirSync(path.dirname(LOG), { recursive: true });
  if (alive(Number(readText(LOCK)))) { console.log(JSON.stringify({ started: false, reason: "already running", pid: Number(readText(LOCK)) })); process.exit(0); }
  try { fs.unlinkSync(STOP); } catch { /* none */ }
  const fd = fs.openSync(LOG, "a");
  let keepAwake = true; try { keepAwake = loadRegistry().runtime?.keep_awake !== false; } catch { /* default on */ }
  const wrapped = keepAwakeWrap(process.execPath, [fileURLToPath(import.meta.url), "serve", "--cwd", root], { enabled: keepAwake });
  const child = spawn(wrapped.cmd, wrapped.args, { cwd: root, detached: true, stdio: ["ignore", fd, fd] });
  child.unref();
  console.log(JSON.stringify({ started: true, supervisor_pid: child.pid, log: LOG }));
  process.exit(0);
}
if (cmd === "serve") {
  fs.mkdirSync(dir, { recursive: true });
  if (!claimLock()) { console.error("a supervisor already owns this project"); process.exit(1); }
  let state = readJson(STATE, {});
  const loop = () => {
    if (fs.existsSync(STOP)) { state.status = "stopped"; state.stopped_at = new Date().toISOString(); writeJson(STATE, state); try { fs.unlinkSync(LOCK); } catch { /* none */ } process.exit(0); }
    try { state = tickAll(state); writeJson(STATE, state); delete state.storage_error; }
    catch (err) { state.storage_error = err.code || String(err); state.storage_failures = (state.storage_failures || 0) + 1; }
    setTimeout(loop, 5000);
  };
  loop();
} else if (!cmd) { console.error("commands: start | serve | status | stop | ack --event ID"); process.exit(2); }
