#!/usr/bin/env node
// Health check for the orchestrator: binaries, auth, usage feeds, opencode config, plugin, tests.
import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { redact } from "../lib/incident.mjs";
import { creditsPolicyText, creditsText, openaiCredits } from "../lib/routing.mjs";
import { getRouting } from "../lib/usage.mjs";

const rows = [];
const ok = (name, detail = "") => rows.push(["OK  ", name, detail]);
const warn = (name, detail = "") => rows.push(["WARN", name, detail]);
const fail = (name, detail = "") => rows.push(["FAIL", name, detail]);
const sh = (cmd, args) => { const r = spawnSync(cmd, args, { encoding: "utf8", timeout: 20000 }); return { code: r.status, out: (r.stdout || "") + (r.stderr || "") }; };

for (const [bin, args] of [["opencode", ["--version"]], ["codex", ["--version"]], ["claude", ["--version"]], ["node", ["--version"]]]) {
  const r = sh(bin, args); r.code === 0 ? ok(`${bin} present`, r.out.trim().split("\n")[0]) : fail(`${bin} missing`, "install it");
}
// Same row as `orch doctor` (PE-18). An explicit ORCH_BIN never falls through to the defaults.
const isExecutableFile = (f) => { try { fs.accessSync(f, fs.constants.X_OK); return fs.statSync(f).isFile(); } catch { return false; } };
const orchCandidates = [path.join(os.homedir(), ".local", "bin", "orch"), ...(process.env.PATH || "").split(":").filter(Boolean).map((dir) => path.join(dir, "orch"))];
const orchBin = process.env.ORCH_BIN ? (isExecutableFile(process.env.ORCH_BIN) ? process.env.ORCH_BIN : null) : orchCandidates.find(isExecutableFile);
if (process.env.ORCH_BIN && !orchBin) fail("orch binary", `ORCH_BIN=${process.env.ORCH_BIN} is not an executable file`);
else if (!orchBin) warn("orch binary", 'not found (runtime js only; see README "Go runtime")');
else { const r = spawnSync(orchBin, ["version"], { encoding: "utf8", timeout: 20000 }); r.status === 0 ? ok("orch binary", `${(r.stdout || "").trim().split("\n")[0]} at ${orchBin}`) : fail("orch binary", `${orchBin} version exited ${r.status ?? r.signal}`); }
const codexLogin = sh("codex", ["login", "status"]); /Logged in/.test(codexLogin.out) ? ok("codex login", codexLogin.out.trim()) : warn("codex not logged in", "run: codex login");
const claudeAuth = sh("claude", ["auth", "status"]); /loggedIn"?:\s*true|logged in/i.test(claudeAuth.out) ? ok("claude login") : warn("claude auth status unclear", claudeAuth.out.trim().slice(0, 120));
const oc = sh("opencode", ["auth", "list"]); /openai/i.test(oc.out) ? ok("opencode: openai connected") : warn("opencode: OpenAI not connected", "run: opencode auth login openai --method chatgpt-token-sharing");
/anthropic/i.test(oc.out) ? ok("opencode: anthropic connected (API key)") : ok("opencode: anthropic not native (expected; Claude runs via claude-exec bridge)");

try {
  const r = await getRouting({ refresh: true });
  r.usage.claude.ok ? ok("Claude usage feed", `session ${r.usage.claude.session.pct}% fable ${r.usage.claude.weekly_by_model?.fable?.pct ?? "n/a"}% via ${r.usage.claude.source}`) : fail("Claude usage feed", r.usage.claude.error);
  const codexPct = (window) => window.applicable === false ? "n/a" : `${window.pct}%`;
  r.usage.codex.ok ? ok("Codex usage feed", `5h ${codexPct(r.usage.codex.five_hour)} weekly ${codexPct(r.usage.codex.weekly)} plan ${r.usage.codex.plan}`) : fail("Codex usage feed", r.usage.codex.error);
  const credits = openaiCredits(r.usage.codex);
  if (credits) ok("OpenAI credits", `${creditsText(credits)}; policy ${creditsPolicyText(r.creditsPolicy)}`);
  ok("routing", Object.entries(r.resolved.roles).map(([k, v]) => `${k}=${v ? v.model.split("/")[1] + "@" + v.via : "PAUSED"}`).join(" "));
} catch (e) { fail("usage/routing", String(e.message || e)); }

const cfg = path.join(os.homedir(), ".config", "opencode", "opencode.jsonc");
fs.existsSync(cfg) ? ok("opencode.jsonc present") : fail("opencode.jsonc missing");
const plugins = sh("opencode", ["plugin", "list"]); /orchestrator/.test(plugins.out) ? ok("plugin registered") : fail("plugin not listed", "check ~/.config/opencode/plugins/orchestrator.ts and the log");
const agents = sh("opencode", ["debug", "agents"]);
try { const ids = JSON.parse(agents.out).map((a) => a.id); ["conductor", "architect-oai", "coder-oai", "reviewer-oai"].every((id) => ids.includes(id)) ? ok("agents loaded", ids.join(",")) : fail("agents missing", ids.join(",")); } catch { fail("opencode debug agents", agents.out.slice(0, 200)); }
const tests = sh("node", ["--test", ...fs.readdirSync(path.join(path.dirname(new URL(import.meta.url).pathname), "..", "tests")).map((f) => path.join(path.dirname(new URL(import.meta.url).pathname), "..", "tests", f))]);
/fail (\d+)/.test(tests.out) && RegExp.$1 === "0" ? ok("unit tests", (tests.out.match(/pass (\d+)/) || [])[0]) : fail("unit tests", (tests.out.match(/fail \d+/) || ["?"])[0]);
const log = path.join(os.homedir(), ".local", "share", "opencode", "log", "opencode.log");
try { const tail = fs.readFileSync(log, "utf8").split("\n").slice(-400).filter((l) => /orchestrator/.test(l) && /ERROR/.test(l)); tail.length ? warn("plugin errors in log", redact(tail.slice(-3).join(" | ")).slice(0, 300)) : ok("no plugin errors in recent log"); } catch { /* no log */ }

for (const [s, n, d] of rows) console.log(`${s} ${n}${d ? "  —  " + d : ""}`);
process.exit(rows.some((r) => r[0] === "FAIL") ? 1 : 0);
