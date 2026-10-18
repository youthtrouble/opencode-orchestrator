// Conductor flip policy (pure). Doors: "opencode" (OpenAI-native conductor) and "claude" (Anthropic-native).
export const DOOR_VENDOR = { opencode: "openai", claude: "anthropic" };
export const otherDoor = (door) => (door === "opencode" ? "claude" : "opencode");

function minutes(ms) { return ms / 60000; }
function windowFreshlyReset(detail, vendor, nowMs, graceMin) {
  // A window that reset less than `grace` minutes ago is not yet "stable"; infer from resets_at and window length.
  const win = vendor === "openai" ? detail?.five_hour : detail?.session;
  if (!win?.resets_at) return false;
  const lengthMin = win.window_mins || 300;
  const untilReset = minutes(Date.parse(win.resets_at) - nowMs);
  return untilReset > lengthMin - graceMin;
}

export function decideFlip({ active, postures, registry, nowMs = Date.now(), lastFlipAtMs = null }) {
  const cfg = registry.conductor || { home: "opencode", green_grace_minutes: 10, flip_cooldown_minutes: 10 };
  const home = cfg.home || "opencode";
  const other = otherDoor(active);
  const own = postures[DOOR_VENDOR[active]];
  const them = postures[DOOR_VENDOR[other]];
  const ownRed = !own?.available || own.overall === "red";
  const themUsable = them?.available && them.overall !== "red";
  const cooling = lastFlipAtMs !== null && minutes(nowMs - lastFlipAtMs) < (cfg.flip_cooldown_minutes ?? 10);

  if (ownRed) {
    if (themUsable) return { action: "flip", to: other, reason: `${DOOR_VENDOR[active]} is red; ${DOOR_VENDOR[other]} has headroom` };
    return { action: "pause", to: null, reason: "both vendors red or unknown; wait for the nearest reset" };
  }
  if (active !== home) {
    const homeP = postures[DOOR_VENDOR[home]];
    const stable = homeP?.available && homeP.overall === "green" && !windowFreshlyReset(homeP.detail, DOOR_VENDOR[home], nowMs, cfg.green_grace_minutes ?? 10);
    if (stable && !cooling) return { action: "handback", to: home, reason: `home door ${home} is stably green again` };
    return { action: "stay", to: active, reason: stable ? "cooldown after a recent flip" : `home door ${home} not stably green yet` };
  }
  return { action: "stay", to: active, reason: "home door with headroom" };
}
