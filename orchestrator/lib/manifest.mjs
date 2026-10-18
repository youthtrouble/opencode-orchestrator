// Source manifests: a JSON map of relative path -> sha256, so a reviewer can prove it read
// exactly the files the author released. Generalized from next-workspace/collaboration.
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

export function sha256File(file) {
  const h = crypto.createHash("sha256");
  h.update(fs.readFileSync(file));
  return h.digest("hex");
}

export function sha256String(s) {
  return crypto.createHash("sha256").update(s).digest("hex");
}

const IGNORED_DIRS = new Set(["node_modules", ".git", "dist", "build", ".next", ".expo", "__pycache__", ".orchestrator", "coverage", ".turbo", ".cache"]);

export function walk(dir, { root = dir, out = [] } = {}) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (IGNORED_DIRS.has(entry.name)) continue;
      walk(path.join(dir, entry.name), { root, out });
    } else if (entry.isFile()) {
      out.push(path.relative(root, path.join(dir, entry.name)));
    }
  }
  return out;
}

export function buildManifest({ root, paths, task, author, baseline = null }) {
  const files = {};
  const expanded = [];
  for (const p of paths) {
    const abs = path.resolve(root, p);
    if (!fs.existsSync(abs)) throw new Error(`manifest path does not exist: ${p}`);
    if (fs.statSync(abs).isDirectory()) expanded.push(...walk(abs, { root }));
    else expanded.push(path.relative(root, abs));
  }
  for (const rel of [...new Set(expanded)].sort()) files[rel] = sha256File(path.join(root, rel));
  const manifest = { task, author, baseline, created_at: new Date().toISOString(), file_count: Object.keys(files).length, files };
  manifest.manifest_sha256 = sha256String(JSON.stringify(files));
  return manifest;
}

export function verifyManifest({ root, manifest }) {
  const mismatched = [], missing = [];
  for (const [rel, expected] of Object.entries(manifest.files)) {
    const abs = path.join(root, rel);
    if (!fs.existsSync(abs)) { missing.push(rel); continue; }
    if (sha256File(abs) !== expected) mismatched.push(rel);
  }
  return { ok: mismatched.length === 0 && missing.length === 0, mismatched, missing, checked: Object.keys(manifest.files).length };
}

export function diffManifests(before, after) {
  const added = [], removed = [], changed = [];
  for (const k of Object.keys(after.files)) {
    if (!(k in before.files)) added.push(k);
    else if (before.files[k] !== after.files[k]) changed.push(k);
  }
  for (const k of Object.keys(before.files)) if (!(k in after.files)) removed.push(k);
  return { added, removed, changed, unchanged: Object.keys(after.files).length - added.length - changed.length };
}
