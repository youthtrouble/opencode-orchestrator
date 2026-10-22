// Conductor flip policy. Doors: "opencode" (OpenAI-native conductor) and "claude" (Anthropic-native).
import fs from "node:fs";
import path from "node:path";

export const DOOR_VENDOR = { opencode: "openai", claude: "anthropic" };
export const otherDoor = (door) => (door === "opencode" ? "claude" : "opencode");

function minutes(ms) { return ms / 60000; }
function windowFreshlyReset(detail, vendor, nowMs, graceMin) {
  // A window that reset less than `grace` minutes ago is not yet "stable"; infer from resets_at and window length.
  const win = vendor === "openai" ? (detail?.five_hour?.applicable === false ? detail?.weekly : detail?.five_hour) : detail?.session;
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

// Clearing a human gate removes `awaiting_human` and, when a gate was set and `relaunch` is on, makes sure
// a `pending_launch` is queued: the supervisor's pending-launch branch starts that conductor once the lane
// is idle and unlocked, and `flip run` drops the record, so it fires once. A `pending_launch` already
// queued (a flip that found the lane locked before the gate) is kept rather than replaced by the gated
// door: it is the handover the conductor asked for, and the supervisor launches it all the same. With
// `relaunch` off (kickoff, which launches the conductor itself) any queued `pending_launch` and any
// `paused` record are removed too, so the supervisor cannot start an older conductor first or a second
// one beside an interactive kickoff. Without a gate the record is otherwise written back unchanged (any
// JSON shape). Returns the door the supervisor will launch, or null when nothing was queued.
export function clearGate(root, flip, { relaunch = true } = {}) {
  const gate = flip?.awaiting_human;
  if (flip !== null && typeof flip === "object") {
    delete flip.awaiting_human;
    if (!relaunch) { delete flip.pending_launch; delete flip.paused; }
  }
  let door = null;
  if (gate && relaunch) {
    const gatedDoor = gate.door || "opencode";
    flip.pending_launch ||= { to: gatedDoor, from: gatedDoor, reason: "human gate cleared", resume: null, at: new Date().toISOString() };
    door = flip.pending_launch.to || null;
  }
  writeFlip(root, flip);
  return door;
}

// Replaces .orchestrator/flip.json through a temp file and a rename, so the supervisor polling it never
// reads a partly written record.
export function writeFlip(root, flip) {
  const file = path.join(root, ".orchestrator", "flip.json");
  const tmp = file + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(flip, null, 2) + "\n");
  fs.renameSync(tmp, file);
}

// Keeps the project's orchestrator state out of git: adds ".orchestrator/" to the repository's
// info/exclude unless a line already names it (Git ends lines only at \n and drops one trailing \r).
// Returns whether the project has a git repository; nothing is written when it has none.
export function ensureGitExclude(root) {
  const file = gitExcludeFile(root);
  if (!file) return false;
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const current = fs.existsSync(file) ? fs.readFileSync(file, "utf8") : "";
  if (!current.split("\n").some((l) => /^\.orchestrator\/?\r?$/.test(l))) fs.appendFileSync(file, (current.endsWith("\n") || !current ? "" : "\n") + ".orchestrator/\n");
  return true;
}

// Starts a .git file: a linked worktree's or a submodule's pointer to its git dir.
const GIT_FILE_PREFIX = "gitdir: ";

// The info/exclude git reads (gitrepository-layout): <root>/.git/info/exclude when .git is a directory.
// A .git file (a linked worktree or a submodule) holds "gitdir: <path>", relative to root as git resolves
// it; a git dir with a commondir file (a linked worktree) uses the common dir's exclude, commondir being
// relative to the git dir, otherwise (a submodule) its own. A malformed .git file, one naming no
// directory, or a commondir naming no directory is no repository, as git judges it (null), so nothing
// is created outside the repository.
function gitExcludeFile(root) {
  const dotGit = path.join(root, ".git");
  if (!fs.existsSync(dotGit)) return null;
  if (isDirectory(dotGit)) return path.join(dotGit, "info", "exclude");
  const pointer = readGitPath(dotGit);
  if (!pointer.startsWith(GIT_FILE_PREFIX)) return null;
  const gitDir = path.resolve(root, pointer.slice(GIT_FILE_PREFIX.length));
  if (!isDirectory(gitDir)) return null;
  const commonDirFile = path.join(gitDir, "commondir");
  if (!fs.existsSync(commonDirFile)) return path.join(gitDir, "info", "exclude");
  const commonDir = path.resolve(gitDir, readGitPath(commonDirFile));
  if (!isDirectory(commonDir)) return null;
  return path.join(commonDir, "info", "exclude");
}

// A .git or commondir file's content as git reads it: only trailing CR/LF bytes end it; a trailing
// space or tab belongs to the path, so "meta \n" names "meta ", not "meta".
function readGitPath(file) {
  return fs.readFileSync(file, "utf8").replace(/[\r\n]+$/, "");
}

// Like fs.existsSync, any stat failure (missing, a dangling symlink, a file used as a directory) is "no".
function isDirectory(p) {
  try { return fs.statSync(p).isDirectory(); } catch { return false; }
}
