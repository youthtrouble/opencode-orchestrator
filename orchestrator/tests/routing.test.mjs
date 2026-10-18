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

test("Fable weekly red: planning leaves Fable; coding follows 5-hour headroom", () => {
  const r = resolveRoles({ claude: claude(40, 90), codex: codex(20, 5) }, registry);
  assert.equal(r.roles.planner.model, "anthropic/claude-opus-5-5");
  assert.equal(r.roles.coder.vendor, "openai"); // 80 vs 60 headroom
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

test("a red session window stays red even if it resets within 20 minutes (only a flag is set)", () => {
  const c = claude(90, 10); c.session.resets_at = soon(5);
  const p = vendorPostures({ claude: c, codex: codex(1, 1) }, registry);
  assert.equal(p.anthropic.windows.session, "red");
  assert.equal(p.anthropic.resets_soon, true);
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

test("frontier tiers need a GREEN frontier posture: amber Fable routes routine planning to Opus", () => {
  const r = resolveRoles({ claude: claude(30, 70), codex: codex(10, 5) }, registry);
  assert.equal(r.roles.planner.model, "anthropic/claude-opus-5-5");
  assert.match(r.reasons.join(" "), /Fable weekly cap is amber/);
});

test("5-hour window steers; an amber weekly does not block or demote a vendor", () => {
  const x = codex(10, 70); // weekly amber
  const r = resolveRoles({ claude: claude(50, 10), codex: x }, registry, { channels: { openai: true } });
  assert.equal(r.roles.coder.vendor, "openai");
  assert.equal(r.postures.openai.overall, "green");
});
test("coder is balanced to the vendor with clearly more 5-hour headroom", () => {
  assert.equal(resolveRoles({ claude: claude(60, 10), codex: codex(10, 10) }, registry).roles.coder.vendor, "openai");
  assert.equal(resolveRoles({ claude: claude(20, 10), codex: codex(25, 10) }, registry).roles.coder.vendor, "anthropic"); // within margin: registry order
  assert.equal(resolveRoles({ claude: claude(10, 10), codex: codex(70, 10) }, registry).roles.coder.vendor, "anthropic");
});
test("a red weekly still blocks the vendor", () => {
  assert.equal(resolveRoles({ claude: claude(10, 10), codex: codex(5, 95) }, registry).roles.reviewer.vendor, "anthropic");
});

test("a preference naming a tier missing from the registry is skipped, not a crash", () => {
  const reg = JSON.parse(JSON.stringify(registry));
  reg.roles.coder.prefer = ["anthropic.nonexistent", "openai.strong", "anthropic.strong"];
  const r = resolveRoles({ claude: claude(10, 10), codex: codex(60, 10) }, reg);
  assert.equal(r.roles.coder.vendor, "anthropic");
});
