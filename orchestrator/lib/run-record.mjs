// Every delegated run (codex exec / claude -p) leaves a compact JSON record under
// <project>/.orchestrator/runs/ so any agent or human can audit what was asked, what came back,
// and what it cost, without re-reading raw logs.
import fs from "node:fs";
import path from "node:path";

export function runsDir(projectRoot) {
  return path.join(projectRoot, ".orchestrator", "runs");
}

export function writeRunRecord(projectRoot, record) {
  const dir = runsDir(projectRoot);
  fs.mkdirSync(dir, { recursive: true });
  const stamp = (record.started_at || new Date().toISOString()).replace(/[:.]/g, "-");
  const id = (record.thread_id || record.session_id || "run").slice(0, 8);
  const file = path.join(dir, `${stamp}-${record.harness}-${id}.json`);
  fs.writeFileSync(file, JSON.stringify(record, null, 2) + "\n");
  return file;
}

export function parseArgs(argv, { valueKeys = [], flagKeys = [] } = {}) {
  const opts = {}; const positional = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith("--")) { positional.push(a); continue; }
    const key = a.slice(2);
    if (flagKeys.includes(key)) { opts[key] = true; continue; }
    const next = argv[i + 1];
    if (valueKeys.includes(key) && next !== undefined) { opts[key] = next; i++; continue; }
    opts[key] = true;
  }
  return { opts, positional };
}

export function readPrompt(opts, positional) {
  if (opts["prompt-file"]) return fs.readFileSync(path.resolve(opts["prompt-file"]), "utf8");
  if (opts.prompt) return opts.prompt;
  if (positional.length) return positional.join(" ");
  if (!process.stdin.isTTY) return fs.readFileSync(0, "utf8");
  throw new Error("no prompt: pass --prompt, --prompt-file, positional text, or stdin");
}
