// Regression tests for redact.mjs: the privacy boundaries of the redactor and the leak scanner.
//
//   node --test go/testdata/redact.test.mjs
//
// Every input is fake. Credential-shaped strings are assembled at run time so this file never holds one.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REDACT = path.join(HERE, "redact.mjs");
const FAKE_HOME = "/Users/fake-person";
const WORK = "/private/var/folders/zz/fakehash/T/opencode/s5a-capture/work";
const MARKER = "zq-private-marker-91";
const ANTHROPIC_KEY = ["sk", "ant", "fakefakefake"].join("-");
const JWT = "ey" + "JhbGciOiJIUzI1NiJ9";
const BEARER = "rt" + "_" + "a".repeat(24);

function tempDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), "redact-test-"));
}

function run(args) {
  const r = spawnSync(process.execPath, [REDACT, ...args], { encoding: "utf8", env: { PATH: process.env.PATH, HOME: FAKE_HOME } });
  return { status: r.status, out: r.stdout + r.stderr };
}

function init(overrides = {}) {
  return {
    type: "system", subtype: "init", cwd: WORK, session_id: "3f2a6c1e-9b7d-4c2a-8e1f-0a1b2c3d4e5f",
    tools: ["Read", "Bash"], mcp_servers: [], model: "claude-haiku-5-5", slash_commands: ["clear"],
    apiKeySource: "none", claude_code_version: "2.1.287", output_style: "default", agents: ["Explore"],
    skills: ["loop"], plugins: [], capabilities: ["msg_lifecycle_v1"], ...overrides,
  };
}

// Writes one raw case and a deny file, runs the redactor, and returns its outcome and outputs.
function redactOne(events, { stderr = "", argv = ["--model", "claude-haiku-5-5"], rawLines, deny = `${MARKER}\nperson@corp.example\n` } = {}) {
  const dir = tempDir();
  const raw = path.join(dir, "raw");
  const out = path.join(dir, "out");
  fs.mkdirSync(raw);
  fs.writeFileSync(path.join(raw, "c.jsonl"), (rawLines ?? events.map((e) => JSON.stringify(e))).join("\n") + "\n");
  fs.writeFileSync(path.join(raw, "c.stderr"), stderr);
  fs.writeFileSync(path.join(raw, "c.exit"), "0\n");
  fs.writeFileSync(path.join(raw, "c.argv"), argv.join("\n") + "\n");
  fs.writeFileSync(path.join(dir, "deny.txt"), deny);
  const r = run(["--in", raw, "--out", out, "--work", WORK, "--deny-file", path.join(dir, "deny.txt")]);
  const read = (f) => (fs.existsSync(path.join(out, f)) ? fs.readFileSync(path.join(out, f), "utf8") : null);
  const result = { ...r, jsonl: read("c.jsonl"), stderr: read("c.stderr"), cases: read("cases.json") };
  fs.rmSync(dir, { recursive: true, force: true });
  return result;
}

function scanText(files, { deny } = {}) {
  const dir = tempDir();
  const target = path.join(dir, "fixtures");
  fs.mkdirSync(target);
  for (const [name, content] of Object.entries(files)) fs.writeFileSync(path.join(target, name), content);
  const args = ["--scan", target];
  if (deny) {
    fs.writeFileSync(path.join(dir, "deny.txt"), deny);
    args.push("--deny-file", path.join(dir, "deny.txt"));
  }
  const r = run(args);
  fs.rmSync(dir, { recursive: true, force: true });
  return r;
}

const rulesIn = (out) => out.split("\n").filter((l) => /:\d+ [a-z-]+$/.test(l)).map((l) => l.split(" ").pop());

// R1: a parse failure reports only file, line and rule.
test("an unparsable line fails without echoing its content", () => {
  const r = redactOne([], { rawLines: [JSON.stringify(init()), `{"broken": "${MARKER} ${ANTHROPIC_KEY}`] });
  assert.equal(r.status, 1);
  assert.match(r.out, /c\.jsonl:2: unparsable JSON line \(rule json-parse\)/);
  assert.ok(!r.out.includes(MARKER) && !r.out.includes(ANTHROPIC_KEY));
  assert.equal(r.jsonl, null);
});

// R2: customization lists never survive, whatever names or shapes they hold.
test("init customization lists are emptied for unknown names and odd shapes", () => {
  const r = redactOne([init({
    agents: ["Explore", "my-private-agent"], skills: ["loop", "secret-skill"], slash_commands: ["clear", "team-cmd"],
    terminal_slash_commands: ["doctor"], plugins: { name: "object-plugin" }, mcp_servers: "corp-mcp",
    tools: ["Read", "Bash", "mcp__corp__lookup", 7], output_style: "corp-style",
  })]);
  assert.equal(r.status, 0, r.out);
  const event = JSON.parse(r.jsonl);
  for (const key of ["agents", "skills", "slash_commands", "terminal_slash_commands", "plugins", "mcp_servers"]) {
    assert.deepEqual(event[key], [], key);
  }
  assert.deepEqual(event.tools, ["Read", "Bash"]);
  assert.equal(event.output_style, "fixture");
  for (const name of ["my-private-agent", "secret-skill", "team-cmd", "object-plugin", "corp-mcp", "mcp__corp", "corp-style"]) {
    assert.ok(!r.jsonl.includes(name), name);
  }
});

test("known built-in tools and the default output style are kept", () => {
  const r = redactOne([init({ tools: ["Read", "StructuredOutput", "Workflow"] })]);
  const event = JSON.parse(r.jsonl);
  assert.deepEqual(event.tools, ["Read", "StructuredOutput", "Workflow"]);
  assert.equal(event.output_style, "default");
  assert.deepEqual(event.capabilities, ["msg_lifecycle_v1"]);
});

// R3: the credential stop covers ordinary fields, keys, discarded fields, stderr and argv.
for (const [name, events, extra] of [
  ["an ordinary field", [init(), { type: "assistant", text: ANTHROPIC_KEY }]],
  ["an object key", [init(), { type: "result", wire_tool_inputs: { [JWT]: {} } }]],
  ["a discarded plugin entry", [init({ plugins: [{ name: "x", token: ANTHROPIC_KEY }] })]],
  ["a discarded customization name", [init({ agents: [BEARER] })]],
  ["a JSON-escaped value", [init()], { rawLines: [JSON.stringify(init()), '{"t":"sk\\u002dant-fake"}'] }],
  ["stderr", [init()], { stderr: `warning ${BEARER}\n` }],
  ["argv", [init()], { argv: ["--", JWT] }],
]) {
  test(`a credential in ${name} stops the run with no output`, () => {
    const r = redactOne(events, extra);
    assert.equal(r.status, 1, r.out);
    assert.match(r.out, /credential-shaped string \(rule [a-z-]+\); stop and report/);
    assert.ok(![ANTHROPIC_KEY, JWT, BEARER].some((c) => r.out.includes(c)));
    assert.equal(r.jsonl, null);
  });
}

test("an apiKeySource other than none stops the run", () => {
  const r = redactOne([init({ apiKeySource: "user" })]);
  assert.equal(r.status, 1);
  assert.match(r.out, /apiKeySource is not "none"/);
  assert.equal(r.jsonl, null);
});

test("ids, UUIDs, paths, deny literals and emails are redacted consistently", () => {
  const sid = "3f2a6c1e-9b7d-4c2a-8e1f-0a1b2c3d4e5f";
  const r = redactOne([
    init(),
    {
      type: "assistant", session_id: sid.toUpperCase(),
      message: { id: "msg_01AbCdEf", content: [{ type: "tool_use", id: "toolu_01Xy_z9", input: { file_path: `${WORK}/README.md` } }] },
      request_id: "req_011CabcDEF", wire_tool_inputs: { "toolu_01Xy_z9": {} },
      paths: [`${WORK.replace("/private", "")}/a`, `${FAKE_HOME}/x`, "/Users/someone-else/y", "/home/other/z", "/var/folders/ab/cd/T/q"],
      who: `${MARKER.toUpperCase()} person@corp.example other@corp.example`,
    },
  ], { stderr: `[warn] ${FAKE_HOME}/.claude ${MARKER}\n` });
  assert.equal(r.status, 0, r.out);
  const [, event] = r.jsonl.trim().split("\n").map((l) => JSON.parse(l));
  assert.equal(event.session_id, "00000000-0000-4000-8000-000000000001");
  assert.equal(event.message.id, "msg_fixture01");
  assert.equal(event.message.content[0].id, "toolu_fixture02");
  assert.deepEqual(Object.keys(event.wire_tool_inputs), ["toolu_fixture02"]);
  assert.equal(event.request_id, "req_fixture03");
  assert.deepEqual(event.paths, ["/tmp/fixture/a", "/home/fixture/x", "/home/fixture/y", "/home/fixture/z", "/tmp/q"]);
  assert.equal(event.who, "fixture fixture@example.com fixture@example.com");
  assert.equal(r.stderr, "[warn] /home/fixture/.claude fixture\n");
  assert.equal(JSON.parse(r.jsonl.split("\n")[0]).cwd, "/tmp/fixture");
});

test("thinking signatures and redacted thinking are replaced", () => {
  const r = redactOne([init(), {
    type: "assistant",
    message: { content: [{ type: "thinking", thinking: "", signature: "CAQSopaqueBLOB/+=" }, { type: "redacted_thinking", data: "ENCRYPTEDblob" }, { type: "text", text: "kept" }] },
  }]);
  assert.equal(r.status, 0, r.out);
  const content = JSON.parse(r.jsonl.trim().split("\n")[1]).message.content;
  assert.deepEqual(content, [
    { type: "thinking", thinking: "", signature: "fixture-signature" },
    { type: "redacted_thinking", data: "fixture-redacted-thinking" },
    { type: "text", text: "kept" },
  ]);
});

test("redacting already-redacted fixtures is idempotent", () => {
  const event = { type: "assistant", session_id: "00000000-0000-4000-8000-000000000002", id: "msg_fixture07", cwd: "/tmp/fixture", home: "/home/fixture" };
  const r = redactOne([init({ session_id: "00000000-0000-4000-8000-000000000002" }), event]);
  assert.deepEqual(JSON.parse(r.jsonl.trim().split("\n")[1]), event);
});

// R4: the scanner decodes JSON and reports only file:line and a rule name.
for (const [name, files, rule] of [
  ["a spaced apiKeySource in JSONL", { "a.jsonl": '{"apiKeySource" :  "user"}\n' }, "api-key-source"],
  ["a nested apiKeySource in JSON", { "a.json": JSON.stringify({ x: [{ apiKeySource: "env" }] }, null, 1) }, "api-key-source"],
  ["a spaced apiKeySource in text", { "a.md": 'see "apiKeySource": "user" here' }, "api-key-source"],
  ["an escaped credential value", { "a.jsonl": '{"t":"sk\\u002dant-zzz"}\n' }, "anthropic-key"],
  ["an escaped credential in text", { "a.stderr": 'x sk\\u002dant-zzz\n' }, "anthropic-key"],
  ["a credential in a key", { "a.jsonl": JSON.stringify({ [JWT]: 1 }) + "\n" }, "jwt"],
  ["a bearer token", { "a.stderr": `${BEARER}\n` }, "bearer-token"],
  ["a non-fixture /home path", { "a.jsonl": '{"p":"/home/alice/x"}\n' }, "user-home"],
  ["an escaped /Users path", { "a.jsonl": '{"p":"\\/Users\\/alice"}\n' }, "users-path"],
  ["a Darwin temp path", { "a.md": "/private/var/folders/ab/x\n" }, "darwin-temp-path"],
  ["a foreign email", { "a.jsonl": '{"e":"bob@corp.example"}\n' }, "email"],
  ["a raw UUID", { "a.jsonl": '{"u":"3F2A6C1E-9B7D-4C2A-8E1F-0A1B2C3D4E5F"}\n' }, "uuid"],
  ["an underscore id suffix", { "a.jsonl": '{"i":"toolu_01ab_cd"}\n' }, "api-id"],
  ["a fixture id with a suffix", { "a.jsonl": '{"i":"msg_fixture01_extra"}\n' }, "api-id"],
  ["an id used as a key", { "a.jsonl": '{"req_011CzzzQQQ":{}}\n' }, "api-id"],
  ["an unparsable JSONL line", { "a.jsonl": `{"x": "${MARKER}\n` }, "json-parse"],
]) {
  test(`scan flags ${name}`, () => {
    const r = scanText(files);
    assert.equal(r.status, 1, r.out);
    assert.ok(rulesIn(r.out).includes(rule), r.out);
    assert.ok(!r.out.includes(MARKER) && !r.out.includes("alice") && !r.out.includes("bob@"));
  });
}

test("scan flags deny literals case-insensitively without printing them", () => {
  const r = scanText({ "a.stderr": `hello ${MARKER.toUpperCase()}\n` }, { deny: `${MARKER}\n` });
  assert.equal(r.status, 1);
  assert.deepEqual(rulesIn(r.out), ["deny-literal"]);
  assert.ok(!r.out.toLowerCase().includes(MARKER));
});

// R5: identity literals match in any case and inside words, however short.
test("a short deny literal is redacted in any case and inside words", () => {
  const red = redactOne([init(), { type: "assistant", t: "Qx7 qx7 xQX7y", [`k-qx7`]: 1 }], { deny: "Qx7\n" });
  assert.equal(red.status, 0, red.out);
  const event = JSON.parse(red.jsonl.trim().split("\n")[1]);
  assert.equal(event.t, "fixture fixture xfixturey");
  assert.deepEqual(Object.keys(event).slice(-1), ["k-fixture"]);
  assert.ok(!/qx7/i.test(red.jsonl) && !/qx7/i.test(red.out));
});

for (const [name, files] of [
  ["its exact spelling", { "a.md": "host Qx7 here\n" }],
  ["another case", { "a.md": "the qx7 word\n" }],
  ["inside a word", { "a.stderr": "xQX7y\n" }],
  ["a JSONL key", { "a.jsonl": '{"pre-qx7-post":1}\n' }],
  ["an escaped JSON value", { "a.jsonl": '{"t":"\\u0071x7"}\n' }],
]) {
  test(`scan flags a short deny literal in ${name}`, () => {
    const r = scanText(files, { deny: "Qx7\n" });
    assert.equal(r.status, 1, r.out);
    assert.deepEqual(rulesIn(r.out), ["deny-literal"]);
    assert.ok(!/qx7/i.test(r.out));
  });
}

// R6: a safe apiKeySource passes in any spacing; an unsafe one fails in any spacing.
for (const [name, files] of [
  ["spaced text", { "a.md": 'init has "apiKeySource" :  "none" here\n' }],
  ["pretty JSON", { "a.json": JSON.stringify({ init: { apiKeySource: "none" } }, null, 2) + "\n" }],
  ["spaced JSONL", { "a.jsonl": '{"apiKeySource" : "none"}\n' }],
  ["tab-separated text", { "a.stderr": '"apiKeySource":\t"none"\n' }],
]) {
  test(`scan accepts apiKeySource none as ${name}`, () => {
    const r = scanText(files);
    assert.equal(r.status, 0, r.out);
  });
}

for (const [name, files] of [
  ["spaced text", { "a.md": '"apiKeySource" :  "user"\n' }],
  ["pretty JSON", { "a.json": JSON.stringify({ apiKeySource: "none ", x: 1 }, null, 2) + "\n" }],
  ["a longer value", { "a.md": '"apiKeySource": "nonexistent"\n' }],
  ["a bare value", { "a.stderr": '"apiKeySource": none\n' }],
  ["an unterminated value", { "a.stderr": '"apiKeySource": "none\n' }],
  ["a missing value", { "a.md": '"apiKeySource":\n' }],
  ["a second, unsafe occurrence", { "a.md": '"apiKeySource": "none", "apiKeySource": "env"\n' }],
]) {
  test(`scan rejects an unsafe apiKeySource as ${name}`, () => {
    const r = scanText(files);
    assert.equal(r.status, 1, r.out);
    assert.ok(rulesIn(r.out).includes("api-key-source"), r.out);
  });
}

test("scan finds a credential at the end of a 1.5 MiB line, quickly", () => {
  const line = JSON.stringify({ c: "0123456789abcdef".repeat(98_304) + " " + ANTHROPIC_KEY }) + "\n";
  const started = Date.now();
  const r = scanText({ "big.jsonl": line });
  assert.equal(r.status, 1);
  assert.deepEqual(rulesIn(r.out), ["anthropic-key"]);
  assert.ok(Date.now() - started < 5000, "scan took too long");
});

test("scan passes clean fixture-shaped content", () => {
  const r = scanText({
    "a.jsonl": JSON.stringify({ apiKeySource: "none", id: "msg_fixture01", t: "toolu_fixture02", caps: ["msg_lifecycle_v1"], u: "00000000-0000-4000-8000-000000000001", p: "/home/fixture/x", e: "fixture@example.com" }) + "\n",
    "a.md": "ids look like `msg_…`; homes become /home/fixture\n",
  });
  assert.equal(r.status, 0, r.out);
});

test("the committed fixtures scan clean and the synthetic cases are current", () => {
  assert.equal(run(["--scan", path.join(HERE, "claude")]).status, 0);
  const gen = spawnSync(process.execPath, [path.join(HERE, "claude", "stream-json", "gen.mjs"), "--check"], { encoding: "utf8" });
  assert.equal(gen.status, 0, gen.stderr);
});
