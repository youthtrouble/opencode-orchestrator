import test from "node:test";
import assert from "node:assert/strict";
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
