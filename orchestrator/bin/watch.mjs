#!/usr/bin/env node
// One-screen dashboard for a project: budgets, routing, lanes, flip state, recent mailbox, incidents, supervisor.
//   watch.mjs [--cwd DIR] [--follow] [--interval 20]
import fs from "node:fs";
import path from "node:path";
import { parseArgs } from "../lib/run-record.mjs";
import { getRouting } from "../lib/usage.mjs";
import { list as mailboxList } from "../lib/mailbox.mjs";
import { read as incidents } from "../lib/incident.mjs";

const { opts } = parseArgs(process.argv.slice(2), { valueKeys: ["cwd", "interval"], flagKeys: ["follow"] });
const root = path.resolve(opts.cwd || process.cwd());
const readJson = (f) => { try { return JSON.parse(fs.readFileSync(f, "utf8")); } catch { return null; } };
const alive = (pid) => { try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } };

async function render() {
  const out = [];
  const r = await getRouting({});
  out.push(`== ${path.basename(root)}  ${new Date().toISOString().slice(0, 19)}Z  (usage from ${r.usage.generated_at.slice(11, 19)}Z${r.usage.stale ? " STALE" : ""})`);
  out.push(...r.prompt.split("\n").slice(0, 2));
  const roles = Object.entries(r.resolved.roles).map(([k, v]) => `${k}=${v ? v.model.split("/")[1] + (v.effort ? "@" + v.effort : v.variant ? "#" + v.variant : "") : "PAUSED"}`).join("  ");
  out.push(`routing: ${roles}`);
  const sup = readJson(path.join(root, ".orchestrator", "supervisor-state.json"));
  out.push(`supervisor: ${sup ? `${sup.status} pid ${sup.pid}${alive(sup.pid) ? "" : " (DEAD)"} checked ${String(sup.checked_at || "").slice(11, 19)}Z` : "not started"}`);
  const flip = readJson(path.join(root, ".orchestrator", "flip.json"));
  out.push(`flip: ${flip ? (flip.paused ? `PAUSED (${flip.paused.door}, ${flip.paused.at.slice(11, 16)}Z)` : `${flip.from}->${flip.to} at ${flip.at.slice(11, 16)}Z`) : "never"}`);
  const lanesDir = path.join(root, ".orchestrator", "workers");
  const lanes = fs.existsSync(lanesDir) ? fs.readdirSync(lanesDir).filter((f) => f.endsWith(".json")).map((f) => readJson(path.join(lanesDir, f))).filter(Boolean) : [];
  out.push(`lanes: ${lanes.length ? lanes.map((l) => `${l.lane}[${l.harness}] ${l.status}${["running", "starting"].includes(l.status) ? (alive(l.pid) ? "" : " (DEAD)") : ""} ${l.last_tool ? "last " + l.last_tool : ""} ${String(l.last_progress_at || l.updated_at || "").slice(11, 16)}Z`).join(" | ") : "none"}`);
  const msgs = mailboxList(root, { limit: 8 });
  out.push(`mailbox (last ${msgs.length}):`);
  for (const m of msgs) out.push(`  ${m.at.slice(11, 16)}Z ${m.kind.padEnd(8)} ${m.from} -> ${m.to}: ${m.subject}`);
  const inc = incidents({ since: new Date(Date.now() - 24 * 3600 * 1000).toISOString() });
  out.push(`incidents (24h): ${inc.length}${inc.length ? "  latest: " + inc[inc.length - 1].summary.slice(0, 90) : ""}`);
  const state = path.join(root, ".orchestrator", "STATE.md");
  if (fs.existsSync(state)) { const next = fs.readFileSync(state, "utf8").split("## Next action")[1]?.split("##")[0]?.trim().split("\n")[0]; if (next) out.push(`next: ${next.slice(0, 160)}`); }
  return out.join("\n");
}
if (opts.follow) {
  const iv = Number(opts.interval || 20) * 1000;
  const loop = async () => { try { process.stdout.write("\x1b[2J\x1b[H" + (await render()) + "\n"); } catch (e) { console.error(e.message); } setTimeout(loop, iv); };
  loop();
} else console.log(await render());
