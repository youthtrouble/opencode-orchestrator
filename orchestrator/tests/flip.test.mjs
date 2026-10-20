import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { decideFlip } from "../lib/flip.mjs";

const flipCli = fileURLToPath(new URL("../bin/flip.mjs", import.meta.url));

const registry = { conductor: { home: "opencode", green_grace_minutes: 10, flip_cooldown_minutes: 10 } };
const now = Date.parse("2026-10-18T12:00:00Z");
const P = (openai, anthropic, { openaiResetInMin = 120, anthropicResetInMin = 120 } = {}) => ({
  openai: { available: true, overall: openai, detail: { five_hour: { resets_at: new Date(now + openaiResetInMin * 60000).toISOString(), window_mins: 300 } } },
  anthropic: { available: true, overall: anthropic, detail: { session: { resets_at: new Date(now + anthropicResetInMin * 60000).toISOString() } } },
});

test("own vendor red and other usable -> forced flip", () => {
  assert.equal(decideFlip({ active: "opencode", postures: P("red", "green"), registry, nowMs: now }).action, "flip");
  assert.equal(decideFlip({ active: "claude", postures: P("amber", "red"), registry, nowMs: now }).to, "opencode");
});
test("both red -> pause", () => {
  assert.equal(decideFlip({ active: "opencode", postures: P("red", "red"), registry, nowMs: now }).action, "pause");
});
test("away conductor hands back when home is stably green", () => {
  const d = decideFlip({ active: "claude", postures: P("green", "green"), registry, nowMs: now });
  assert.deepEqual([d.action, d.to], ["handback", "opencode"]);
});
test("home just reset (<grace) is not stable; amber home is not enough", () => {
  assert.equal(decideFlip({ active: "claude", postures: P("green", "green", { openaiResetInMin: 295 }), registry, nowMs: now }).action, "stay");
  assert.equal(decideFlip({ active: "claude", postures: P("amber", "green"), registry, nowMs: now }).action, "stay");
});
test("cooldown suppresses a hand-back but never a forced flip", () => {
  const recent = now - 2 * 60000;
  assert.equal(decideFlip({ active: "claude", postures: P("green", "green"), registry, nowMs: now, lastFlipAtMs: recent }).action, "stay");
  assert.equal(decideFlip({ active: "claude", postures: P("green", "red"), registry, nowMs: now, lastFlipAtMs: recent }).action, "flip");
});
test("home conductor with headroom stays", () => {
  assert.equal(decideFlip({ active: "opencode", postures: P("amber", "green"), registry, nowMs: now }).action, "stay");
});

function tempProject(t) {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "flip-gate-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  return project;
}
function flip(project, ...args) {
  const r = spawnSync(process.execPath, [flipCli, ...args, "--cwd", project], { encoding: "utf8" });
  assert.equal(r.status, 0, r.stderr);
  return r.stdout.trim();
}
const readFlipJson = (project) => JSON.parse(fs.readFileSync(path.join(project, ".orchestrator", "flip.json"), "utf8"));

test("gate --clear removes the gate and queues a relaunch of the gated door", (t) => {
  const project = tempProject(t);
  flip(project, "gate", "--active", "claude", "--reason", "push");

  assert.equal(flip(project, "gate", "--clear"), '{"awaiting_human":false,"relaunch":"claude"}');

  const record = readFlipJson(project);
  assert.equal(record.awaiting_human, undefined);
  assert.deepEqual({ ...record.pending_launch, at: "<iso>" }, { to: "claude", from: "claude", reason: "human gate cleared", resume: null, at: "<iso>" });
  assert.deepEqual(fs.readdirSync(path.join(project, ".orchestrator")).filter((f) => f.startsWith("flip.json")), ["flip.json"]);
});

test("gate --clear keeps a pending launch queued before the gate", (t) => {
  const project = tempProject(t);
  const queued = { to: "opencode", from: "claude", reason: "handback", resume: null, at: "2026-01-01T00:00:00.000Z" };
  fs.mkdirSync(path.join(project, ".orchestrator"));
  fs.writeFileSync(path.join(project, ".orchestrator", "flip.json"), JSON.stringify({ pending_launch: queued }));
  flip(project, "gate", "--active", "claude");

  assert.equal(flip(project, "gate", "--clear"), '{"awaiting_human":false,"relaunch":"opencode"}');

  assert.deepEqual(readFlipJson(project), { pending_launch: queued });
});

test("gate --clear without a gate queues nothing", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.join(project, ".orchestrator"));
  fs.writeFileSync(path.join(project, ".orchestrator", "flip.json"), '{"to":"claude"}');

  assert.equal(flip(project, "gate", "--clear"), '{"awaiting_human":false}');

  assert.deepEqual(readFlipJson(project), { to: "claude" });
});

test("gate --clear --no-relaunch (kickoff) clears without queueing a relaunch", (t) => {
  const project = tempProject(t);
  flip(project, "gate");

  assert.equal(flip(project, "gate", "--clear", "--no-relaunch"), '{"awaiting_human":false}');

  assert.deepEqual(readFlipJson(project), {});
});
