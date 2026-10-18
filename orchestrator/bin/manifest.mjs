#!/usr/bin/env node
// manifest.mjs create --task T --author A [--baseline file.json] --out file.json <paths...>
// manifest.mjs verify file.json          (exit 1 on any mismatch)
// manifest.mjs diff before.json after.json
import fs from "node:fs";
import path from "node:path";
import { buildManifest, verifyManifest, diffManifests } from "../lib/manifest.mjs";

const [cmd, ...rest] = process.argv.slice(2);
const opts = {}; const positional = [];
for (let i = 0; i < rest.length; i++) {
  if (rest[i].startsWith("--")) { opts[rest[i].slice(2)] = rest[i + 1]; i++; } else positional.push(rest[i]);
}
const root = path.resolve(opts.root || process.cwd());
if (cmd === "create") {
  if (!opts.task || !opts.author || !opts.out || positional.length === 0) { console.error("usage: manifest.mjs create --task T --author A --out file.json [--baseline b.json] [--root dir] <paths...>"); process.exit(2); }
  const m = buildManifest({ root, paths: positional, task: opts.task, author: opts.author, baseline: opts.baseline || null });
  fs.mkdirSync(path.dirname(path.resolve(opts.out)), { recursive: true });
  fs.writeFileSync(path.resolve(opts.out), JSON.stringify(m, null, 2) + "\n");
  console.log(JSON.stringify({ out: opts.out, file_count: m.file_count, manifest_sha256: m.manifest_sha256 }));
} else if (cmd === "verify") {
  const m = JSON.parse(fs.readFileSync(path.resolve(positional[0]), "utf8"));
  const r = verifyManifest({ root, manifest: m });
  console.log(JSON.stringify(r));
  process.exit(r.ok ? 0 : 1);
} else if (cmd === "diff") {
  const a = JSON.parse(fs.readFileSync(path.resolve(positional[0]), "utf8"));
  const b = JSON.parse(fs.readFileSync(path.resolve(positional[1]), "utf8"));
  console.log(JSON.stringify(diffManifests(a, b), null, 2));
} else { console.error("commands: create | verify | diff"); process.exit(2); }
