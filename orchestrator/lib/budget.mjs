// Pre-flight budget gate shared by both CLI bridges.
import { creditsPolicyText, openaiCredits } from "./routing.mjs";
import { getRouting, getUsage } from "./usage.mjs";

// mayDrawCredits: the caller records credit-funded runs (balance before and after), so a red
// OpenAI window whose work routing funds from ChatGPT credits is allowed instead of refused.
export async function budgetGate(vendor, { force = false, frontier = false, cwd, mayDrawCredits = false } = {}) {
  let routing;
  try { routing = await getRouting({ cwd }); } catch (err) { return { allowed: true, note: `usage unavailable (${err.message}); proceeding` }; }
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

// The ChatGPT credits balance from a fresh usage reading, for the run record of a credit-funded
// run; null when it cannot be read (or the credits are unlimited).
export async function freshCreditsBalance() {
  try { return openaiCredits((await getUsage({ refresh: true })).codex)?.balance ?? null; } catch { return null; }
}
