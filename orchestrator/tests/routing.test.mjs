import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { creditsPolicyOf, creditsText, openaiCredits, resolveRoles, summarizeForPrompt, vendorPostures, worst } from "../lib/routing.mjs";
import { normalizeCodex } from "../lib/usage.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../models.json", import.meta.url), "utf8"));
const soon = (mins) => new Date(Date.now() + mins * 60000).toISOString();
const claude = (session, fable, extra = {}) => ({ ok: true, plan: "team", session: { pct: session, resets_at: soon(120) }, weekly: { pct: null, resets_at: null }, weekly_by_model: fable === null ? {} : { fable: { pct: fable, resets_at: soon(600) } }, ...extra });
const codex = (five, weekly) => ({ ok: true, plan: "plus", five_hour: { pct: five, resets_at: soon(120) }, weekly: { pct: weekly, resets_at: soon(5000) } });

test("weekly-only Codex alone steers posture and routing", () => {
  const limits = JSON.parse(fs.readFileSync(new URL("../../go/testdata/usage/codex-ratelimits-weekly-only.json", import.meta.url), "utf8"));
  for (const [pct, level] of [[10, "green"], [registry.thresholds.amber, "amber"], [registry.thresholds.red, "red"], [null, "unknown"]]) {
    const x = normalizeCodex({ limits });
    x.weekly.pct = pct;
    const usage = { claude: claude(10, 10), codex: x };
    const r = resolveRoles(usage, registry);
    const p = r.postures.openai;
    assert.deepEqual(p.windows, { five_hour: "n/a", weekly: level });
    assert.equal(p.overall, level);
    assert.equal(p.frontier, level);
    assert.equal(p.headroom, pct === null ? null : 100 - pct);
    assert.equal(p.resets_soon, false);
    assert.doesNotMatch(r.reasons.join(" "), /not steering/);
    if (level === "red") {
      for (const role of Object.values(r.roles)) assert.equal(role.vendor, "anthropic");
      assert.match(r.reasons.join(" "), /Codex window is red/);
      assert.equal(resolveRoles({ ...usage, claude: claude(99, 99) }, registry).paused, true);
    }
    if (level === "green") assert.equal(r.roles.reviewer.vendor, "openai");
  }
  const x = normalizeCodex({ limits });
  x.weekly.pct = 5;
  x.rate_limit_reached = "primary";
  assert.equal(vendorPostures({ codex: x }, registry).openai.overall, "red");
  assert.equal(worst("n/a"), "green");
  x.rate_limit_reached = null;
  x.weekly.resets_at = soon(5);
  assert.equal(vendorPostures({ codex: x }, registry).openai.resets_soon, true);
});

test("weekly-only literal posture and prompt match the Go contract", () => {
  const x = { ok: true, plan: "prolite", five_hour: { pct: null, resets_at: null, window_mins: null, applicable: false }, weekly: { pct: 0, resets_at: "2099-01-07T20:06:43.000Z", window_mins: 10080 } };
  const usage = { claude: claude(10, 10), codex: x };
  const r = resolveRoles(usage, registry);
  assert.deepEqual(r.postures.openai.windows, { five_hour: "n/a", weekly: "green" });
  assert.equal(summarizeForPrompt(r, usage).split("\n")[1], "Codex (prolite): 5h n/a; weekly 0% [green] resets 2099-01-07T20:06:43.000Z.");
  x.five_hour.applicable = true;
  x.five_hour.pct = 10;
  x.weekly.applicable = false;
  const short = resolveRoles(usage, registry);
  assert.equal(short.postures.openai.overall, "green");
  assert.equal(short.postures.openai.windows.weekly, "n/a");
  assert.match(summarizeForPrompt(short, usage), /; weekly n\/a\./);
});

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

// OpenAI credits. Fixtures mirror `account/rateLimits/read` as normalizeCodex stores it.
const codexAt = (five, weekly, { allowed = true, reached = null, credits, banked = 0 } = {}) => ({ ...codex(five, weekly), rate_limit_reached: reached, ordinary_usage_allowed: allowed, reset_credits_available: banked, ...(credits === undefined ? {} : { credits }) });
const limitReached = (credits, banked = 3) => codexAt(88, 100, { allowed: false, reached: "rate_limit_reached", credits, banked });
const withCredits = { hasCredits: true, unlimited: false, balance: "215.68" };
const ON = { mode: "on", floor: 0 };

test("credits fact: limits ok, limit reached with and without credits, unlimited, field absent", () => {
  assert.deepEqual(openaiCredits(codexAt(20, 5, { credits: { hasCredits: false, unlimited: false, balance: "0" } })), { balance: "0", has_credits: false, unlimited: false, plan_limit_reached: false, banked_resets: 0 });
  assert.deepEqual(openaiCredits(limitReached(withCredits)), { balance: "215.68", has_credits: true, unlimited: false, plan_limit_reached: true, banked_resets: 3 });
  assert.equal(creditsText(openaiCredits(limitReached(withCredits))), "credits 215.68 left; plan limit reached; 3 banked resets (window resets, not money)");
  assert.equal(creditsText(openaiCredits(limitReached({ hasCredits: false, unlimited: false, balance: "0" }, 1))), "no credits; plan limit reached; 1 banked reset (window resets, not money)");
  assert.equal(creditsText(openaiCredits(limitReached({ hasCredits: true, unlimited: true }, 0))), "credits unlimited; plan limit reached");
  assert.equal(creditsText(openaiCredits(codexAt(20, 5, { credits: null, banked: 2 }))), "2 banked resets (window resets, not money)");
  assert.equal(openaiCredits(codexAt(20, 5)), null, "no credits object and no banked resets");
  assert.equal(openaiCredits({ ok: false, error: "x" }), null);
  assert.equal(openaiCredits(limitReached({ hasCredits: true, unlimited: false, balance: "lots" })).balance, null, "a balance that is not a decimal is not shown");
});

test("credits policy: anything but on is off; the floor is a number or 0", () => {
  assert.deepEqual(creditsPolicyOf(undefined), { mode: "off", floor: 0 });
  assert.deepEqual(creditsPolicyOf({ openai_credits: "security-only", openai_credits_floor: "x" }), { mode: "off", floor: 0 });
  assert.deepEqual(creditsPolicyOf({ openai_credits: "on", openai_credits_floor: "25.5" }), { mode: "on", floor: 25.5 });
  assert.deepEqual(creditsPolicyOf({ openai_credits: "on", openai_credits_floor: "-5" }), { mode: "on", floor: 0 }, "a negative floor is 0");
});

test("policy off (the default): a red Codex window with credits still fails over to Anthropic", () => {
  const usage = { claude: claude(10, 10), codex: limitReached(withCredits) };
  const r = resolveRoles(usage, registry);
  assert.equal(r.postures.openai.credit_funded, undefined);
  assert.equal(r.roles.reviewer.vendor, "anthropic");
  assert.deepEqual(resolveRoles(usage, registry, { credits: { mode: "off", floor: 0 } }), r);
  assert.match(summarizeForPrompt(r, usage), /weekly 100% \[red\] resets .*; credits 215\.68 left; plan limit reached; 3 banked resets \(window resets, not money\)\./);
});

test("policy on: limit reached with credits routes OpenAI roles through the codex bridge on credits", () => {
  const r = resolveRoles({ claude: claude(10, 10), codex: limitReached(withCredits) }, registry, { channels: { openai: true }, credits: ON });
  assert.equal(r.postures.openai.overall, "red", "the colour stays: credits are a separate fact");
  assert.equal(r.postures.openai.credit_funded, true);
  assert.equal(r.roles.reviewer.vendor, "openai");
  assert.equal(r.roles.reviewer.credit_funded, true);
  assert.equal(r.roles.reviewer.via, "codex-cli", "credit-funded work never runs natively");
  // Weekly-capped with an empty 5-hour window: 100 headroom against Claude's 20 would balance the coder.
  const weeklyCapped = codexAt(0, 100, { allowed: false, reached: "rate_limit_reached", credits: withCredits });
  assert.equal(resolveRoles({ claude: claude(80, 10), codex: weeklyCapped }, registry, { credits: ON }).roles.coder.vendor, "anthropic", "credits are never spent to balance headroom");
  assert.match(r.reasons.join(" "), /Codex window is red but openai_credits=on: OpenAI roles spend ChatGPT credits through the codex bridge \(credits 215\.68 left/);
});

test("policy on: no credits, a balance at the floor, or ordinary usage still allowed keep OpenAI red", () => {
  const funded = (codexEntry, credits = ON) => resolveRoles({ claude: claude(10, 10), codex: codexEntry }, registry, { credits }).postures.openai.credit_funded === true;
  assert.equal(funded(limitReached({ hasCredits: false, unlimited: false, balance: "0" })), false);
  assert.equal(funded(limitReached(undefined)), false, "field absent");
  assert.equal(funded(limitReached(withCredits), { mode: "on", floor: 215.68 }), false, "floor respected");
  assert.equal(funded(limitReached(withCredits), { mode: "on", floor: 200 }), true);
  assert.equal(funded(limitReached({ hasCredits: true, unlimited: true })), true, "unlimited");
  assert.equal(funded(codexAt(99, 40, { allowed: true, credits: withCredits })), false, "red by percentage while Codex still allows ordinary usage");
});
