import test, { after, before } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn, spawnSync } from "node:child_process";

const repo = path.dirname(path.dirname(new URL(import.meta.url).pathname));
const read = (rel) => fs.readFileSync(path.join(repo, rel), "utf8");
const awayHeading = "## Going away (`/away`)";
const questionsHeading = "## Questions for the human";
const liveCheckoutHeading = "## Keeping the live checkout current";

function frontmatter(rel) {
  const match = /^---\n([\s\S]*?)\n---\n/.exec(read(rel));
  assert.ok(match, `${rel} has frontmatter`);
  return Object.fromEntries(match[1].split("\n").map((line) => line.split(/:\s*(.*)/s).slice(0, 2)));
}

// A shared section is the one copy of a rule both doors load: AGENTS.md (opencode) and the Claude
// conductor's own instructions, which do not include AGENTS.md.
function section(rel, heading) {
  const text = read(rel);
  const start = text.indexOf(heading);
  assert.notEqual(start, -1, `${rel} has "${heading}"`);
  return text.slice(start, text.indexOf("\n## ", start + 1));
}

// The whole orch environment of one test: a project and HOME of its own, no inherited ORCH*
// variable, and an ORCH_REGISTRY that does not exist, so a command that routes fails before it
// could collect live usage.
function sandbox(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "away-project-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.mkdirSync(path.join(dir, ".orchestrator"));
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("ORCH")));
  Object.assign(env, { HOME: dir, ORCH_REGISTRY: path.join(dir, "no-models.json") });
  const run = (...args) => spawnSync(orch, args, { cwd: dir, env, input: "", encoding: "utf8" });
  return { dir, env, run };
}

// The `orch <command> [<verb>] [--option ...]` a section names.
function documentedCommands(text) {
  return [...text.matchAll(/`orch (\w+)((?: [^`]*)?)`/g)].map(([, command, rest]) => {
    const verb = /^ (\w+)/.exec(rest)?.[1];
    return { words: verb ? [command, verb] : [command], options: [...rest.matchAll(/--[\w-]+/g)].map(([option]) => option) };
  });
}

// What each documented invocation answers when it is given no other argument: the command's
// own refusal (or its harmless empty answer), so none of them does any work, and kickoff stops
// at its usage line before it could route.
const dispatchAnswers = {
  kickoff: { status: 2, says: /^usage: orch kickoff --brief FILE/ },
  watch: { status: 1, says: /^orch watch: ENOENT: .*no-models\.json/ },
  "flip run": { status: 2, says: /^--to opencode\|claude required/ },
  "worker status": { status: 0, says: /^\[\]$/ },
  "mailbox inbox": { status: 0, says: /^No open questions for you\.$/ },
  "mailbox reply": { status: 2, says: /^usage: orch mailbox reply <id-or-prefix>/ },
  "mailbox post": { status: 1, says: /^Error: post needs from and subject$/ },
  "mailbox ack": { status: 1, says: /^no message with id undefined in / },
  "session beat": { status: 2, says: /^session beat: --door opencode\|claude required$/ },
};

// orch dispatches the words when they get their own answer, which is neither the top-level
// unknown-command diagnostic nor the one their command gives an unknown verb in their place.
function assertDispatched(run, words) {
  const name = `orch ${words.join(" ")}`;
  const expected = dispatchAnswers[words.join(" ")];
  assert.ok(expected, `${name} has an expected answer`);
  const { status, stdout, stderr } = run(...words);
  assert.doesNotMatch(stderr, /^orch: unknown command /m, `${name} is a known command`);
  if (words.length > 1) assert.notEqual(stderr, run(words[0], "no-such-verb").stderr, `${name} is a known verb`);
  assert.equal(status, expected.status, `${name} exit status: ${stderr}`);
  assert.match(`${stdout}${stderr}`.trim(), expected.says, `${name} answers for itself`);
}

// The documented options whose effect a test below proves, by command. Kickoff's are proven by
// go/internal/kickoff/kickoff_test.go instead: the binary's routing would refresh live usage.
const provenOptions = {
  "mailbox post": ["--from", "--to", "--kind", "--subject", "--body", "--recommended", "--blocking", "--ask"],
  "mailbox ack": ["--id", "--by", "--note"],
  "worker status": ["--lane"],
  "session beat": ["--door", "--now", "--slice", "--progress"],
  watch: ["--follow"],
  kickoff: ["--brief", "--headless", "--unattended", "--dry-run"],
};

let orch;
before(() => {
  orch = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "away-orch-")), "orch");
  const build = spawnSync("go", ["build", "-o", orch, "./cmd/orch"], { cwd: path.join(repo, "go"), encoding: "utf8" });
  assert.equal(build.status, 0, build.stderr);
});
after(() => fs.rmSync(path.dirname(orch), { recursive: true, force: true }));
test("/away is an opencode command for the conductor and a Claude skill only the human invokes", () => {
  assert.equal(frontmatter("commands/away.md").agent, "conductor");
  const skill = frontmatter("claude/skills/away/SKILL.md");
  assert.equal(skill.name, "away");
  assert.equal(skill["disable-model-invocation"], "true");
  for (const rel of ["commands/away.md", "claude/skills/away/SKILL.md"]) assert.match(read(rel), /\$ARGUMENTS/);
});

for (const heading of [awayHeading, questionsHeading, liveCheckoutHeading]) {
  test(`"${heading}" is word-identical in both contracts`, () => {
    assert.equal(section("claude/agents/conductor.md", heading), section("AGENTS.md", heading));
  });
}

for (const [rule, heading, minimum] of [["/away", awayHeading, 5], ["questions", questionsHeading, 4]]) {
  test(`orch dispatches every command the ${rule} rule names, and each option it names is proven`, (t) => {
    const commands = documentedCommands(section("AGENTS.md", heading));
    assert.ok(commands.length >= minimum, `at least ${minimum} orch commands`);
    const { run } = sandbox(t);
    for (const { words, options } of commands) {
      assertDispatched(run, words);
      const key = words.join(" ");
      for (const option of options) assert.ok(provenOptions[key]?.includes(option), `orch ${key} ${option} is proven`);
    }
  });
}

test("mailbox post records every documented option, and reply and ack close the human's questions", (t) => {
  const { run } = sandbox(t);
  const post = (...args) => {
    const posted = run("mailbox", "post", "--from", "conductor", "--to", "human", "--kind", "question", ...args);
    assert.equal(posted.status, 0, posted.stderr);
    return JSON.parse(posted.stdout).id;
  };
  const decision = post("--subject", "Ship it?", "--body", "why", "--recommended", "Yes, ship it.", "--blocking", "true");
  const action = post("--subject", "Create the env", "--body", "the steps", "--ask", "action", "--blocking", "false");
  const messages = () => JSON.parse(run("mailbox", "list", "--json").stdout);
  const fields = ({ from, to, kind, subject, body, recommended, blocking, ask }) => ({ from, to, kind, subject, body, recommended, blocking, ask });
  assert.deepEqual(messages().map(fields), [
    { from: "conductor", to: "human", kind: "question", subject: "Ship it?", body: "why", recommended: "Yes, ship it.", blocking: true, ask: undefined },
    { from: "conductor", to: "human", kind: "question", subject: "Create the env", body: "the steps", recommended: undefined, blocking: false, ask: "action" },
  ]);
  assert.match(run("mailbox", "inbox").stdout, /Ship it\? \(blocking\)[\s\S]*Create the env/);

  const reply = run("mailbox", "reply", decision.slice(0, 8), "go ahead");
  assert.equal(reply.status, 0, reply.stderr);
  assert.ok(messages().some((m) => m.thread === decision && m.from === "human" && m.body === "go ahead"));
  const ack = run("mailbox", "ack", "--id", action, "--by", "human", "--note", "answered in chat");
  assert.equal(ack.status, 0, ack.stderr);
  assert.deepEqual((({ id, by, note }) => ({ id, by, note }))(JSON.parse(ack.stdout)), { id: action, by: "human", note: "answered in chat" });
  assert.equal(run("mailbox", "inbox").stdout, "No open questions for you.\n");
});

test("worker status --lane reports that lane even before it has run", (t) => {
  const { run } = sandbox(t);
  assert.deepEqual(JSON.parse(run("worker", "status").stdout), []);
  const [conductor, ...others] = JSON.parse(run("worker", "status", "--lane", "conductor").stdout);
  assert.deepEqual([conductor.lane, conductor.status, others], ["conductor", "unknown", []]);
});

test("session beat records the door, what is happening now, the slice and its progress", (t) => {
  const { dir, run } = sandbox(t);
  const beat = run("session", "beat", "--door", "claude", "--now", "on the slice", "--slice", "s1", "--progress", "2/5");
  assert.equal(beat.status, 0, beat.stderr);
  const record = JSON.parse(fs.readFileSync(path.join(dir, ".orchestrator/sessions/claude.json"), "utf8"));
  assert.deepEqual([record.door, record.now, record.slice, record.progress], ["claude", "on the slice", "s1", { done: 2, total: 5 }]);
});

test("watch --follow keeps redrawing until it is stopped", { timeout: 10_000 }, async (t) => {
  const { dir, env } = sandbox(t);
  const watch = spawn(orch, ["watch", "--follow", "--interval", "0.05"], { cwd: dir, env, stdio: ["ignore", "ignore", "pipe"] });
  t.after(() => watch.kill());
  const exited = new Promise((resolve) => watch.on("exit", (code, signal) => resolve({ code, signal })));
  // Without a registry every frame fails to route and says so on stderr. A single render would
  // instead exit 1 after one `orch watch: ...` line. Three frames prove it redraws; three more
  // are the window in which it must not stop by itself.
  let stderr = "";
  const frames = await Promise.race([
    new Promise((resolve) =>
      watch.stderr.on("data", (chunk) => {
        stderr += chunk;
        const lines = stderr.split("\n").slice(0, -1);
        if (lines.length >= 6) resolve(lines);
      }),
    ),
    exited.then(({ code }) => assert.fail(`watch --follow exited ${code} by itself: ${stderr}`)),
  ]);
  for (const frame of frames) assert.match(frame, /^ENOENT: .*no-models\.json/);
  // Only the signal ends it; a process that had already exited would report its own code.
  watch.kill();
  assert.deepEqual(await exited, { code: null, signal: "SIGTERM" });
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
