#!/usr/bin/env node
// incident.mjs record --component plugin|bridge|worker|supervisor|flip|usage|contract|agent --kind bug|limit|misroute|permission|crash|timeout|contract|human-correction|flip|note --summary "..." [--detail "..."|-] [--cwd DIR] [--ctx key=value]...
// incident.mjs list [--since ISO] [--kind K] [--component C] [--json]
// incident.mjs report [--since ISO]          grouped by fingerprint, most frequent first (what the audit skill reads)
// incident.mjs share [--since ISO] --out FILE  sanitized bundle (already redacted) for a GitHub issue; never uploads
import fs from "node:fs";
import { record, read, summarize, INCIDENTS } from "../lib/incident.mjs";

const [cmd, ...rest] = process.argv.slice(2);
const opts = { ctx: [] };
for (let i = 0; i < rest.length; i++) { const a = rest[i]; if (!a.startsWith("--")) continue; const k = a.slice(2); const v = rest[i + 1]; if (v === undefined || v.startsWith("--")) { opts[k] = true; continue; } if (k === "ctx") opts.ctx.push(v); else opts[k] = v; i++; }
if (cmd === "record") {
  let detail = opts.detail || ""; if (detail === "-") detail = fs.readFileSync(0, "utf8");
  const context = Object.fromEntries(opts.ctx.map((kv) => kv.split("=")));
  const e = record({ component: opts.component || "agent", kind: opts.kind || "note", summary: opts.summary, detail, context, project: opts.cwd || process.cwd() });
  console.log(JSON.stringify({ recorded: e.fp, at: e.at }));
} else if (cmd === "list") {
  const es = read({ since: opts.since, kind: opts.kind, component: opts.component });
  if (opts.json) console.log(JSON.stringify(es, null, 2)); else for (const e of es.slice(-100)) console.log(`${e.at} ${e.fp} ${e.component}/${e.kind} ${e.project || "-"}: ${e.summary}`);
} else if (cmd === "report") {
  const s = summarize(read({ since: opts.since }));
  if (!s.length) { console.log("(no incidents)"); process.exit(0); }
  for (const r of s) console.log(`${String(r.count).padStart(3)}x ${r.fp} ${r.component}/${r.kind} [${r.projects.join(",") || "-"}] ${r.summary}  (first ${r.first.slice(0, 16)}, last ${r.last.slice(0, 16)})`);
} else if (cmd === "share") {
  const es = read({ since: opts.since }).map(({ project, ...e }) => e); // drop even project basenames
  const out = opts.out || "incidents-share.json";
  fs.writeFileSync(out, JSON.stringify({ generated_at: new Date().toISOString(), incidents: es, summary: summarize(es) }, null, 2));
  console.log(`wrote ${out} (${es.length} incidents). Review it, then attach to a GitHub issue by hand. Nothing was uploaded.`);
} else { console.error(`commands: record | list | report | share   (log: ${INCIDENTS})`); process.exit(2); }
