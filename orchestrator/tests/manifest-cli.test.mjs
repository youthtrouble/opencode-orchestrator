import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const MANIFEST_CLI = fileURLToPath(new URL("../bin/manifest.mjs", import.meta.url));

// A project with one file and a preload that makes crypto.randomInt return `draws` in order (the
// CLI imports it by name, so the builtin's ESM exports are resynced), then 1 for ever.
function sandbox(t, draws) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "manifest-cli-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.writeFileSync(path.join(root, "a.txt"), "a\n");
  const preload = path.join(root, "draws.mjs");
  fs.writeFileSync(preload, [
    `import crypto from "node:crypto";`,
    `import { syncBuiltinESMExports } from "node:module";`,
    `const draws = ${JSON.stringify(draws)};`,
    `crypto.randomInt = () => draws.length ? draws.shift() : 1;`,
    `syncBuiltinESMExports();`,
  ].join("\n"));
  const out = path.join(root, "m.json");
  const create = () => spawnSync(process.execPath, ["--import", preload, MANIFEST_CLI, "create", "--root", root, "--task", "t", "--author", "a", "--out", out, "a.txt"], { encoding: "utf8" });
  return { out, create };
}

test("create keeps another writer's temp on a name collision and writes under a fresh name", (t) => {
  const { out, create } = sandbox(t, [7, 8]);
  fs.writeFileSync(`${out}.tmp-7`, "another writer's temp\n");
  const r = create();
  assert.equal(r.status, 0, r.stderr);
  assert.equal(fs.readFileSync(`${out}.tmp-7`, "utf8"), "another writer's temp\n");
  assert.equal(JSON.parse(fs.readFileSync(out, "utf8")).manifest_sha256, JSON.parse(r.stdout).manifest_sha256);
  assert.equal(fs.existsSync(`${out}.tmp-8`), false);
});

test("create gives up after repeated collisions without touching the existing temp", (t) => {
  const { out, create } = sandbox(t, []);
  fs.writeFileSync(`${out}.tmp-1`, "another writer's temp\n");
  const r = create();
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /EEXIST/);
  assert.equal(fs.readFileSync(`${out}.tmp-1`, "utf8"), "another writer's temp\n");
  assert.equal(fs.existsSync(out), false);
});
