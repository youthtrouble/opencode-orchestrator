#!/usr/bin/env node
// Effective autonomy policy for a project: models.json autonomy merged with <project>/.orchestrator/policy.json.
//   policy.mjs show [--cwd DIR]            policy.mjs set key=value [key=value...] [--cwd DIR]
//   policy.mjs unattended [--cwd DIR]      (push_to_main=auto, product_questions=continue_with_assumption)
import fs from "node:fs";
import path from "node:path";
import { loadRegistry } from "../lib/usage.mjs";
const argv = process.argv.slice(2); const cmd = argv[0];
const ci = argv.indexOf("--cwd"); const root = path.resolve(ci >= 0 ? argv[ci + 1] : process.cwd());
const file = path.join(root, ".orchestrator", "policy.json");
const read = () => { try { return JSON.parse(fs.readFileSync(file, "utf8")); } catch { return {}; } };
const effective = () => { const base = { ...(loadRegistry().autonomy || {}) }; delete base.$comment; return { ...base, ...read() }; };
if (cmd === "show" || !cmd) console.log(JSON.stringify(effective(), null, 2));
else if (cmd === "set" || cmd === "unattended") {
  const cur = read();
  const pairs = cmd === "unattended" ? ["push_to_main=auto", "product_questions=continue_with_assumption"] : argv.slice(1).filter((a) => a.includes("="));
  for (const kv of pairs) { const [k, v] = kv.split("="); if (v === "never" && k !== "force_push") { console.error(`refusing ${k}=never`); process.exit(2); } cur[k] = v; }
  if ("force_push" in cur && cur.force_push !== "never") { console.error("force_push is always never"); process.exit(2); }
  fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, JSON.stringify(cur, null, 2) + "\n");
  console.log(JSON.stringify(effective(), null, 2));
} else { console.error("commands: show | set key=value | unattended"); process.exit(2); }
