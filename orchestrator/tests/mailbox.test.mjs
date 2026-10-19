import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { post, list } from "../lib/mailbox.mjs";
import { runWithLateStdin } from "./late-stdin.mjs";

const mailboxCli = fileURLToPath(new URL("../bin/mailbox.mjs", import.meta.url));

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
