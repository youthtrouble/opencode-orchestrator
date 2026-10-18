// Reads Claude subscription usage via the OAuth usage endpoint that Claude Code itself uses.
// Token sources, in order: opencode's own Anthropic OAuth login, then Claude Code's keychain entry.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";

const USAGE_URL = "https://api.anthropic.com/api/oauth/usage";

function readOpencodeAuthToken() {
  const candidates = [
    path.join(os.homedir(), ".local", "share", "opencode", "auth.json"),
    path.join(os.homedir(), ".opencode", "auth.json"),
  ];
  for (const file of candidates) {
    try {
      const data = JSON.parse(fs.readFileSync(file, "utf8"));
      const entry = data.anthropic;
      if (entry && entry.type === "oauth" && entry.access) {
        const expired = typeof entry.expires === "number" && entry.expires < Date.now();
        if (!expired) return { token: entry.access, source: "opencode-auth" };
      }
    } catch { /* try next */ }
  }
  return null;
}

function readClaudeCodeKeychainToken() {
  try {
    const raw = execFileSync("security", ["find-generic-password", "-s", "Claude Code-credentials", "-w"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
    const oauth = JSON.parse(raw).claudeAiOauth;
    if (oauth?.accessToken && (!oauth.expiresAt || oauth.expiresAt > Date.now())) {
      return { token: oauth.accessToken, source: "claude-code-keychain", plan: oauth.subscriptionType, tier: oauth.rateLimitTier };
    }
  } catch { /* not on macOS or not logged in */ }
  try {
    const file = path.join(os.homedir(), ".claude", ".credentials.json");
    const oauth = JSON.parse(fs.readFileSync(file, "utf8")).claudeAiOauth;
    if (oauth?.accessToken) return { token: oauth.accessToken, source: "claude-code-credentials-file", plan: oauth.subscriptionType };
  } catch { /* none */ }
  return null;
}

export function findClaudeToken() {
  return readOpencodeAuthToken() || readClaudeCodeKeychainToken();
}

export async function fetchClaudeUsageRaw(token, { timeoutMs = 15000 } = {}) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(USAGE_URL, {
      headers: { Authorization: `Bearer ${token}`, "anthropic-beta": "oauth-2025-04-20", "Content-Type": "application/json" },
      signal: ctrl.signal,
    });
    if (!res.ok) throw new Error(`usage endpoint ${res.status}`);
    return await res.json();
  } finally {
    clearTimeout(t);
  }
}

// Normalize the raw payload into the few numbers routing needs.
export function normalizeClaudeUsage(raw, meta = {}) {
  const limits = Array.isArray(raw.limits) ? raw.limits : [];
  const pick = (pred) => limits.find(pred);
  const session = pick((l) => l.kind === "session") || null;
  const weeklyAll = pick((l) => l.group === "weekly" && !l.scope) || null;
  const scoped = limits.filter((l) => l.group === "weekly" && l.scope?.model);
  const byModel = {};
  for (const l of scoped) {
    const name = (l.scope.model.display_name || l.scope.model.id || "model").toLowerCase();
    byModel[name] = { pct: l.percent, resets_at: l.resets_at, severity: l.severity, active: l.is_active };
  }
  const five = raw.five_hour || {};
  const seven = raw.seven_day || {};
  return {
    ok: true,
    source: meta.source || null,
    plan: meta.plan || null,
    tier: meta.tier || null,
    session: {
      pct: session?.percent ?? (typeof five.utilization === "number" ? Math.round(five.utilization) : null),
      resets_at: session?.resets_at ?? five.resets_at ?? null,
      severity: session?.severity ?? null,
    },
    weekly: {
      pct: weeklyAll?.percent ?? (typeof seven.utilization === "number" ? Math.round(seven.utilization) : null),
      resets_at: weeklyAll?.resets_at ?? seven.resets_at ?? null,
      severity: weeklyAll?.severity ?? null,
    },
    weekly_by_model: byModel, // e.g. { fable: { pct: 81, ... } }
    extra_usage_enabled: Boolean(raw.extra_usage?.is_enabled),
    locked_reason: five.locked_reason || null,
  };
}

export async function readClaudeUsage() {
  const found = findClaudeToken();
  if (!found) return { ok: false, error: "no Claude OAuth token found (run `opencode auth login` for Anthropic, or log in to Claude Code)" };
  try {
    const raw = await fetchClaudeUsageRaw(found.token);
    return normalizeClaudeUsage(raw, found);
  } catch (err) {
    return { ok: false, source: found.source, error: String(err.message || err) };
  }
}
