import test, { mock } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// USAGE_FILE is resolved from the home directory at import time, so the sandbox HOME must be
// in place before usage.mjs loads; the real ~/.local/state is never read or written.
const sandboxHome = fs.mkdtempSync(path.join(os.tmpdir(), "usage-cache-"));
process.env.HOME = sandboxHome;
const { readCachedUsage, getUsage, USAGE_FILE } = await import("../lib/usage.mjs");
test.after(() => fs.rmSync(sandboxHome, { recursive: true, force: true }));

const NOW = Date.parse("2026-10-18T23:40:00.000Z");
const MAX_AGE = 5 * 60 * 1000;
const at = (offsetMs) => new Date(NOW + offsetMs).toISOString();
const FUTURE = at(60 * 60 * 1000);
const PASSED = at(0); // a window that resets exactly now has reset

function writeCache(overrides = {}) {
  const usage = {
    generated_at: at(-60 * 1000),
    claude: {
      ok: true,
      session: { pct: 99, resets_at: FUTURE },
      weekly: { pct: 40, resets_at: FUTURE },
      weekly_by_model: { fable: { pct: 30, resets_at: FUTURE } },
    },
    codex: { ok: true, five_hour: { pct: 10, resets_at: FUTURE }, weekly: { pct: 5, resets_at: FUTURE } },
  };
  const merged = structuredClone(usage);
  for (const [dotted, value] of Object.entries(overrides)) {
    const keys = dotted.split(".");
    const parent = keys.slice(0, -1).reduce((o, k) => o[k], merged);
    parent[keys.at(-1)] = value;
  }
  fs.mkdirSync(path.dirname(USAGE_FILE), { recursive: true });
  fs.writeFileSync(USAGE_FILE, JSON.stringify(merged));
  return merged;
}

test("a fresh cache whose windows all reset in the future is used as is", () => {
  const written = writeCache();
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), written);
});

test("a reset at or before generated_at was behind the reading and does not make it stale", () => {
  for (const resetsAt of [at(-60 * 1000), at(-30 * 60 * 1000)]) {
    const written = writeCache({ "claude.session.resets_at": resetsAt });
    assert.deepEqual(readCachedUsage(MAX_AGE, NOW), written);
  }
});

// A claude entry carried forward as last-good over a failed collection, as getUsage writes it.
const lastGood = (sessionResetsAt) => ({
  ok: true,
  session: { pct: 100, resets_at: sessionResetsAt },
  last_good_at: at(-20 * 60 * 1000),
  read_error: "usage endpoint 429",
});

test("a last-good reading cached before its reset is stale, so the first read after it collects", () => {
  const written = writeCache({ claude: lastGood(at(-30 * 1000)) });
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), { ...written, stale: true });
});

test("a last-good reading carried past its reset waits for the max age", () => {
  const written = writeCache({ claude: lastGood(at(-2 * 60 * 1000)) });
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), written);
});

test("the other vendor resetting while a last-good reading waits is stale", () => {
  const written = writeCache({ claude: lastGood(at(-2 * 60 * 1000)), "codex.five_hour.resets_at": at(-30 * 1000) });
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), { ...written, stale: true });
});

for (const window of ["claude.session", "claude.weekly", "claude.weekly_by_model.fable", "codex.five_hour", "codex.weekly"]) {
  test(`a fresh cache is stale once ${window} has reset`, () => {
    const written = writeCache({ [`${window}.resets_at`]: PASSED });
    assert.deepEqual(readCachedUsage(MAX_AGE, NOW), { ...written, stale: true });
  });
}

test("a cache older than maxAgeMs is stale", () => {
  const written = writeCache({ generated_at: at(-MAX_AGE - 1) });
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), { ...written, stale: true });
});

test("missing, null or unparsable resets_at never makes a fresh cache stale", () => {
  const written = writeCache({
    "claude.session.resets_at": null,
    "claude.weekly.resets_at": "soon",
    "claude.weekly_by_model": {},
    "codex.five_hour": { pct: 10 },
    "codex.weekly.resets_at": 0,
  });
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), written);
});

test("a failed vendor reading without windows does not make the cache stale", () => {
  const written = writeCache({ claude: { ok: false, error: "usage endpoint 429" } });
  assert.deepEqual(readCachedUsage(MAX_AGE, NOW), written);
});

// A codex on PATH that answers initialize, account/read and account/rateLimits/read (requests
// on input lines 1, 3 and 4) with a healthy reading whose windows reset in 2099.
function installHealthyCodex() {
  const bin = path.join(sandboxHome, "bin");
  fs.mkdirSync(bin, { recursive: true });
  const limits = { rateLimits: { primary: { usedPercent: 10, resetsAt: 4070926819, windowDurationMins: 300 }, secondary: { usedPercent: 5, resetsAt: 4071427603, windowDurationMins: 10080 } } };
  const reply = (id, result) => `'${JSON.stringify({ jsonrpc: "2.0", id, result })}'`;
  fs.writeFileSync(path.join(bin, "codex"), `#!/bin/sh
n=0
while IFS= read -r line; do
  n=$((n+1))
  case $n in
    1) echo ${reply(1, {})} ;;
    3) echo ${reply(2, { account: { planType: "pro" } })} ;;
    4) echo ${reply(3, limits)} ;;
  esac
done
`, { mode: 0o755 });
  process.env.PATH = `${bin}:/bin:/usr/bin`;
}

// After a reset, a failing feed is retried on the first read and then on the normal max-age
// cadence, not on every read: the failed collection carries the expired window forward as
// last-good with a fresh generated_at. The healthy codex feed never forces a collection. Every
// collection fetches the Claude endpoint once, so the fetch count is the collection count.
test("getUsage retries a failing feed after a reset on the max-age cadence", async (t) => {
  fs.rmSync(USAGE_FILE, { force: true });
  fs.mkdirSync(path.join(sandboxHome, ".local", "share", "opencode"), { recursive: true });
  fs.writeFileSync(path.join(sandboxHome, ".local", "share", "opencode", "auth.json"), JSON.stringify({ anthropic: { type: "oauth", access: "tok-test" } }));
  installHealthyCodex();
  const RED = "2026-10-18T23:40:00+00:00", RENEWED = "2026-10-19T04:40:00+00:00", LIMITED = null;
  let endpoint, fetches = 0;
  t.mock.method(globalThis, "fetch", async () => {
    fetches++;
    if (endpoint === LIMITED) return new Response("", { status: 429 });
    return Response.json({ limits: [{ kind: "session", percent: 100, resets_at: endpoint, severity: "normal" }] });
  });
  t.mock.timers.enable({ apis: ["Date"] });

  const steps = [
    ["a red reading before the reset is collected", "2026-10-18T23:38:00.000Z", RED, 1, false],
    ["within the max age before the reset it is reused", "2026-10-18T23:39:59.999Z", RED, 1, false],
    ["the first read after the reset collects; the 429 keeps the last good reading", "2026-10-18T23:40:30.000Z", LIMITED, 2, true],
    ["within the max age of that attempt nothing is collected", "2026-10-18T23:41:00.000Z", LIMITED, 2, true],
    ["exactly the max age after the attempt still nothing", "2026-10-18T23:45:30.000Z", LIMITED, 2, true],
    ["past the max age the failing feed is retried", "2026-10-18T23:45:30.001Z", LIMITED, 3, true],
    ["within the max age of the retry nothing is collected", "2026-10-18T23:47:00.000Z", RENEWED, 3, true],
    ["the next retry recovers", "2026-10-18T23:50:30.002Z", RENEWED, 4, false],
    ["and the recovered reading is reused", "2026-10-18T23:51:00.000Z", RENEWED, 4, false],
  ];
  for (const [name, when, answer, wantFetches, wantReadError] of steps) {
    endpoint = answer;
    t.mock.timers.setTime(Date.parse(when));
    const usage = await getUsage();
    assert.equal(fetches, wantFetches, `${name}: collections`);
    assert.equal("read_error" in usage.claude, wantReadError, `${name}: claude read_error`);
    assert.equal(usage.codex.ok, true, `${name}: codex stays healthy`);
  }
});
