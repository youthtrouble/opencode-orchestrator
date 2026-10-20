import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { isProject } from "../lib/project.mjs";

test("isProject is true only where .orchestrator is a directory", (t) => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "project-"));
  t.after(() => fs.rmSync(tmp, { recursive: true, force: true }));
  const [project, empty, stray] = ["project", "empty", "stray"].map((name) => path.join(tmp, name));
  fs.mkdirSync(path.join(project, ".orchestrator"), { recursive: true });
  fs.mkdirSync(empty);
  fs.mkdirSync(stray);
  fs.writeFileSync(path.join(stray, ".orchestrator"), "");

  assert.equal(isProject(project), true);
  assert.equal(isProject(empty), false);
  assert.equal(isProject(stray), false);
  assert.equal(isProject(path.join(tmp, "missing")), false);
});

test("isProject is false under a file and rethrows a stat failure that is not absence", { skip: process.platform === "win32" || process.getuid?.() === 0 }, (t) => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "project-"));
  t.after(() => { fs.chmodSync(locked, 0o700); fs.rmSync(tmp, { recursive: true, force: true }); });
  const file = path.join(tmp, "file");
  fs.writeFileSync(file, "");
  const locked = path.join(tmp, "locked");
  fs.mkdirSync(locked);
  fs.chmodSync(locked, 0o000);

  assert.equal(isProject(file), false);
  assert.throws(() => isProject(locked), { code: "EACCES" });
});
