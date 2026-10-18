import test from "node:test";
import assert from "node:assert/strict";
import { observe, chooseEvent, pruneEvents, CADENCE, QUIET_REALERT_SECONDS } from "../lib/supervise.mjs";

const T0 = 1_800_000_000;
const isoAt = (s) => new Date(s * 1000).toISOString();
const running = (over = {}) => ({ lane: "B-01", run_id: "run1", status: "running", pid: 1, breadth: "standard", started_at: isoAt(T0), last_progress_at: isoAt(T0), ...over });

test("first observation of a run never prompts; a check is due after one cadence", () => {
  const obs = observe(running(), T0 + 1, { alive: true, lockPresent: true });
  assert.equal(obs.phase, "running");
  assert.equal(chooseEvent(obs, {}, {}, T0 + 1), null);
  const state = { run: "run1", next_check: T0 + CADENCE.standard };
  assert.deepEqual(chooseEvent(obs, state, {}, T0 + CADENCE.standard), { id: `run1:check:${T0 + CADENCE.standard}`, reason: "check" });
});

test("terminal phases prompt exactly once unless the event is retryable", () => {
  const obs = observe(running({ status: "finished" }), T0 + 10, { alive: false, lockPresent: false });
  assert.equal(obs.phase, "finished");
  assert.deepEqual(chooseEvent(obs, {}, {}, T0 + 10), { id: "run1:finished", reason: "finished" });
  assert.equal(chooseEvent(obs, { events: { "run1:finished": { status: "queued" } } }, {}, T0 + 10), null);
  assert.deepEqual(chooseEvent(obs, { events: { "run1:finished": { status: "intent_persisted" } } }, {}, T0 + 10), { id: "run1:finished", reason: "finished" });
});

test("F1: a definite delivery failure does not suppress the next due check", () => {
  const obs = observe(running(), T0 + 2 * CADENCE.standard, { alive: true, lockPresent: true });
  const state = { run: "run1", next_check: T0 + CADENCE.standard, events: { [`run1:check:${T0 + CADENCE.standard}`]: { status: "delivery_failed", retry_after: T0 + 2 * CADENCE.standard } } };
  const ev = chooseEvent(obs, state, {}, T0 + 2 * CADENCE.standard);
  assert.ok(ev, "a check should be emitted");
});

test("a delivered-but-unacknowledged progress reminder suppresses further checks", () => {
  const obs = observe(running(), T0 + 2 * CADENCE.standard, { alive: true, lockPresent: true });
  const state = { run: "run1", next_check: T0 + CADENCE.standard, events: { [`run1:check:${T0 + CADENCE.standard}`]: { status: "queued" } } };
  assert.equal(chooseEvent(obs, state, {}, T0 + 2 * CADENCE.standard), null);
});

test("F4: quiet workers re-alert every 30 minutes with distinct ids", () => {
  const w = running();
  const q1 = observe(w, T0 + 1000, { alive: true, lockPresent: true });
  assert.equal(q1.quiet, true);
  const e1 = chooseEvent(q1, { run: "run1" }, {}, T0 + 1000);
  assert.equal(e1.reason, "quiet");
  const q2 = observe(w, T0 + 1000 + QUIET_REALERT_SECONDS, { alive: true, lockPresent: true });
  const e2 = chooseEvent(q2, { run: "run1", events: { [e1.id]: { status: "queued" } } }, { [e1.id]: true }, T0 + 1000 + QUIET_REALERT_SECONDS);
  assert.ok(e2 && e2.id !== e1.id);
});

test("dead process while status says running -> process_exited; terminal with lock -> stale_lock", () => {
  assert.equal(observe(running(), T0 + 5, { alive: false, lockPresent: true }).phase, "process_exited");
  assert.equal(observe(running({ status: "failed" }), T0 + 5, { alive: false, lockPresent: true }).phase, "stale_lock");
});

test("F8: events older than the TTL are pruned", () => {
  const events = { old: { created_at: isoAt(T0 - 10 * 24 * 3600) }, fresh: { created_at: isoAt(T0) } };
  assert.deepEqual(Object.keys(pruneEvents(events, T0)), ["fresh"]);
});
