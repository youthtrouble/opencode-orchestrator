import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { clearGate, decideFlip, ensureGitExclude } from "../lib/flip.mjs";

const flipCli = fileURLToPath(new URL("../bin/flip.mjs", import.meta.url));

const registry = { conductor: { home: "opencode", green_grace_minutes: 10, flip_cooldown_minutes: 10 } };
const now = Date.parse("2026-10-18T12:00:00Z");
const P = (openai, anthropic, { openaiResetInMin = 120, anthropicResetInMin = 120 } = {}) => ({
  openai: { available: true, overall: openai, detail: { five_hour: { resets_at: new Date(now + openaiResetInMin * 60000).toISOString(), window_mins: 300 } } },
  anthropic: { available: true, overall: anthropic, detail: { session: { resets_at: new Date(now + anthropicResetInMin * 60000).toISOString() } } },
});

test("own vendor red and other usable -> forced flip", () => {
  assert.equal(decideFlip({ active: "opencode", postures: P("red", "green"), registry, nowMs: now }).action, "flip");
  assert.equal(decideFlip({ active: "claude", postures: P("amber", "red"), registry, nowMs: now }).to, "opencode");
});
test("both red -> pause", () => {
  assert.equal(decideFlip({ active: "opencode", postures: P("red", "red"), registry, nowMs: now }).action, "pause");
});
test("away conductor hands back when home is stably green", () => {
  const d = decideFlip({ active: "claude", postures: P("green", "green"), registry, nowMs: now });
  assert.deepEqual([d.action, d.to], ["handback", "opencode"]);
});
test("home just reset (<grace) is not stable; amber home is not enough", () => {
  assert.equal(decideFlip({ active: "claude", postures: P("green", "green", { openaiResetInMin: 295 }), registry, nowMs: now }).action, "stay");
  assert.equal(decideFlip({ active: "claude", postures: P("amber", "green"), registry, nowMs: now }).action, "stay");
});
test("cooldown suppresses a hand-back but never a forced flip", () => {
  const recent = now - 2 * 60000;
  assert.equal(decideFlip({ active: "claude", postures: P("green", "green"), registry, nowMs: now, lastFlipAtMs: recent }).action, "stay");
  assert.equal(decideFlip({ active: "claude", postures: P("green", "red"), registry, nowMs: now, lastFlipAtMs: recent }).action, "flip");
});
test("home conductor with headroom stays", () => {
  assert.equal(decideFlip({ active: "opencode", postures: P("amber", "green"), registry, nowMs: now }).action, "stay");
});

function tempProject(t) {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "flip-gate-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  return project;
}
function flip(project, ...args) {
  const r = spawnSync(process.execPath, [flipCli, ...args, "--cwd", project], { encoding: "utf8" });
  assert.equal(r.status, 0, r.stderr);
  return r.stdout.trim();
}
const readFlipJson = (project) => JSON.parse(fs.readFileSync(path.join(project, ".orchestrator", "flip.json"), "utf8"));

test("gate --clear removes the gate and queues a relaunch of the gated door", (t) => {
  const project = tempProject(t);
  flip(project, "gate", "--active", "claude", "--reason", "push");

  assert.equal(flip(project, "gate", "--clear"), '{"awaiting_human":false,"relaunch":"claude"}');

  const record = readFlipJson(project);
  assert.equal(record.awaiting_human, undefined);
  assert.deepEqual({ ...record.pending_launch, at: "<iso>" }, { to: "claude", from: "claude", reason: "human gate cleared", resume: null, at: "<iso>" });
  assert.deepEqual(fs.readdirSync(path.join(project, ".orchestrator")).filter((f) => f.startsWith("flip.json")), ["flip.json"]);
});

test("gate --clear keeps a pending launch queued before the gate", (t) => {
  const project = tempProject(t);
  const queued = { to: "opencode", from: "claude", reason: "handback", resume: null, at: "2026-01-01T00:00:00.000Z" };
  fs.mkdirSync(path.join(project, ".orchestrator"));
  fs.writeFileSync(path.join(project, ".orchestrator", "flip.json"), JSON.stringify({ pending_launch: queued }));
  flip(project, "gate", "--active", "claude");

  assert.equal(flip(project, "gate", "--clear"), '{"awaiting_human":false,"relaunch":"opencode"}');

  assert.deepEqual(readFlipJson(project), { pending_launch: queued });
});

test("gate --clear without a gate queues nothing", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.join(project, ".orchestrator"));
  fs.writeFileSync(path.join(project, ".orchestrator", "flip.json"), '{"to":"claude"}');

  assert.equal(flip(project, "gate", "--clear"), '{"awaiting_human":false}');

  assert.deepEqual(readFlipJson(project), { to: "claude" });
});

test("gate --clear --no-relaunch (kickoff) clears without queueing a relaunch", (t) => {
  const project = tempProject(t);
  flip(project, "gate");

  assert.equal(flip(project, "gate", "--clear", "--no-relaunch"), '{"awaiting_human":false}');

  assert.deepEqual(readFlipJson(project), {});
});

test("clearGate without relaunch (kickoff) also drops a queued launch and a pause", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.join(project, ".orchestrator"));
  const record = { awaiting_human: { door: "claude" }, pending_launch: { to: "opencode" }, paused: { door: "claude" }, z: 1 };

  assert.equal(clearGate(project, record, { relaunch: false }), null);

  assert.deepEqual(readFlipJson(project), { z: 1 });
});

test("clearGate with relaunch keeps a queued launch", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.join(project, ".orchestrator"));
  const queued = { to: "opencode", from: "claude", reason: "handback", resume: null, at: "2026-01-01T00:00:00.000Z" };

  assert.equal(clearGate(project, { awaiting_human: { door: "claude" }, pending_launch: queued }, { relaunch: true }), "opencode");

  assert.deepEqual(readFlipJson(project), { pending_launch: queued });
});

// Writes each relative path's content under dir (a trailing "/" makes a directory).
function layout(dir, files) {
  for (const [rel, content] of Object.entries(files)) {
    const file = path.join(dir, rel);
    if (rel.endsWith("/")) { fs.mkdirSync(file, { recursive: true }); continue; }
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, content.replaceAll("{dir}", dir));
  }
}

test("ensureGitExclude writes the info/exclude git reads behind a .git directory or file", async (t) => {
  for (const [name, files, exclude] of [
    ["a .git directory", { "project/.git/": "" }, "project/.git/info/exclude"],
    ["a linked worktree (commondir)", { "project/.git": "gitdir: {dir}/main/.git/worktrees/w\n", "main/.git/worktrees/w/commondir": "../..\n" }, "main/.git/info/exclude"],
    ["a submodule (no commondir)", { "project/.git": "gitdir: ../main/.git/modules/sub\n", "main/.git/modules/sub/": "" }, "main/.git/modules/sub/info/exclude"],
    ["a relative gitdir with trailing white space", { "project/.git": "gitdir: .gitmeta/worktrees/w \r\n", "project/.gitmeta/worktrees/w/commondir": "../.." }, "project/.gitmeta/info/exclude"],
  ]) {
    await t.test(name, (t) => {
      const dir = tempProject(t);
      layout(dir, files);
      assert.equal(ensureGitExclude(path.join(dir, "project")), true);
      assert.equal(fs.readFileSync(path.join(dir, exclude), "utf8"), ".orchestrator/\n");
    });
  }
});

test("ensureGitExclude treats a malformed .git file or a missing gitdir as no repository", async (t) => {
  for (const [name, content] of [["malformed", "not a pointer\n"], ["no space", "gitdir:x\n"], ["missing gitdir", "gitdir: /elsewhere\n"], ["gitdir is a file", "gitdir: file\n"]]) {
    await t.test(name, (t) => {
      const project = tempProject(t);
      layout(project, { ".git": content, file: "" });
      assert.equal(ensureGitExclude(project), false);
      assert.deepEqual(fs.readdirSync(project).sort(), [".git", "file"]);
    });
  }
});

// Git judges such a git dir no repository, so nothing is created where the commondir points (C4-F1).
test("ensureGitExclude treats a commondir naming no directory as no repository", async (t) => {
  for (const [name, commonDir] of [["relative, missing", "../../../absent\n"], ["absolute, missing", "{dir}/absent"], ["a file", "../../file\n"]]) {
    await t.test(name, (t) => {
      const project = tempProject(t);
      layout(project, { ".git": "gitdir: meta/worktrees/w\n", "meta/worktrees/w/commondir": commonDir, "meta/file": "" });
      assert.equal(ensureGitExclude(project), false);
      assert.deepEqual(fs.readdirSync(path.join(project, "meta")).sort(), ["file", "worktrees"]);
      assert.equal(fs.existsSync(path.join(project, "absent")), false);
    });
  }
});
