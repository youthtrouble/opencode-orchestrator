import test from "node:test";
import assert from "node:assert/strict";
import { decideFlip } from "../lib/flip.mjs";

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
