import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";

const repo = path.dirname(path.dirname(new URL(import.meta.url).pathname));
const read = (rel) => fs.readFileSync(path.join(repo, rel), "utf8");
const ruleHeading = "## Going away (`/away`)";

function frontmatter(rel) {
  const match = /^---\n([\s\S]*?)\n---\n/.exec(read(rel));
  assert.ok(match, `${rel} has frontmatter`);
  return Object.fromEntries(match[1].split("\n").map((line) => line.split(/:\s*(.*)/s).slice(0, 2)));
}

// The rule is the one copy of the procedure both doors load: AGENTS.md (opencode) and the Claude
// conductor's own instructions, which do not include AGENTS.md.
function rule(rel) {
  const text = read(rel);
  const start = text.indexOf(ruleHeading);
  assert.notEqual(start, -1, `${rel} has the /away rule`);
  return text.slice(start, text.indexOf("\n## ", start + 1));
}

test("/away is an opencode command for the conductor and a Claude skill only the human invokes", () => {
  assert.equal(frontmatter("commands/away.md").agent, "conductor");
  const skill = frontmatter("claude/skills/away/SKILL.md");
  assert.equal(skill.name, "away");
  assert.equal(skill["disable-model-invocation"], "true");
  for (const rel of ["commands/away.md", "claude/skills/away/SKILL.md"]) assert.match(read(rel), /\$ARGUMENTS/);
});

test("the /away rule is word-identical in both contracts", () => {
  assert.equal(rule("claude/agents/conductor.md"), rule("AGENTS.md"));
});

test("every orch command the /away rule names exists in its script's usage", () => {
  const commands = [...rule("AGENTS.md").matchAll(/`orch (\w+) ?(\w+)?/g)];
  assert.ok(commands.length >= 5);
  for (const [, script, verb] of commands) {
    const source = read(`orchestrator/bin/${script}.mjs`);
    if (verb) assert.match(source, new RegExp(`${script}\\.mjs ${verb}\\b`), `orch ${script} ${verb}`);
  }
});

test("install-claude.sh links the away skill and --uninstall removes it", (t) => {
  const claudeHome = fs.mkdtempSync(path.join(os.tmpdir(), "orchestrator claude home "));
  t.after(() => fs.rmSync(claudeHome, { recursive: true, force: true }));
  const env = { ...process.env, CLAUDE_CONFIG_DIR: claudeHome, HOME: claudeHome };
  const skill = path.join(claudeHome, "skills", "away");
  const install = spawnSync("bash", [path.join(repo, "install-claude.sh")], { env, encoding: "utf8" });
  assert.equal(install.status, 0, install.stderr);
  assert.equal(fs.readlinkSync(skill), path.join(fs.realpathSync(repo), "claude", "skills", "away"));
  const uninstall = spawnSync("bash", [path.join(repo, "install-claude.sh"), "--uninstall"], { env, encoding: "utf8" });
  assert.equal(uninstall.status, 0, uninstall.stderr);
  assert.equal(fs.lstatSync(skill, { throwIfNoEntry: false }), undefined);
});
