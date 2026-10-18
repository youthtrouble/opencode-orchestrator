// Pure supervision policy (no I/O), so it can be unit-tested with an injected clock.
// Generalized from next-workspace/collaboration/claude-supervisor.py with the O-01 findings fixed:
//   F1: a definite delivery failure no longer suppresses later checks.
//   F2: two-phase intent (intent_persisted -> cli_invoked -> outcome) so a crash before the CLI call is safely retried.
//   F4: quiet workers re-alert with back-off instead of exactly once.
//   F8: events are pruned.
export const CADENCE = { focused: 120, standard: 300, broad: 600 };
export const TERMINAL = new Set(["finished", "failed", "interrupted", "deferred"]);
export const QUIET_REALERT_SECONDS = 1800;
export const EVENT_TTL_SECONDS = 7 * 24 * 3600;

export function observe(worker, nowSec, { alive, lockPresent }) {
  const breadth = CADENCE[worker.breadth] ? worker.breadth : "standard";
  const interval = CADENCE[breadth];
  const progress = toSec(worker.last_progress_at) ?? toSec(worker.updated_at) ?? toSec(worker.started_at) ?? nowSec;
  const status = worker.status || "unknown";
  let phase;
  if (TERMINAL.has(status)) phase = alive ? "finishing" : lockPresent ? "stale_lock" : status;
  else if (status === "running" || status === "starting") phase = alive ? "running" : "process_exited";
  else phase = "unknown";
  const quietFor = nowSec - progress;
  return { run: worker.run_id || `${worker.pid}:${worker.lane}`, lane: worker.lane, phase, breadth, interval, progress_at: progress, quiet: phase === "running" && quietFor >= Math.max(600, interval * 3), quiet_for: quietFor };
}

function toSec(iso) { if (!iso) return null; const t = Date.parse(iso); return Number.isNaN(t) ? null : Math.floor(t / 1000); }

// Decide whether to emit an event now. Returns { id, reason } or null.
export function chooseEvent(obs, state, acks, nowSec) {
  const events = state.events || {};
  const run = obs.run;
  const retryable = (ev) => ev.status === "intent_persisted" || (ev.status === "delivery_failed" && nowSec >= (ev.retry_after ?? Infinity));
  const notAcked = (id) => !acks[id];

  if (TERMINAL.has(obs.phase) || obs.phase === "process_exited" || obs.phase === "stale_lock") {
    const id = `${run}:${obs.phase}`;
    if (!events[id] || (notAcked(id) && retryable(events[id]))) return { id, reason: obs.phase };
    return null;
  }
  if (obs.phase !== "running") return null;

  // Re-arm an intent that never reached the CLI (crash between persist and send).
  for (const [id, ev] of Object.entries(events)) if (id.startsWith(run + ":") && notAcked(id) && ev.status === "intent_persisted") return { id, reason: ev.reason };

  // At most one unacknowledged *delivered* progress reminder per run (failed deliveries don't count: F1).
  const pending = Object.entries(events).some(([id, ev]) => id.startsWith(run + ":") && notAcked(id) && ["queued", "delivery_uncertain", "cli_invoked"].includes(ev.status));
  if (pending) return null;

  if (obs.quiet) {
    const bucket = Math.floor(obs.quiet_for / QUIET_REALERT_SECONDS); // F4: re-alert every 30 min of continued quiet
    const id = `${run}:quiet:${obs.progress_at}:${bucket}`;
    if (!events[id] || (notAcked(id) && retryable(events[id]))) return { id, reason: "quiet" };
    return null;
  }
  const sameRun = state.run === run;
  const due = sameRun && state.next_check ? state.next_check : nowSec + obs.interval;
  if (nowSec >= due) return { id: `${run}:check:${due}`, reason: "check" };
  return null;
}

export function pruneEvents(events, nowSec) {
  const out = {};
  for (const [id, ev] of Object.entries(events || {})) {
    const created = toSec(ev.created_at) ?? nowSec;
    if (nowSec - created <= EVENT_TTL_SECONDS) out[id] = ev;
  }
  return out;
}

export function reminderText(id, reason, obs, projectRoot) {
  return [
    `Orchestrator supervisor event ${id}: worker lane "${obs.lane}" is ${obs.phase} (${reason}; ${obs.breadth} cadence).`,
    `Read ${projectRoot}/.orchestrator/workers/${obs.lane}.json, the mailbox (node ~/.config/opencode/orchestrator/bin/mailbox.mjs list --unacked-by coordinator) and any released report.`,
    `Acknowledge receipt with: node ~/.config/opencode/orchestrator/bin/supervisor.mjs ack --event ${id}. Receipt is not acceptance.`,
    `Then act: review released work independently and send findings back to the author via the mailbox, or dispatch the next bounded brief. Never duplicate a running worker, approve your own code, or bypass permissions. A quiet worker may be thinking; inspect evidence before calling it stuck.`,
  ].join(" ");
}
