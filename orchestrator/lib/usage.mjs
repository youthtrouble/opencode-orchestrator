// Gathers Claude + Codex usage, caches it, and resolves routing.
import fs from "node:fs";
import { execFileSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readClaudeUsage } from "./claude-usage.mjs";
import { readCodexAccountAndLimits } from "./codex-appserver.mjs";
import { effectivePolicy } from "./policy.mjs";
import { creditsPolicyOf, resolveRoles, summarizeForPrompt } from "./routing.mjs";

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
  const candidates = [rl.primary, rl.secondary].map((w) => w && typeof w === "object" && !Array.isArray(w) ? w : null);
  const slots = candidates.map((w, i) => typeof w?.windowDurationMins === "number" ? (w.windowDurationMins < 1440 ? 0 : 1) : i);
  // Ambiguous durations retain the legacy positional mapping.
  if (candidates.every(Boolean) && slots[0] === slots[1]) slots.splice(0, 2, 0, 1);
  const windows = [null, null];
  candidates.forEach((w, i) => { if (w) windows[slots[i]] = w; });
  const labeled = candidates.some((w) => typeof w?.windowDurationMins === "number");
  const window = (w) => ({ pct: w?.usedPercent ?? null, resets_at: iso(w?.resetsAt), window_mins: w?.windowDurationMins ?? null, ...(!w && labeled ? { applicable: false } : {}) });
  return {
    ok: true,
    plan: account?.account?.planType || rl.planType || null,
    five_hour: window(windows[0]),
    weekly: window(windows[1]),
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

// The windows whose postures gate routing (see vendorPostures in routing.mjs). Once any of them
// has reset, a cached reading misreports it (a red session that is now empty), so it is stale.
function routingWindows(usage) {
  const { claude, codex } = usage ?? {};
  return [claude?.session, claude?.weekly, claude?.weekly_by_model?.fable, codex?.five_hour, codex?.weekly];
}

// Whether a routing window reset after `since` (the reading's generated_at) and at or before
// `now`, both epoch ms. A reset at or before `since` was already behind the collection that
// wrote the reading: either the reading is post-reset, or the collection failed and carried an
// old window forward as last-good (read_error), in which case the retry waits for the normal
// max age instead of re-collecting on every read. A resets_at that is absent or does not parse
// never counts.
export function anyWindowResetSince(usage, since, now) {
  return routingWindows(usage).some((window) => {
    const resetsAt = typeof window?.resets_at === "string" ? Date.parse(window.resets_at) : NaN;
    return resetsAt > since && resetsAt <= now;
  });
}

// The cached usage, a copy marked stale: true when it is older than maxAgeMs or any routing
// window in it has reset since it was generated, or null when there is none.
export function readCachedUsage(maxAgeMs = 5 * 60 * 1000, now = Date.now()) {
  try {
    const data = JSON.parse(fs.readFileSync(USAGE_FILE, "utf8"));
    const generatedAt = Date.parse(data.generated_at);
    if (now - generatedAt <= maxAgeMs && !anyWindowResetSince(data, generatedAt, now)) return data;
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

// Routing for the project at opts.cwd (default: this process's cwd), whose policy says whether
// OpenAI work may draw ChatGPT credits.
export async function getRouting(opts = {}) {
  const registry = loadRegistry();
  const usage = await getUsage(opts);
  const channels = detectChannels();
  const creditsPolicy = creditsPolicyOf(effectivePolicy(path.resolve(opts.cwd ?? process.cwd()), registry));
  const resolved = resolveRoles(usage, registry, { channels, credits: creditsPolicy });
  return { usage, registry, channels, creditsPolicy, resolved, prompt: summarizeForPrompt(resolved, usage) };
}
