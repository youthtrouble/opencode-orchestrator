import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { resolveRoles, vendorPostures } from "../lib/routing.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../models.json", import.meta.url), "utf8"));
const soon = (mins) => new Date(Date.now() + mins * 60000).toISOString();
const claude = (session, fable, extra = {}) => ({ ok: true, plan: "team", session: { pct: session, resets_at: soon(120) }, weekly: { pct: null, resets_at: null }, weekly_by_model: fable === null ? {} : { fable: { pct: fable, resets_at: soon(600) } }, ...extra });
const codex = (five, weekly) => ({ ok: true, plan: "plus", five_hour: { pct: five, resets_at: soon(120) }, weekly: { pct: weekly, resets_at: soon(5000) } });

test("green everywhere: Fable plans, Opus codes, OpenAI reviews (cross-vendor)", () => {
  const r = resolveRoles({ claude: claude(20, 30), codex: codex(20, 5) }, registry, { channels: { openai: true } });
  assert.equal(r.roles.planner.model, "anthropic/claude-fable-5-1");
  assert.equal(r.roles.coder.model, "anthropic/claude-opus-5-5");
  assert.equal(r.roles.reviewer.vendor, "openai");
  assert.equal(r.roles.reviewer.cross_vendor, true);
  assert.equal(r.roles.coder.via, "claude-cli");
  assert.equal(r.roles.reviewer.via, "opencode");
});

test("Fable weekly red: planning leaves Fable but Opus still codes", () => {
  const r = resolveRoles({ claude: claude(40, 90), codex: codex(20, 5) }, registry);
  assert.equal(r.roles.planner.model, "openai/gpt-6-astra");
  assert.equal(r.roles.coder.model, "anthropic/claude-opus-5-5");
  assert.match(r.reasons.join(" "), /Fable weekly cap is red/);
});

test("Claude session red: everything Anthropic fails over to OpenAI, review flips to Anthropic? no: cross vendor unavailable -> same vendor cheap", () => {
  const r = resolveRoles({ claude: claude(95, 50), codex: codex(20, 5) }, registry);
  assert.equal(r.roles.coder.vendor, "openai");
  assert.equal(r.roles.reviewer.vendor, "openai");
  assert.equal(r.roles.reviewer.cross_vendor, false);
});

test("Codex red: OpenAI roles fail over to Anthropic", () => {
  const r = resolveRoles({ claude: claude(10, 10), codex: codex(99, 40) }, registry);
  assert.equal(r.roles.reviewer.vendor, "anthropic");
  assert.equal(r.roles.reviewer.model, "anthropic/claude-sonnet-5-5");
  assert.equal(r.roles.reviewer.cross_vendor, false);
});

test("both red: paused", () => {
  const r = resolveRoles({ claude: claude(99, 99), codex: codex(99, 99) }, registry);
  assert.equal(r.paused, true);
});

test("a red session window that resets within 20 minutes is treated as amber", () => {
  const c = claude(90, 10); c.session.resets_at = soon(5);
  const p = vendorPostures({ claude: c, codex: codex(1, 1) }, registry);
  assert.equal(p.anthropic.windows.session, "amber");
});

test("unknown Claude usage does not crash and routes to OpenAI", () => {
  const r = resolveRoles({ claude: { ok: false, error: "no token" }, codex: codex(10, 1) }, registry);
  assert.equal(r.roles.coder.vendor, "openai");
  assert.match(r.reasons.join(" "), /Claude usage unknown/);
});

test("effort scales with task size and defaults to medium-size policy", async () => {
  const { effortFor } = await import("../lib/routing.mjs");
  assert.equal(effortFor(registry, "coder", "trivial"), "low");
  assert.equal(effortFor(registry, "coder", "hard"), "xhigh");
  assert.equal(effortFor(registry, "planner", "hard"), "max");
  assert.equal(effortFor(registry, "reviewer", undefined), "medium");
  assert.equal(effortFor(registry, "verifier", "large"), "medium");
});
