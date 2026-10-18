#!/usr/bin/env node
// Conductor flip: decide and execute moving the project's conductor between the two front doors.
//   flip.mjs decide --active opencode|claude [--cwd DIR]        -> JSON {action: flip|handback|stay|pause, to, reason}
//   flip.mjs run --to opencode|claude [--cwd DIR] [--from X] [--reason TEXT]
//   flip.mjs status [--cwd DIR]
// `run` posts a handoff to the mailbox, records .orchestrator/flip.json, and starts the other door's conductor
// headless as worker lane "conductor" (lane lock => one driver per project). Adds .orchestrator/ to .git/info/exclude.
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { parseArgs } from "../lib/run-record.mjs";
import { getRouting } from "../lib/usage.mjs";
import { decideFlip, otherDoor } from "../lib/flip.mjs";
import { post } from "../lib/mailbox.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const { opts, positional } = parseArgs(process.argv.slice(2), { valueKeys: ["active", "to", "from", "cwd", "reason"], flagKeys: ["force"] });
const cmd = positional[0];
const root = path.resolve(opts.cwd || process.cwd());
const FLIP = path.join(root, ".orchestrator", "flip.json");
const readFlip = () => { try { return JSON.parse(fs.readFileSync(FLIP, "utf8")); } catch { return null; } };

export function ensureGitExclude(projectRoot) {
  const info = path.join(projectRoot, ".git", "info");
  if (!fs.existsSync(path.join(projectRoot, ".git"))) return false;
  fs.mkdirSync(info, { recursive: true });
  const file = path.join(info, "exclude");
  const current = fs.existsSync(file) ? fs.readFileSync(file, "utf8") : "";
  if (!/^\.orchestrator\/?$/m.test(current)) fs.appendFileSync(file, (current.endsWith("\n") || !current ? "" : "\n") + ".orchestrator/\n");
  return true;
}

if (cmd === "status") { console.log(JSON.stringify(readFlip() || { state: "never flipped" }, null, 2)); process.exit(0); }
if (cmd === "decide") {
  if (!opts.active) { console.error("--active opencode|claude required"); process.exit(2); }
  const r = await getRouting({});
  const prev = readFlip();
  const d = decideFlip({ active: opts.active, postures: r.resolved.postures, registry: r.registry, lastFlipAtMs: prev?.at ? Date.parse(prev.at) : null });
  console.log(JSON.stringify(d));
  process.exit(0);
}
if (cmd === "run") {
  const to = opts.to; if (!["opencode", "claude"].includes(to)) { console.error("--to opencode|claude required"); process.exit(2); }
  const from = opts.from || otherDoor(to);
  ensureGitExclude(root);
  const briefsDir = path.join(root, ".orchestrator", "briefs"); fs.mkdirSync(briefsDir, { recursive: true });
  const brief = path.join(briefsDir, "conductor-continue.md");
  fs.writeFileSync(brief, `# Continue as conductor (${to} door)\n\nYou are taking over this project from the ${from} conductor (${opts.reason || "vendor budget flip"}).\nRead .orchestrator/STATE.md and the mailbox, verify recorded facts against the tree, then execute the exact next action and keep following the loop.\nAt each brief boundary run: node ~/.config/opencode/orchestrator/bin/flip.mjs decide --active ${to}\nIf it says handback or flip: update STATE.md, post a handoff, run flip.mjs run --to <door> --from ${to}, and stop.\nYou cannot ask the human interactively: put questions in the mailbox (kind question) and continue with independent work, or stop at human gates.\n`);
  const prev = readFlip();
  const record = { at: new Date().toISOString(), from, to, reason: opts.reason || null, previous: prev ? { at: prev.at, to: prev.to } : null };
  fs.writeFileSync(FLIP, JSON.stringify(record, null, 2) + "\n");
  try { post(root, { from: `conductor-${from}`, to: `conductor-${to}`, kind: "handoff", subject: `conductor flip ${from} -> ${to}`, body: opts.reason || "vendor budget flip", refs: [".orchestrator/STATE.md", path.relative(root, brief)] }); } catch { /* mailbox optional */ }
  const args = [path.join(HERE, "worker.mjs"), "start", "--brief", path.relative(root, brief), "--lane", "conductor", "--harness", to, "--agent", "conductor", "--breadth", "broad", "--cwd", root, "--detach"];
  if (to === "claude") args.push("--permission-mode", "auto");
  if (opts.force) args.push("--force");
  const r = spawnSync(process.execPath, args, { cwd: root, encoding: "utf8" });
  console.log(JSON.stringify({ flipped: r.status === 0, to, from, launcher: (r.stdout || r.stderr || "").trim().slice(0, 400) }));
  process.exit(r.status === 0 ? 0 : 1);
}
console.error("commands: decide | run | status"); process.exit(2);
