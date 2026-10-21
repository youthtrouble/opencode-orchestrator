// Budget-aware role -> model routing. Pure functions; no I/O.

// Posture is computed from the raw percentage only. An imminent reset never makes a window look
// greener: that rule once launched a conductor into a window with 2% left (incident 2026-10-18).
export function postureFromPct(pct, thresholds, { locked = false } = {}) {
  if (locked) return "red";
  if (pct === null || pct === undefined) return "unknown";
  const level = pct >= thresholds.red ? 2 : pct >= thresholds.amber ? 1 : 0;
  return ["green", "amber", "red"][level];
}

const RANK = { green: 0, unknown: 1, amber: 2, red: 3 };
export const worst = (...p) => p.reduce((a, b) => (RANK[b] > RANK[a] ? b : a), "green");

function minutesUntil(iso, now) {
  if (!iso) return null;
  const t = typeof iso === "number" ? iso * 1000 : Date.parse(iso);
  if (Number.isNaN(t)) return null;
  return Math.round((t - now) / 60000);
}

// Build per-vendor postures from normalized usage.
export function vendorPostures(usage, registry, now = Date.now()) {
  const th = registry.thresholds;
  const out = {};

  const c = usage.claude;
  if (c?.ok) {
    const sessionSoon = (minutesUntil(c.session.resets_at, now) ?? 1e9) <= th.soon_minutes;
    const session = postureFromPct(c.session.pct, th, { locked: Boolean(c.locked_reason) });
    const weekly = postureFromPct(c.weekly.pct, th);
    const fableWin = c.weekly_by_model?.fable;
    const fable = fableWin ? postureFromPct(fableWin.pct, th) : "unknown";
    // 5-hour window is primary; weekly only blocks when red (long-horizon signal, not a steering one).
    const weeklyBlock = weekly === "red" ? "red" : "green";
    out.anthropic = {
      available: true,
      overall: worst(session, weeklyBlock),
      frontier: worst(session, weeklyBlock, fable === "unknown" ? "green" : fable),
      headroom: c.session.pct === null || c.session.pct === undefined ? null : 100 - c.session.pct,
      windows: { session, weekly, fable },
      resets_soon: sessionSoon,
      detail: c,
    };
  } else {
    out.anthropic = { available: false, overall: "unknown", frontier: "unknown", windows: {}, detail: c || null };
  }

  const x = usage.codex;
  if (x?.ok) {
    const fiveSoon = (minutesUntil(x.five_hour.resets_at, now) ?? 1e9) <= th.soon_minutes;
    const five = postureFromPct(x.five_hour.pct, th, { locked: Boolean(x.rate_limit_reached) });
    const weekly = postureFromPct(x.weekly.pct, th);
    const weeklyBlockO = weekly === "red" ? "red" : "green";
    out.openai = { available: true, overall: worst(five, weeklyBlockO), frontier: worst(five, weeklyBlockO), headroom: x.five_hour.pct === null || x.five_hour.pct === undefined ? null : 100 - x.five_hour.pct, windows: { five_hour: five, weekly }, resets_soon: fiveSoon, detail: x };
  } else {
    out.openai = { available: false, overall: "unknown", frontier: "unknown", windows: {}, detail: x || null };
  }
  return out;
}

// ChatGPT credits: Codex draws them by itself once the plan limits are reached (ordinary usage
// refused). Banked resets (rateLimitResetCredits) are promotional resets of the usage window,
// not money; nothing here redeems them. A balance is a decimal string in the app-server schema.
const DECIMAL = /^-?\d+(\.\d+)?$/;
const isPlainObject = (v) => v !== null && typeof v === "object" && !Array.isArray(v);

// A number, or a decimal string read as one (policy values are strings); null for anything else.
export function decimalOrNull(v) {
  if (typeof v === "number") return Number.isFinite(v) ? v : null;
  return typeof v === "string" && DECIMAL.test(v) ? Number(v) : null;
}

// The OpenAI credits fact of a Codex usage entry, or null when Codex reported neither credits
// nor banked resets. has_credits, unlimited and balance are null when it sent no credits object.
export function openaiCredits(codex) {
  if (!codex?.ok) return null;
  const credits = isPlainObject(codex.credits) ? codex.credits : null;
  const banked = Number.isInteger(codex.reset_credits_available) && codex.reset_credits_available > 0 ? codex.reset_credits_available : 0;
  if (!credits && banked === 0) return null;
  return {
    balance: typeof credits?.balance === "string" && DECIMAL.test(credits.balance) ? credits.balance : null,
    has_credits: credits ? credits.hasCredits === true : null,
    unlimited: credits ? credits.unlimited === true : null,
    plan_limit_reached: codex.ordinary_usage_allowed === false || Boolean(codex.rate_limit_reached),
    banked_resets: banked,
  };
}

// "credits 215.68 left; plan limit reached; 3 banked resets (window resets, not money)".
export function creditsText(fact) {
  const parts = [];
  if (fact.has_credits !== null) {
    if (fact.unlimited) parts.push("credits unlimited");
    else if (!fact.has_credits) parts.push("no credits");
    else parts.push(fact.balance === null ? "credits available" : `credits ${fact.balance} left`);
  }
  if (fact.plan_limit_reached) parts.push("plan limit reached");
  if (fact.banked_resets > 0) parts.push(`${fact.banked_resets} banked reset${fact.banked_resets === 1 ? "" : "s"} (window resets, not money)`);
  return parts.join("; ");
}

// The owner's credits setting (policy openai_credits, openai_credits_floor). Anything but "on"
// is off: credits are spend, so they are never drawn without the explicit setting.
export const CREDITS_OFF = Object.freeze({ mode: "off", floor: 0 });
export function creditsPolicyOf(policy) {
  return { mode: policy?.openai_credits === "on" ? "on" : "off", floor: decimalOrNull(policy?.openai_credits_floor) ?? 0 };
}
export const creditsPolicyText = (credits) => `openai_credits=${credits.mode}${credits.floor ? ` floor ${credits.floor}` : ""}`;

// Whether red OpenAI work may run on credits: the setting is on, Codex refuses ordinary usage
// (the plan limit is reached), and credits are unlimited or their balance is above the floor.
function creditFunded(posture, credits) {
  if (credits.mode !== "on" || !posture.available || posture.overall !== "red" || posture.detail.ordinary_usage_allowed !== false) return false;
  const fact = openaiCredits(posture.detail);
  return Boolean(fact?.unlimited || (fact?.has_credits && fact.balance !== null && Number(fact.balance) > credits.floor));
}

function tierEntry(registry, vendor, tier) {
  const v = registry.vendors[vendor];
  const t = v?.tiers?.[tier];
  if (!t) return null;
  return { vendor, tier, model: t.opencode, cli_model: t.cli, cli: v.cli, variant: t.variant ?? null, effort: t.effort ?? null, context: t.context ?? null, note: t.note ?? "" };
}

// A vendor is "usable" for a role unless red (or unknown when the other vendor is known-good);
// a red one whose work is credit-funded stays usable, except for frontier tiers.
// Frontier tiers draw on weekly caps (Fable has its own); they are used only while that posture is GREEN.
// Amber frontier means: route routine planning to the strong tier; `size: hard` may still force frontier (delegate tool).
function usable(postures, vendor, isFrontier) {
  const p = postures[vendor];
  if (!p || !p.available) return false;
  if (isFrontier) return p.frontier === "green";
  return p.overall !== "red" || p.credit_funded === true;
}

export function resolveRoles(usage, registry, { now = Date.now(), channels = {}, credits = CREDITS_OFF } = {}) {
  const postures = vendorPostures(usage, registry, now);
  if (creditFunded(postures.openai, credits)) postures.openai.credit_funded = true;
  const roles = {};
  const reasons = [];

  const margin = registry.thresholds?.balance_margin ?? 15;
  const BALANCED = new Set(["coder", "verifier", "researcher", "explorer", "utility"]);
  const pickFrom = (prefs, { coderVendor, role } = {}) => {
    const candidates = [];
    for (const pref of prefs) {
      let [vendor, tier] = pref.split(".");
      if (vendor === "cross") vendor = coderVendor === "anthropic" ? "openai" : "anthropic";
      if (vendor === "same") vendor = coderVendor;
      const isFrontier = tier === "frontier";
      if (!usable(postures, vendor, isFrontier)) continue;
      const entry = tierEntry(registry, vendor, tier);
      if (entry) candidates.push(postures[vendor].credit_funded ? { ...entry, credit_funded: true } : entry);
    }
    if (!candidates.length) return null;
    // Balance equivalent tiers across vendors by 5-hour headroom (the vendor with clearly more room wins).
    // Credits are never spent to balance headroom.
    if (BALANCED.has(role) && candidates.length > 1) {
      const first = candidates[0];
      const balanceable = (c) => c.vendor !== first.vendor && !c.credit_funded;
      const alt = candidates.find((c) => balanceable(c) && c.tier === first.tier) || candidates.find(balanceable);
      const h = (v) => postures[v]?.headroom ?? -1;
      if (alt && h(alt.vendor) - h(first.vendor) > margin) return { ...alt, balanced: true };
    }
    return candidates[0];
  };

  // planner and coder first; reviewer depends on coder's vendor (cross-vendor review).
  roles.planner = pickFrom(registry.roles.planner.prefer, { role: "planner" });
  roles.coder = pickFrom(registry.roles.coder.prefer, { role: "coder" });
  const coderVendor = roles.coder?.vendor || "anthropic";
  roles.reviewer = pickFrom(registry.roles.reviewer.prefer, { coderVendor, role: "reviewer" });
  if (roles.reviewer) roles.reviewer.cross_vendor = roles.reviewer.vendor !== coderVendor;
  for (const r of ["verifier", "researcher", "explorer", "utility"]) roles[r] = pickFrom(registry.roles[r].prefer, { role: r });

  // Explain the interesting decisions.
  const a = postures.anthropic, o = postures.openai;
  if (a.available && a.windows.fable === "red") reasons.push("Fable weekly cap is red: planning routed away from Fable until it resets.");
  if (a.available && a.windows.fable === "amber") reasons.push("Fable weekly cap is amber: routine planning goes to Opus 5.5; only size=hard delegations use Fable.");
  if (a.available && a.overall === "red") reasons.push("Claude session/weekly is red: Anthropic roles failed over to OpenAI.");
  if (roles.coder?.balanced) reasons.push(`Coder balanced to ${roles.coder.vendor}: it has more 5-hour headroom (Claude ${100 - (a.headroom ?? 0)}% vs Codex ${100 - (o.headroom ?? 0)}% used).`);
  if ((a.available && a.windows.weekly === "amber") || (o.available && o.windows.weekly === "amber")) reasons.push("A weekly cap is amber: shown for planning, not steering; only a red weekly blocks a vendor.");
  if (o.available && o.overall === "red") reasons.push(o.credit_funded ? `Codex window is red but ${creditsPolicyText(credits)}: OpenAI roles spend ChatGPT credits through the codex bridge (${creditsText(openaiCredits(o.detail))}).` : "Codex window is red: OpenAI roles failed over to Anthropic.");
  if (!a.available) reasons.push("Claude usage unknown (no OAuth token found): treating Anthropic as unavailable for automatic routing.");
  if (!o.available) reasons.push("Codex usage unknown (app-server unreachable or not logged in): treating OpenAI as unavailable for automatic routing.");
  const allPaused = Object.values(roles).every((r) => !r);
  if (allPaused) reasons.push("Both vendors are red or unavailable: pause heavy work; wait for the nearest reset.");

  // Attach the execution channel: native opencode provider or the vendor's CLI bridge. Credit-funded
  // work always takes the bridge, which records the balance before and after the run.
  for (const [name, r] of Object.entries(roles)) {
    if (!r) continue;
    const native = channels[r.vendor] === true && !r.credit_funded;
    r.via = native ? "opencode" : `${r.cli}-cli`;
    roles[name] = r;
  }

  return { postures, roles, reasons, paused: allPaused };
}

export function summarizeForPrompt(resolved, usage) {
  const lines = [];
  const c = usage.claude, x = usage.codex;
  const p = resolved.postures;
  if (c?.ok) {
    const fable = c.weekly_by_model?.fable;
    lines.push(`Claude (${c.plan || "subscription"}${c.read_error ? ", last good " + String(c.last_good_at).slice(11, 16) + "Z" : ""}): session ${c.session.pct ?? "?"}% [${p.anthropic.windows.session}] resets ${c.session.resets_at || "?"}; weekly ${c.weekly.pct ?? "n/a"}%${fable ? `; Fable weekly ${fable.pct}% [${p.anthropic.windows.fable}] resets ${fable.resets_at}` : ""}.`);
  } else lines.push(`Claude usage: unavailable (${c?.error || "no data"}).`);
  if (x?.ok) {
    const credits = openaiCredits(x);
    lines.push(`Codex (${x.plan || "subscription"}): 5h ${x.five_hour.pct}% [${p.openai.windows.five_hour}] resets ${x.five_hour.resets_at}; weekly ${x.weekly.pct}% [${p.openai.windows.weekly}] resets ${x.weekly.resets_at}${credits ? "; " + creditsText(credits) : ""}.`);
  }
  else lines.push(`Codex usage: unavailable (${x?.error || "no data"}).`);
  lines.push("Routing now:");
  for (const [role, r] of Object.entries(resolved.roles)) {
    lines.push(r ? `  ${role}: ${r.model}${r.variant ? "#" + r.variant : ""}${r.effort ? " (effort " + r.effort + ")" : ""} via ${r.via}${r.cross_vendor === false ? " [same-vendor review: cross vendor unavailable]" : ""}${r.balanced ? " [balanced by headroom]" : ""}` : `  ${role}: PAUSED`);
  }
  for (const reason of resolved.reasons) lines.push(`  note: ${reason}`);
  return lines.join("\n");
}

// Effort for a role given the task size, from registry.effort_policy. Unknown size -> default size.
export function effortFor(registry, role, size) {
  const policy = registry.effort_policy || {};
  const table = policy[role];
  if (!table) return null;
  const chosen = table[size] || table[policy.default_size || "medium"] || "medium";
  return chosen;
}
