import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { spawnSync } from "node:child_process";
import { normalizeCodex } from "../lib/usage.mjs";
import { vendorPostures } from "../lib/routing.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../models.json", import.meta.url), "utf8"));
const fixture = (name) => JSON.parse(fs.readFileSync(new URL(`../../go/testdata/usage/codex-ratelimits-${name}.json`, import.meta.url), "utf8"));
const absent = { pct: null, resets_at: null, window_mins: null, applicable: false };
const five = { pct: 16, resets_at: "2099-01-01T05:00:19.000Z", window_mins: 300 };
const weekly = { pct: 60, resets_at: "2099-01-07T00:06:43.000Z", window_mins: 10080 };

test("Codex maps shared raw fixtures by duration, not position", () => {
  const owner = normalizeCodex({ limits: fixture("weekly-only") });
  assert.equal(owner.plan, "prolite");
  assert.deepEqual(owner.five_hour, absent);
  assert.deepEqual(owner.weekly, { ...weekly, pct: 0 });
  const swapped = normalizeCodex({ limits: fixture("swapped") });
  assert.deepEqual(swapped.five_hour, five);
  assert.deepEqual(swapped.weekly, weekly);
  const short = normalizeCodex({ limits: fixture("five-hour-only") });
  assert.deepEqual(short.five_hour, five);
  assert.deepEqual(short.weekly, absent);
  assert.equal(vendorPostures({ codex: short }, registry).openai.windows.weekly, "n/a");
});

test("unreadable Codex windows remain unknown, not absent", () => {
  for (const limits of [undefined, { rateLimits: { primary: { usedPercent: null }, secondary: null } }, { rateLimits: { primary: 7, secondary: [] } }]) {
    const codex = normalizeCodex({ account: undefined, limits });
    assert.equal(Object.hasOwn(codex.five_hour, "applicable"), false);
    assert.equal(Object.hasOwn(codex.weekly, "applicable"), false);
    const p = vendorPostures({ codex }, registry).openai;
    assert.equal(p.overall, "unknown");
    assert.deepEqual(p.windows, { five_hour: "unknown", weekly: "unknown" });
  }
  const codex = normalizeCodex({ limits: { rateLimits: { primary: { usedPercent: null, windowDurationMins: 1440 } } } });
  assert.deepEqual(codex.five_hour, absent);
  assert.equal(Object.hasOwn(codex.weekly, "applicable"), false);
  assert.equal(vendorPostures({ codex }, registry).openai.overall, "unknown");
});

test("single-window normalization is feed-shape independent with full literal outputs", () => {
  const cases = [
    [fixture("weekly-only").rateLimits, {
      ok: true,
      plan: "prolite",
      five_hour: { pct: null, resets_at: null, window_mins: null, applicable: false },
      weekly: { pct: 0, resets_at: "2099-01-07T00:06:43.000Z", window_mins: 10080 },
      rate_limit_reached: null,
      ordinary_usage_allowed: null,
      reset_credits_available: 0,
      credits: { hasCredits: false, unlimited: false, balance: "0" },
    }],
    [fixture("by-id").rateLimitsByLimitId.codex, {
      ok: true,
      plan: "pro",
      five_hour: { pct: 100, resets_at: "2026-10-19T00:20:19.987Z", window_mins: 300 },
      weekly: { pct: null, resets_at: null, window_mins: null, applicable: false },
      rate_limit_reached: "primary",
      ordinary_usage_allowed: null,
      reset_credits_available: 0,
      credits: null,
    }],
  ];
  for (const [windowData, expected] of cases) {
    const topLevel = normalizeCodex({ limits: { rateLimits: windowData } });
    const byID = normalizeCodex({ limits: { rateLimitsByLimitId: { codex: windowData } } });
    assert.deepEqual(topLevel, expected);
    assert.deepEqual(byID, expected);
    assert.deepEqual(topLevel, byID);
  }
});

test("colliding duration claims retain positional mapping", () => {
  for (const windowDurationMins of [300, 1440, 10080]) {
    const codex = normalizeCodex({ limits: { rateLimits: { primary: { usedPercent: 1, windowDurationMins }, secondary: { usedPercent: 2, windowDurationMins } } } });
    assert.equal(codex.five_hour.pct, 1);
    assert.equal(codex.weekly.pct, 2);
    assert.equal(Object.hasOwn(codex.five_hour, "applicable"), false);
    assert.equal(Object.hasOwn(codex.weekly, "applicable"), false);
  }
});

test("doctor prints n/a only for an inapplicable Codex window", () => {
  // Load the real CLI with I/O boundaries replaced: no live feed, binaries or recursive tests.
  for (const [name, detail] of [["weekly-only", "5h n/a weekly 0% plan prolite"], ["swapped", "5h 16% weekly 60% plan plus"], ["five-hour-only", "5h 16% weekly n/a plan plus"]]) {
    const codex = normalizeCodex({ limits: fixture(name) });
    const routing = { usage: { claude: { ok: false, error: "offline" }, codex }, resolved: { roles: {} }, creditsPolicy: { mode: "off" } };
    const program = `
      import { registerHooks } from "node:module";
      registerHooks({ load(url, context, next) {
        if (url === ${JSON.stringify(new URL("../lib/usage.mjs", import.meta.url).href)}) return { format: "module", shortCircuit: true, source: ${JSON.stringify(`export const getRouting = async () => (${JSON.stringify(routing)});`)} };
        if (url === "node:child_process") return { format: "module", shortCircuit: true, source: 'export const execFileSync = () => ""; export const spawnSync = () => ({ status: 0, stdout: "pass 1; fail 0", stderr: "" });' };
        return next(url, context);
      }});
      await import(${JSON.stringify(new URL("../bin/doctor.mjs", import.meta.url).href)});
    `;
    const result = spawnSync(process.execPath, ["--input-type=module", "--eval", program], { encoding: "utf8" });
    assert.equal(result.error, undefined);
    assert.ok(result.stdout.includes(`Codex usage feed  —  ${detail}\n`), result.stdout + result.stderr);
  }
});
