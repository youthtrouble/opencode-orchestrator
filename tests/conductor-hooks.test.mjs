import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";

const repo = path.dirname(path.dirname(new URL(import.meta.url).pathname));
const heartbeat = "node ~/.config/opencode/orchestrator/bin/session.mjs beat --door claude --hook";

// The command hooks of the conductor's frontmatter, by event. A reader for the one documented shape
// (event, then `- hooks:`, then `- type: command` entries), not YAML in general.
function frontmatterHooks() {
  const text = fs.readFileSync(path.join(repo, "claude/agents/conductor.md"), "utf8");
  const frontmatter = /^---\n([\s\S]*?)\n---\n/.exec(text);
  assert.ok(frontmatter, "the conductor has frontmatter");
  const lines = frontmatter[1].split("\n");
  const start = lines.indexOf("hooks:");
  assert.notEqual(start, -1, "the frontmatter has a hooks block");
  const hooks = {};
  let event = null;
  for (const line of lines.slice(start + 1)) {
    if (!line.startsWith(" ")) break;
    const eventKey = /^ {2}(\w+):$/.exec(line);
    if (eventKey) { event = eventKey[1]; hooks[event] = []; continue; }
    const command = /^ +command: "(.*)"$/.exec(line);
    if (command) hooks[event].push(command[1]);
  }
  return hooks;
}

test("the conductor's frontmatter beats the session heartbeat on prompt, stop and session end", () => {
  assert.deepEqual(frontmatterHooks(), { UserPromptSubmit: [heartbeat], Stop: [heartbeat], SessionEnd: [heartbeat] });
});

test("each frontmatter hook command records its event's state in the project the hook names", (t) => {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "conductor-hooks-"));
  t.after(() => fs.rmSync(project, { recursive: true, force: true }));
  fs.mkdirSync(path.join(project, ".orchestrator"));
  // The installed path, pointed at this checkout.
  const [, ...args] = heartbeat.replace("~/.config/opencode/orchestrator", path.join(repo, "orchestrator")).split(" ");
  const expected = { UserPromptSubmit: "working", Stop: "waiting", SessionEnd: "ended" };
  for (const [event, commands] of Object.entries(frontmatterHooks())) {
    assert.deepEqual(commands, [heartbeat]);
    const input = JSON.stringify({ session_id: "s1", cwd: project, hook_event_name: event, prompt: "do not store" });
    const run = spawnSync(process.execPath, args, { input, encoding: "utf8" });
    assert.equal(run.status, 0, run.stderr);
    assert.equal(run.stdout, "", "UserPromptSubmit stdout would be added to the conversation");
    const record = JSON.parse(fs.readFileSync(path.join(project, ".orchestrator/sessions/claude.json"), "utf8"));
    assert.equal(record.state, expected[event]);
  }
});
