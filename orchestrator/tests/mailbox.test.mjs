import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { post, list } from "../lib/mailbox.mjs";
import { runWithLateStdin } from "./late-stdin.mjs";

const mailboxCli = fileURLToPath(new URL("../bin/mailbox.mjs", import.meta.url));
const flipCli = fileURLToPath(new URL("../bin/flip.mjs", import.meta.url));

// `reply` without answer words reads the answer from stdin, which may be a pipe its writer has
// not written yet: it must wait for it, not fail with EAGAIN (incident e80dd9b226b2).
test("mailbox reply waits for an answer piped on stdin late", async (t) => {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "mailbox-reply-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  const question = post(project, { from: "conductor", to: "human", kind: "question", subject: "ship it?" });

  const { code, stderr } = await runWithLateStdin([mailboxCli, "reply", question.id], "yes", { env: { ...process.env, ORCHESTRATOR_PROJECT: project } });

  assert.equal(stderr, "");
  assert.equal(code, 0);
  const reply = list(project, { limit: 10 }).find((m) => m.thread === question.id);
  assert.equal(reply?.body, "yes");
});

// A project stopped at a human gate on a new question, recorded as questionRef(id) (the id or a prefix).
function gatedProject(t, questionRef = (id) => id) {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "mailbox-gate-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  const question = post(project, { from: "conductor-claude", to: "human", kind: "question", subject: "push to main?" });
  const gate = spawnSync(process.execPath, [flipCli, "gate", "--active", "claude", "--question", questionRef(question.id), "--cwd", project], { encoding: "utf8" });
  assert.equal(gate.status, 0, gate.stderr);
  return { project, question };
}
function reply(project, id) {
  return spawnSync(process.execPath, [mailboxCli, "reply", id, "yes"], { encoding: "utf8", env: { ...process.env, ORCHESTRATOR_PROJECT: project } });
}
const readFlipJson = (project) => JSON.parse(fs.readFileSync(path.join(project, ".orchestrator", "flip.json"), "utf8"));
const clearedLine = "cleared the human gate; the supervisor relaunches the claude conductor within a minute";

for (const [name, questionRef, replyRef] of [
  ["full id", (id) => id, (id) => id],
  ["8-character prefix", (id) => id.slice(0, 8), (id) => id.slice(0, 8)],
]) {
  test(`mailbox reply to the gate's question (${name}) clears the gate and queues a relaunch`, (t) => {
    const { project, question } = gatedProject(t, questionRef);

    const r = reply(project, replyRef(question.id));

    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stderr, "");
    assert.equal(r.stdout.trim().split("\n")[1], clearedLine);
    const record = readFlipJson(project);
    assert.equal(record.awaiting_human, undefined);
    assert.deepEqual([record.pending_launch.to, record.pending_launch.from, record.pending_launch.reason], ["claude", "claude", "human gate cleared"]);
  });
}

test("mailbox reply to another message keeps the gate", (t) => {
  const { project } = gatedProject(t);
  const other = post(project, { from: "conductor-claude", to: "human", kind: "question", subject: "naming?" });

  const r = reply(project, other.id);

  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout.includes("cleared the human gate"), false);
  const record = readFlipJson(project);
  assert.equal(record.awaiting_human.door, "claude");
  assert.equal(record.pending_launch, undefined);
});

// The gate's question was stored as a short prefix that later also matches another message: a reply to
// that other message must not clear the gate (the prefix no longer names one message).
test("mailbox reply to a message that only shares the gate question's short prefix keeps the gate", (t) => {
  const { project, question } = gatedProject(t, (id) => id.slice(0, 2));
  const other = post(project, { from: "conductor-claude", to: "human", kind: "question", subject: "naming?" });
  const messagesFile = path.join(project, ".orchestrator", "mailbox", "messages.jsonl");
  const otherId = question.id.slice(0, 2) + other.id.slice(2);
  fs.writeFileSync(messagesFile, fs.readFileSync(messagesFile, "utf8").replace(other.id, otherId));

  const r = reply(project, otherId);

  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout.includes("cleared the human gate"), false);
  assert.equal(r.stderr, `could not match the human gate's question: ambiguous message id prefix ${question.id.slice(0, 2)}\n`);
  assert.equal(readFlipJson(project).awaiting_human.door, "claude");
});
