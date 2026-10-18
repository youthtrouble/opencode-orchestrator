// Pre-flight budget gate shared by both CLI bridges.
import { getRouting } from "./usage.mjs";

export async function budgetGate(vendor, { force = false, frontier = false } = {}) {
  let routing;
  try { routing = await getRouting({}); } catch (err) { return { allowed: true, note: `usage unavailable (${err.message}); proceeding` }; }
  const p = routing.resolved.postures[vendor];
  if (!p || !p.available) return { allowed: true, note: `${vendor} usage unknown; proceeding`, routing };
  const level = frontier ? p.frontier : p.overall;
  if (level === "red" && !force) {
    const d = p.detail;
    const resets = vendor === "anthropic" ? (d.session?.resets_at || d.weekly_by_model?.fable?.resets_at) : (d.five_hour?.resets_at || d.weekly?.resets_at);
    return { allowed: false, level, resets_at: resets, note: `${vendor} budget is red; refusing to start (pass --force to override). Resets ${resets}.`, routing };
  }
  return { allowed: true, level, routing };
}
