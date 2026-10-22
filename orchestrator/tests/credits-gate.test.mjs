import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// USAGE_FILE is resolved from the home directory at import time, so the sandbox HOME must be
// in place before usage.mjs loads; the real ~/.local/state is never read or written.
// A credit draw is confirmed on a fresh reading, so PATH holds only a fake codex (and /bin for
// sh): no real codex, keychain or opencode is reached.
const sandboxHome = fs.mkdtempSync(path.join(os.tmpdir(), "credits-gate-"));
process.env.HOME = sandboxHome;
const fakeBin = fs.mkdtempSync(path.join(sandboxHome, "bin-"));
process.env.PATH = `${fakeBin}:/bin`;
const { USAGE_FILE } = await import("../lib/usage.mjs");
const { budgetGate, freshCreditsBalance } = await import("../lib/budget.mjs");
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

const fakeCodex = (body) => fs.writeFileSync(path.join(fakeBin, "codex"), `#!/bin/sh\n${body}\n`, { mode: 0o755 });
const epochInHours = (h) => Math.floor(Date.now() / 1000) + h * 3600;

// A codex app-server reporting the weekly plan limit reached with `balance` credits left. It
// answers initialize, account/read and rateLimits/read by line (line 2 is a notification).
function codexAnswering(balance) {
  const limits = { ordinaryUsageAllowed: false, rateLimitResetCredits: { availableCount: 3 }, rateLimits: { planType: "plus", rateLimitReachedType: "rate_limit_reached", primary: { usedPercent: 88, windowDurationMins: 300, resetsAt: epochInHours(2) }, secondary: { usedPercent: 100, windowDurationMins: 10080, resetsAt: epochInHours(100) }, credits: { hasCredits: true, unlimited: false, balance } } };
  const reply = (id, result) => `echo '${JSON.stringify({ jsonrpc: "2.0", id, result })}'`;
  fakeCodex(`n=0\nwhile IFS= read -r line; do\n  n=$((n+1))\n  case $n in\n    1) ${reply(1, {})} ;;\n    3) ${reply(2, { account: { planType: "plus" } })} ;;\n    4) ${reply(3, limits)} ;;\n  esac\ndone`);
}

// A codex app-server that refuses initialize: a failed refresh.
const codexFailing = () => fakeCodex(`read -r line; echo '{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"not logged in"}}'`);

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
  codexAnswering("215.68");
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

test("weekly-only red refusal names the weekly reset", async () => {
  writeLimitReachedCache();
  const cache = JSON.parse(fs.readFileSync(USAGE_FILE, "utf8"));
  cache.codex = { ok: true, five_hour: { pct: null, resets_at: null, applicable: false }, weekly: { pct: 85, resets_at: inHours(100) } };
  fs.writeFileSync(USAGE_FILE, JSON.stringify(cache));
  const d = await budgetGate("openai", { cwd: project(null) });
  assert.equal(d.allowed, false);
  assert.equal(d.resets_at, cache.codex.weekly.resets_at);
  assert.ok(d.note.includes(cache.codex.weekly.resets_at));
});

test("a credit draw is decided on a fresh reading and refused without one", async () => {
  const floor20 = project({ openai_credits: "on", openai_credits_floor: "20" });
  writeLimitReachedCache();
  codexAnswering("10");
  const belowFloor = await budgetGate("openai", { cwd: floor20, mayDrawCredits: true });
  assert.equal(belowFloor.allowed, false, "the floor is checked against the fresh balance, not the cached 215.68");
  assert.match(belowFloor.note, /refusing to start/);

  writeLimitReachedCache();
  codexFailing();
  const unconfirmed = await budgetGate("openai", { cwd: floor20, mayDrawCredits: true });
  assert.equal(unconfirmed.allowed, false);
  assert.equal(unconfirmed.note, "openai budget is red and the ChatGPT credits balance could not be re-read (not logged in); refusing to draw credits (pass --force to override).");
});

test("the balance after a credit-funded run is null when the refresh failed", async () => {
  writeLimitReachedCache();
  codexAnswering("190.5");
  assert.equal(await freshCreditsBalance(), "190.5");
  writeLimitReachedCache();
  codexFailing();
  assert.equal(await freshCreditsBalance(), null, "the kept last good reading (read_error) has a stale balance");
});

test("policy shows openai_credits off by default and refuses values it does not take", () => {
  const root = project(null);
  const run = (...args) => spawnSync(process.execPath, [POLICY_BIN, ...args, "--cwd", root], { encoding: "utf8" });
  assert.equal(JSON.parse(run("show").stdout).openai_credits, "off");
  const refused = run("set", "openai_credits=security-only");
  assert.equal(refused.status, 2);
  assert.equal(refused.stderr.trim(), "openai_credits must be off or on");
  assert.equal(run("set", "openai_credits_floor=lots").stderr.trim(), "openai_credits_floor must be a number, 0 or more");
  assert.equal(run("set", "openai_credits_floor=-5").stderr.trim(), "openai_credits_floor must be a number, 0 or more");
  const set = run("set", "openai_credits=on", "openai_credits_floor=20");
  assert.equal(set.status, 0);
  assert.deepEqual([JSON.parse(set.stdout).openai_credits, JSON.parse(set.stdout).openai_credits_floor], ["on", "20"]);
});
