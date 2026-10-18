// Gathers Claude + Codex usage, caches it, and resolves routing.
import fs from "node:fs";
import { execFileSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readClaudeUsage } from "./claude-usage.mjs";
import { readCodexAccountAndLimits } from "./codex-appserver.mjs";
import { resolveRoles, summarizeForPrompt } from "./routing.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const ROOT = path.resolve(HERE, "..");
export const STATE_DIR = path.join(os.homedir(), ".local", "state", "opencode", "orchestrator");
export const USAGE_FILE = path.join(STATE_DIR, "usage.json");
export const REGISTRY_FILE = path.join(ROOT, "models.json");

export function loadRegistry() {
  return JSON.parse(fs.readFileSync(REGISTRY_FILE, "utf8"));
}

export function normalizeCodex({ account, limits }) {
  const rl = limits?.rateLimits || limits?.rateLimitsByLimitId?.codex || {};
  const iso = (s) => (typeof s === "number" ? new Date(s * 1000).toISOString() : null);
  return {
    ok: true,
    plan: account?.account?.planType || rl.planType || null,
    five_hour: { pct: rl.primary?.usedPercent ?? null, resets_at: iso(rl.primary?.resetsAt), window_mins: rl.primary?.windowDurationMins ?? null },
    weekly: { pct: rl.secondary?.usedPercent ?? null, resets_at: iso(rl.secondary?.resetsAt), window_mins: rl.secondary?.windowDurationMins ?? null },
    rate_limit_reached: rl.rateLimitReachedType || null,
    ordinary_usage_allowed: limits?.ordinaryUsageAllowed ?? null,
    reset_credits_available: limits?.rateLimitResetCredits?.availableCount ?? 0,
    credits: rl.credits || null,
  };
}

// Which vendors are natively authenticated inside opencode (vs reachable only via their CLI).
// opencode v2 stores credentials in SQLite (credential table); v1 used auth.json. Check both.
export function detectChannels() {
  const channels = { anthropic: false, openai: false };
  const db = path.join(os.homedir(), ".local", "share", "opencode", "opencode.db");
  if (fs.existsSync(db)) {
    try {
      const out = execFileSync("sqlite3", [db, "select distinct integration_id from credential where active = 1 or active is null"], { encoding: "utf8", timeout: 5000, stdio: ["ignore", "pipe", "ignore"] });
      for (const id of out.split("\n").map((l) => l.trim()).filter(Boolean)) if (id in channels) channels[id] = true;
      return channels;
    } catch { /* column name may differ; fall through to the CLI */ }
    try {
      const out = execFileSync("opencode", ["auth", "list"], { encoding: "utf8", timeout: 15000, stdio: ["ignore", "pipe", "ignore"] });
      if (/openai/i.test(out)) channels.openai = true;
      if (/anthropic/i.test(out)) channels.anthropic = true;
      return channels;
    } catch { /* service unavailable */ }
  }
  for (const file of [path.join(os.homedir(), ".local", "share", "opencode", "auth.json")]) {
    try {
      const data = JSON.parse(fs.readFileSync(file, "utf8"));
      if (data.anthropic) channels.anthropic = true;
      if (data.openai) channels.openai = true;
    } catch { /* no file */ }
  }
  return channels;
}

export async function collectUsage() {
  const [claude, codexRes] = await Promise.allSettled([readClaudeUsage(), readCodexAccountAndLimits()]);
  const usage = {
    generated_at: new Date().toISOString(),
    claude: claude.status === "fulfilled" ? claude.value : { ok: false, error: String(claude.reason?.message || claude.reason) },
    codex: codexRes.status === "fulfilled" ? normalizeCodex(codexRes.value) : { ok: false, error: String(codexRes.reason?.message || codexRes.reason) },
  };
  return usage;
}

export function readCachedUsage(maxAgeMs = 5 * 60 * 1000) {
  try {
    const data = JSON.parse(fs.readFileSync(USAGE_FILE, "utf8"));
    const age = Date.now() - Date.parse(data.generated_at);
    if (age <= maxAgeMs) return data;
    return { ...data, stale: true };
  } catch {
    return null;
  }
}

export async function getUsage({ refresh = false, maxAgeMs } = {}) {
  const cached = readCachedUsage(maxAgeMs);
  if (!refresh && cached && !cached.stale) return cached;
  const usage = await collectUsage();
  // A failed read (e.g. HTTP 429 on the usage endpoint) must not make a vendor look unavailable:
  // keep the last good reading, marked with its age, for up to 30 minutes.
  for (const vendor of ["claude", "codex"]) {
    const fresh = usage[vendor], last = cached?.[vendor];
    if (fresh && !fresh.ok && last && last.ok && Date.now() - Date.parse(cached.generated_at) < 30 * 60 * 1000) {
      usage[vendor] = { ...last, last_good_at: last.last_good_at || cached.generated_at, read_error: fresh.error };
    }
  }
  fs.mkdirSync(STATE_DIR, { recursive: true });
  const tmp = USAGE_FILE + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(usage, null, 2));
  fs.renameSync(tmp, USAGE_FILE);
  return usage;
}

export async function getRouting(opts = {}) {
  const registry = loadRegistry();
  const usage = await getUsage(opts);
  const channels = detectChannels();
  const resolved = resolveRoles(usage, registry, { channels });
  return { usage, registry, channels, resolved, prompt: summarizeForPrompt(resolved, usage) };
}
