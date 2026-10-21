// Pre-flight budget gate shared by both CLI bridges.
import { creditsPolicyText, openaiCredits } from "./routing.mjs";
import { getRouting, getUsage } from "./usage.mjs";

// mayDrawCredits: the caller records credit-funded runs (balance before and after), so a red
// OpenAI window whose work routing funds from ChatGPT credits is allowed instead of refused.
export async function budgetGate(vendor, options = {}) {
  let routing;
  try { routing = await getRouting({ cwd: options.cwd }); } catch (err) { return { allowed: true, note: `usage unavailable (${err.message}); proceeding` }; }
  const decision = decide(vendor, routing, options);
  return decision.credit_funded ? confirmOnFreshReading(vendor, options) : decision;
}

function decide(vendor, routing, { force = false, frontier = false, mayDrawCredits = false }) {
  const p = routing.resolved.postures[vendor];
  if (!p || !p.available) return { allowed: true, note: `${vendor} usage unknown; proceeding`, routing };
  const level = frontier ? p.frontier : p.overall;
  if (level === "red" && !force && mayDrawCredits && !frontier && p.credit_funded) {
    return { allowed: true, level, note: `${vendor} budget is red; drawing ChatGPT credits (${creditsPolicyText(routing.creditsPolicy)}).`, credit_funded: true, credits_before: openaiCredits(p.detail).balance, routing };
  }
  if (level === "red" && !force) {
    const d = p.detail;
    const resets = vendor === "anthropic" ? (d.session?.resets_at || d.weekly_by_model?.fable?.resets_at) : (d.five_hour?.resets_at || d.weekly?.resets_at);
    return { allowed: false, level, resets_at: resets, note: `${vendor} budget is red; refusing to start (pass --force to override). Resets ${resets}.`, routing };
  }
  return { allowed: true, level, routing };
}

// The cached reading behind routing can be minutes old, so a credit draw (OpenAI only) is
// decided again on a fresh one: the floor is checked against the balance actually left. With
// no fresh Codex reading the draw is refused (fail closed), as credits are money.
async function confirmOnFreshReading(vendor, options) {
  let routing;
  try { routing = await getRouting({ cwd: options.cwd, refresh: true }); } catch (err) { return refuseUnconfirmedDraw(vendor, err.message); }
  const { codex } = routing.usage;
  if (!codex?.ok || codex.read_error) return { ...refuseUnconfirmedDraw(vendor, codex?.read_error ?? codex?.error), routing };
  return decide(vendor, routing, options);
}

const refuseUnconfirmedDraw = (vendor, why) => ({ allowed: false, level: "red", note: `${vendor} budget is red and the ChatGPT credits balance could not be re-read (${why}); refusing to draw credits (pass --force to override).` });

// The ChatGPT credits balance from a fresh usage reading, for the run record of a credit-funded
// run; null when it cannot be read (a failed refresh keeps the last good reading, marked
// read_error, whose balance is stale) or the credits are unlimited.
export async function freshCreditsBalance() {
  try {
    const { codex } = await getUsage({ refresh: true });
    return codex?.read_error ? null : openaiCredits(codex)?.balance ?? null;
  } catch { return null; }
}
