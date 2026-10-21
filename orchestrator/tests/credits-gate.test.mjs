import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// USAGE_FILE is resolved from the home directory at import time, so the sandbox HOME must be
// in place before usage.mjs loads; the real ~/.local/state is never read or written.
const sandboxHome = fs.mkdtempSync(path.join(os.tmpdir(), "credits-gate-"));
process.env.HOME = sandboxHome;
const { USAGE_FILE } = await import("../lib/usage.mjs");
const { budgetGate } = await import("../lib/budget.mjs");
test.after(() => fs.rmSync(sandboxHome, { recursive: true, force: true }));

const POLICY_BIN = new URL("../bin/policy.mjs", import.meta.url).pathname;
const inHours = (h) => new Date(Date.now() + h * 3600 * 1000).toISOString();

// A fresh cache: Claude green; Codex past its weekly plan limit with 215.68 credits left.
function writeLimitReachedCache() {
  fs.mkdirSync(path.dirname(USAGE_FILE), { recursive: true });
  fs.writeFileSync(USAGE_FILE, JSON.stringify({
    generated_at: new Date().toISOString(),
    claude: { ok: true, plan: "team", session: { pct: 10, resets_at: inHours(2) }, weekly: { pct: 10, resets_at: inHours(50) }, weekly_by_model: {} },
    codex: { ok: true, plan: "plus", five_hour: { pct: 88, resets_at: inHours(2) }, weekly: { pct: 100, resets_at: inHours(100) }, rate_limit_reached: "rate_limit_reached", ordinary_usage_allowed: false, reset_credits_available: 3, credits: { hasCredits: true, unlimited: false, balance: "215.68" } },
  }));
}

function project(policy) {
  const root = fs.mkdtempSync(path.join(sandboxHome, "project-"));
  if (policy) {
    fs.mkdirSync(path.join(root, ".orchestrator"));
    fs.writeFileSync(path.join(root, ".orchestrator", "policy.json"), JSON.stringify(policy));
  }
  return root;
}

test("the bridge gate refuses a red OpenAI window unless the project's policy funds it from credits", async () => {
  writeLimitReachedCache();
  const off = await budgetGate("openai", { cwd: project(null), mayDrawCredits: true });
  assert.equal(off.allowed, false);

  const on = project({ openai_credits: "on" });
  const funded = await budgetGate("openai", { cwd: on, mayDrawCredits: true });
  assert.equal(funded.allowed, true);
  assert.equal(funded.credit_funded, true);
  assert.equal(funded.credits_before, "215.68");
  assert.match(funded.note, /drawing ChatGPT credits \(openai_credits=on\)/);

  assert.equal((await budgetGate("openai", { cwd: on })).allowed, false, "a caller that records no credit-funded run is refused");
  assert.equal((await budgetGate("openai", { cwd: on, mayDrawCredits: true, frontier: true })).allowed, false, "frontier tiers never run on credits");
  assert.equal((await budgetGate("openai", { cwd: project({ openai_credits: "on", openai_credits_floor: "300" }), mayDrawCredits: true })).allowed, false, "floor respected");
});

test("policy shows openai_credits off by default and refuses values it does not take", () => {
  const root = project(null);
  const run = (...args) => spawnSync(process.execPath, [POLICY_BIN, ...args, "--cwd", root], { encoding: "utf8" });
  assert.equal(JSON.parse(run("show").stdout).openai_credits, "off");
  const refused = run("set", "openai_credits=security-only");
  assert.equal(refused.status, 2);
  assert.equal(refused.stderr.trim(), "openai_credits must be off or on");
  assert.equal(run("set", "openai_credits_floor=lots").stderr.trim(), "openai_credits_floor must be a number");
  const set = run("set", "openai_credits=on", "openai_credits_floor=20");
  assert.equal(set.status, 0);
  assert.deepEqual([JSON.parse(set.stdout).openai_credits, JSON.parse(set.stdout).openai_credits_floor], ["on", "20"]);
});
