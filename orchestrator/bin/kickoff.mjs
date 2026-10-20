#!/usr/bin/env node
// One command to start a run: preflight, pick the door by live budget, launch the conductor.
//   kickoff.mjs --brief .orchestrator/briefs/X.md [--brief Y.md ...] [--door auto|claude|opencode] [--headless] [--unattended] [--cwd DIR] [--dry-run]
// Preflight: refresh usage, ensure supervisor running, add .orchestrator/ to .git/info/exclude.
// --dry-run changes nothing: it prints the door and prompt and skips the preflight and the launch.
// Door "auto": the home door if its vendor is usable, else the other. Both red => the preflight, the prompt
// saved as the conductor's brief, a pause naming it (the supervisor's relaunch at the reset starts from it)
// and exit 75; exit 1 when the supervisor did not start or the pause was not recorded.
import fs from "node:fs";
import path from "node:path";
import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { parseArgs } from "../lib/run-record.mjs";
import { getRouting } from "../lib/usage.mjs";
import { DOOR_VENDOR, otherDoor } from "../lib/flip.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const briefs = []; for (let i = 0; i < argv.length; i++) if (argv[i] === "--brief" && argv[i + 1]) briefs.push(argv[++i]);
const { opts } = parseArgs(argv.filter((a, i, arr) => !(a === "--brief" || arr[i - 1] === "--brief")), { valueKeys: ["door", "cwd"], flagKeys: ["headless", "dry-run", "help", "unattended"] });
const root = path.resolve(opts.cwd || process.cwd());
if (opts.help || !briefs.length) { console.log("usage: kickoff.mjs --brief FILE [--brief FILE2] [--door auto|claude|opencode] [--headless] [--unattended] [--dry-run]"); process.exit(briefs.length ? 0 : 2); }
for (const b of briefs) if (!fs.existsSync(path.resolve(root, b))) { console.error(`brief not found: ${b} (looked in ${root}; cd into the project or pass --cwd <project dir>)`); process.exit(2); }

const r = await getRouting({ refresh: true });
const home = r.registry.conductor?.home || "opencode";
const usable = (door) => { const p = r.resolved.postures[DOOR_VENDOR[door]]; return p?.available && p.overall !== "red"; };
let door = opts.door && opts.door !== "auto" ? opts.door : usable(home) ? home : usable(otherDoor(home)) ? otherDoor(home) : null;
console.log(r.prompt.split("\n").slice(0, 2).join("\n"));
const [main, ...side] = briefs;
const prompt = `Ship ${main}. Plan first with the architect (size per the brief), then one slice at a time; flip or hand back per the contract at each brief boundary.${side.length ? ` When budget allows, also run ${side.join(", ")} as side brief(s) with the cheapest adequate role.` : ""} Record orchestrator defects as incidents.`;
if (!door) {
  if (opts["dry-run"]) { console.log("dry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset."); process.exit(75); }
  // The gate clear in the preflight drops any older pause, so it must run before this one is recorded.
  // Exit 75 promises a relaunch at the reset, which needs both a running supervisor and the pause.
  if (!preflight()) notDeferred("the supervisor did not start");
  const briefFile = writeKickoffBrief();
  const pause = spawnSync(process.execPath, [path.join(HERE, "flip.mjs"), "pause", "--active", home, "--brief", briefFile, "--cwd", root], { stdio: "inherit" });
  if (pause.status !== 0) notDeferred("the pause was not recorded");
  console.log("Both vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do.");
  process.exit(75);
}
// A dry run keeps the supervisor line, so its output has the shape of a real run's.
if (opts["dry-run"]) console.log("supervisor: not started (dry-run)");
else preflight();
console.log(`door: ${door} (${DOOR_VENDOR[door]} ${r.resolved.postures[DOOR_VENDOR[door]].overall}; home ${home})${opts.headless ? " headless" : " interactive"}`);
if (opts["dry-run"]) { console.log(`prompt: ${prompt}`); process.exit(0); }
if (opts.headless) {
  const w = spawnSync(process.execPath, [path.join(HERE, "worker.mjs"), "start", "--brief", writeKickoffBrief(), "--lane", "conductor", "--harness", door, "--agent", "conductor", "--breadth", "broad", "--cwd", root, "--detach", ...(door === "claude" ? ["--permission-mode", "auto"] : [])], { encoding: "utf8" });
  console.log((w.stdout || w.stderr).trim());
  console.log(`watch with: node ~/.config/opencode/orchestrator/bin/watch.mjs --follow`);
  process.exit(w.status ?? 1);
}
const cmd = door === "claude" ? ["claude", ["--agent", "conductor", prompt]] : ["opencode", ["--prompt", prompt]];
console.log(`launching: ${cmd[0]} ... (watch in another terminal: node ~/.config/opencode/orchestrator/bin/watch.mjs --follow)`);
const child = spawn(cmd[0], cmd[1], { cwd: root, stdio: "inherit" });
child.on("close", (code) => process.exit(code ?? 0));

// Preflight, never for a dry run (a human is running this, so any human gate is cleared). --no-relaunch:
// kickoff starts the conductor itself, so the clear also drops any queued relaunch or pause, which would
// otherwise let the supervisor start an older conductor or a second one beside an interactive kickoff.
// Returns whether the supervisor is running (`start` exits 0 when it started one or found one).
function preflight() {
  if (opts.unattended) spawnSync(process.execPath, [path.join(HERE, "policy.mjs"), "unattended", "--cwd", root], { stdio: "ignore" });
  spawnSync(process.execPath, [path.join(HERE, "flip.mjs"), "gate", "--clear", "--no-relaunch", "--cwd", root], { stdio: "ignore" });
  const sup = spawnSync(process.execPath, [path.join(HERE, "supervisor.mjs"), "start", "--cwd", root], { encoding: "utf8" });
  console.log(`supervisor: ${(sup.stdout || sup.stderr).trim().slice(0, 120)}`);
  const info = path.join(root, ".git", "info");
  if (fs.existsSync(path.join(root, ".git"))) { fs.mkdirSync(info, { recursive: true }); const f = path.join(info, "exclude"); const cur = fs.existsSync(f) ? fs.readFileSync(f, "utf8") : ""; if (!cur.split("\n").some((l) => /^\.orchestrator\/?\r?$/.test(l))) fs.appendFileSync(f, (cur && !cur.endsWith("\n") ? "\n" : "") + ".orchestrator/\n"); }
  return sup.status === 0;
}

// A both-red kickoff that could not set up the relaunch fails as an ordinary error (exit 1), so nobody
// leaves the run to a supervisor that will not start it.
function notDeferred(what) {
  console.error(`Both vendors are red, but ${what}: nothing will relaunch at the reset. Fix it and run kickoff again.`);
  process.exit(1);
}

// Saves the prompt as the conductor's brief and returns its path relative to the project.
function writeKickoffBrief() {
  const briefFile = path.join(root, ".orchestrator", "briefs", "conductor-kickoff.md"); fs.mkdirSync(path.dirname(briefFile), { recursive: true }); fs.writeFileSync(briefFile, prompt + "\n");
  return path.relative(root, briefFile);
}
