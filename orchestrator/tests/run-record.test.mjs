import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { runsDir, writeRunRecord } from "../lib/run-record.mjs";
import { runWithLateStdin } from "./late-stdin.mjs";

const runRecord = new URL("../lib/run-record.mjs", import.meta.url).href;

// A bridge's prompt arrives on a pipe its worker runner may not have written yet (incident
// e80dd9b226b2): readPrompt must wait for it, not fail with EAGAIN.
test("readPrompt waits for a stdin pipe that is written late", async () => {
  const reader = `import { readPrompt } from ${JSON.stringify(runRecord)}; process.stdout.write(readPrompt({}, []));`;
  const { code, stdout, stderr } = await runWithLateStdin(["--input-type=module", "-e", reader], "the prompt");
  assert.equal(stderr, "");
  assert.equal(code, 0);
  assert.equal(stdout, "the prompt");
});

// A session id holding "/" must not place the record outside runs/.
test("writeRunRecord keeps slashed ids inside runs/", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "run-record-"));
  try {
    for (const [threadId, name] of [["/../../x", "s-h-_.._.._x.json"], [["a/../../b"], "s-h-a_.._.._b.json"]]) {
      const file = writeRunRecord(root, { started_at: "s", harness: "h", thread_id: threadId });
      assert.equal(file, path.join(runsDir(root), name));
      assert.ok(fs.existsSync(file));
    }
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
