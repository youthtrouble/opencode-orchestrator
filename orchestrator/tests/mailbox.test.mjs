import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { post, list, readsAsCompletionClaim, QuestionNeedsRecommendationError, RecommendationRefusedError } from "../lib/mailbox.mjs";
import { runWithLateStdin } from "./late-stdin.mjs";

const mailboxCli = fileURLToPath(new URL("../bin/mailbox.mjs", import.meta.url));
const flipCli = fileURLToPath(new URL("../bin/flip.mjs", import.meta.url));

// `reply` without answer words reads the answer from stdin, which may be a pipe its writer has
// not written yet: it must wait for it, not fail with EAGAIN (incident e80dd9b226b2).
test("mailbox reply waits for an answer piped on stdin late", async (t) => {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "mailbox-reply-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  const question = post(project, { from: "conductor", to: "human", kind: "question", subject: "ship it?", recommended: "yes" });

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
  const question = post(project, { from: "conductor-claude", to: "human", kind: "question", subject: "push to main?", recommended: "yes" });
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
  const other = post(project, { from: "conductor-claude", to: "human", kind: "question", subject: "naming?", recommended: "keep it" });

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
  const other = post(project, { from: "conductor-claude", to: "human", kind: "question", subject: "naming?", recommended: "keep it" });
  const messagesFile = path.join(project, ".orchestrator", "mailbox", "messages.jsonl");
  const otherId = question.id.slice(0, 2) + other.id.slice(2);
  fs.writeFileSync(messagesFile, fs.readFileSync(messagesFile, "utf8").replace(other.id, otherId));

  const r = reply(project, otherId);

  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout.includes("cleared the human gate"), false);
  assert.equal(r.stderr, `could not match the human gate's question: ambiguous message id prefix ${question.id.slice(0, 2)}\n`);
  assert.equal(readFlipJson(project).awaiting_human.door, "claude");
});

test("mailbox reply outside a project names the directory, exits 2 and creates nothing", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mailbox-outside-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));

  const r = reply(dir, "abc");

  assert.equal(r.status, 2);
  assert.equal(r.stdout, "");
  assert.equal(r.stderr, `not an orchestrator project: ${dir} has no .orchestrator/ directory; cd into the project or pass --root <project dir>\n`);
  assert.equal(fs.existsSync(path.join(dir, ".orchestrator")), false);
});

test("mailbox reply and ack with an unknown id name the mailbox searched and exit 1", (t) => {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "mailbox-unknown-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  post(project, { from: "conductor", to: "human", kind: "question", subject: "ship it?", recommended: "yes" });
  const want = `no message with id zzz in ${path.join(project, ".orchestrator", "mailbox")}; if it belongs to another project, cd there or pass --root <project dir>\n`;

  const replied = reply(project, "zzz");
  const acked = spawnSync(process.execPath, [mailboxCli, "ack", "--id", "zzz", "--by", "human", "--root", project], { encoding: "utf8" });

  for (const r of [replied, acked]) {
    assert.equal(r.status, 1);
    assert.equal(r.stdout, "");
    assert.equal(r.stderr, want);
  }
});

function tempProject(t) {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "mailbox-question-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  return project;
}
const messagesFile = (project) => path.join(project, ".orchestrator", "mailbox", "messages.jsonl");
function mailbox(project, ...args) {
  return spawnSync(process.execPath, [mailboxCli, ...args], { encoding: "utf8", env: { ...process.env, ORCHESTRATOR_PROJECT: project } });
}
const postQuestion = (project, ...args) => mailbox(project, "post", "--from", "conductor", "--to", "human", "--kind", "question", "--subject", "ship?", ...args);

test("post() refuses a question without a recommended answer and writes nothing", (t) => {
  const project = tempProject(t);
  for (const recommended of [undefined, ""]) {
    assert.throws(() => post(project, { from: "conductor", kind: "question", subject: "ship?", recommended }), QuestionNeedsRecommendationError);
  }
  assert.equal(fs.existsSync(messagesFile(project)), false);
});

test("mailbox post records a question's recommended answer and blocking state after thread", (t) => {
  for (const [args, tail] of [
    [["--recommended", "ship it"], '"thread":null,"recommended":"ship it","blocking":true}'],
    [["--recommended", "ship it", "--blocking"], '"thread":null,"recommended":"ship it","blocking":true}'],
    [["--blocking", "false", "--recommended", "ship it"], '"thread":null,"recommended":"ship it","blocking":false}'],
    [["--ask", "decision", "--recommended", "ship it"], '"thread":null,"recommended":"ship it","blocking":true}'],
    [["--ask", "action", "--blocking", "false"], '"thread":null,"blocking":false,"ask":"action"}'],
  ]) {
    const project = tempProject(t);
    const r = postQuestion(project, ...args);
    assert.equal(r.status, 0, r.stderr);
    assert.ok(fs.readFileSync(messagesFile(project), "utf8").endsWith(tail + "\n"), args.join(" "));
  }
});

test("mailbox post refuses misused question options, exits 2 and writes nothing", (t) => {
  const needsRecommendation = 'mailbox post: a question needs --recommended "<the answer you recommend>"\n';
  const onlyForQuestions = "mailbox post: --recommended, --blocking and --ask are only for --kind question\n";
  const badAsk = "mailbox post: --ask must be decision or action\n";
  const claimsDone = "mailbox post: --recommended reads as a report that work is done; recommended answers are for decisions. To ask the owner to do something, use --ask action with the steps in --body\n";
  for (const [args, stderr] of [
    [["--kind", "question"], needsRecommendation],
    [["--kind", "question", "--recommended"], needsRecommendation],
    [["--kind", "question", "--recommended", ""], needsRecommendation],
    [["--kind", "question", "--recommended", "r", "--blocking", "maybe"], "mailbox post: --blocking must be true or false\n"],
    [["--recommended", "r"], onlyForQuestions],
    [["--kind", "note", "--blocking"], onlyForQuestions],
    [["--ask", "action"], onlyForQuestions],
    [["--kind", "question", "--recommended", "r", "--ask"], badAsk],
    [["--kind", "question", "--ask", "task", "--recommended", "r"], badAsk],
    [["--kind", "question", "--ask", "decision"], needsRecommendation],
    [["--kind", "question", "--ask", "action", "--recommended", "Yes"],
      "mailbox post: an --ask action question takes no --recommended: put the steps in --body; the owner's reply is a claim to verify\n"],
    [["--kind", "question", "--recommended", "Done: token created, wt-24-owner.sh succeeded, orch rebuilt"], claimsDone],
    [["--kind", "question", "--recommended", "  i've DONE it"], claimsDone],
  ]) {
    const project = tempProject(t);
    const r = mailbox(project, "post", "--from", "conductor", "--subject", "s", ...args);
    assert.deepEqual([r.status, r.stdout, r.stderr], [2, "", stderr], args.join(" "));
    assert.equal(fs.existsSync(path.join(project, ".orchestrator")), false);
  }
});

test("post() refuses an action with a recommendation and a recommendation that reports work done, writing nothing", (t) => {
  const project = tempProject(t);
  const question = { from: "conductor", kind: "question", subject: "set up?" };
  assert.throws(() => post(project, { ...question, ask: "action", recommended: "Yes" }), RecommendationRefusedError);
  assert.throws(() => post(project, { ...question, recommended: "Ran the setup." }), RecommendationRefusedError);
  assert.throws(() => post(project, { ...question, ask: "task", recommended: "Yes" }), /a question asks for a decision or an action, not task/);
  assert.equal(fs.existsSync(messagesFile(project)), false);
});

// The incident's recommendation and each listed opener are claims; words that only begin like one are not.
test("readsAsCompletionClaim matches the openers that report work done, ASCII case and padding aside", () => {
  const claims = ["Done", "done.", " Ran it", "COMPLETED", "finished", "Created the env", "deployed", "Rebuilt orch", "installed it",
    "I ran it", "I've done it", "I’ve done it", "I have done it", "all done", "\ufeffDone"];
  for (const text of claims) assert.equal(readsAsCompletionClaim(text), true, text);
  for (const text of ["Donate", "ranked", "Ship it; done later", "finiſhed", "Create the env first", ""]) {
    assert.equal(readsAsCompletionClaim(text), false, text);
  }
});

test("mailbox inbox marks blocking questions and shows the recommended answer; older questions print as before", (t) => {
  const project = tempProject(t);
  fs.mkdirSync(path.dirname(messagesFile(project)), { recursive: true });
  fs.writeFileSync(messagesFile(project), [
    { id: "q1000000", at: "2026-10-18T12:00:00.000Z", from: "c", to: "human", kind: "question", subject: "ship?", body: "b", recommended: "yes\nnow", blocking: true },
    { id: "q2000000", at: "2026-10-18T13:00:00.000Z", from: "c", to: "human", kind: "question", subject: "name?", body: "b", recommended: "r".repeat(205), blocking: false },
    { id: "q3000000", at: "2026-10-18T14:00:00.000Z", from: "c", to: "human", kind: "question", subject: "legacy", body: "b" },
  ].map((m) => JSON.stringify(m) + "\n").join(""));

  const r = mailbox(project, "inbox");

  const hint = (id) => `    reply: node ~/.config/opencode/orchestrator/bin/mailbox.mjs reply ${id} "your answer"\n\n`;
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout,
    "[q1000000] 2026-10-18T12:00Z from c: ship? (blocking)\n    b\n    recommended: yes now\n" + hint("q1000000") +
    `[q2000000] 2026-10-18T13:00Z from c: name?\n    b\n    recommended: ${"r".repeat(200)}\n` + hint("q2000000") +
    "[q3000000] 2026-10-18T14:00Z from c: legacy\n    b\n" + hint("q3000000"));
});
