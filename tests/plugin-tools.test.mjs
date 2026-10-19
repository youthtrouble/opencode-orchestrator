// The plugin's state-file tools and script spawns in both runtimes (S10b): mailbox, manifest and
// incident go through `orch mailbox|manifest|incident` in go mode, and every spawned script
// (claude-exec, codex-exec, worker, supervisor, flip) runs as `orch <script>` with the argv the
// JS script gets. Go sandboxes poison every lib/*.mjs and bin/*.mjs, so any JS use is visible.
import test, { describe } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { calls, checkoutRegistry, createSandbox, loadPlugin, poisonHits, routingDocument, setAnswers, SPAWNED_SCRIPTS, typescriptSupported, usageFixture, waitFor, writeFakeOrch } from "./plugin-harness.mjs";

const skip = typescriptSupported ? false : "Node cannot strip TypeScript types here (needs >= 22.18)";

/** A go sandbox whose fake orch answers routing like the real binary, plus `answers`. */
async function loadGo(t, answers = {}) {
  return loadGoInto(t, createSandbox(t, { runtime: "go" }), answers);
}

async function loadGoInto(t, sandbox, answers) {
  const doc = await routingDocument(usageFixture("green"));
  setAnswers(sandbox, { "routing --json": { stdout: doc }, "routing --json --refresh": { stdout: doc }, "routing --cached --json": { stdout: doc }, ...answers });
  const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go" } });
  return { sandbox, plugin };
}

async function loadJs(t, answers = {}) {
  const sandbox = createSandbox(t, { runtime: "js" });
  setAnswers(sandbox, answers);
  const plugin = await loadPlugin(t, sandbox);
  return { sandbox, plugin };
}

/** The calls made to `orch <first>...` (go) since the log was `from` entries long. */
const orchCalls = (sandbox, first, from = 0) => calls(sandbox).slice(from).filter((c) => c.self === "path" && c.argv[0] === first);

describe("mailbox_post", { skip }, () => {
  test("js: a real message line lands in messages.jsonl", async (t) => {
    const { sandbox, plugin } = await loadJs(t);
    const r = await plugin.callTool("mailbox_post", { from: "coder", subject: "hi", body: "text", refs: ["a.md"] });
    const lines = fs.readFileSync(path.join(sandbox.project, ".orchestrator", "mailbox", "messages.jsonl"), "utf8").trim().split("\n").map((l) => JSON.parse(l));
    assert.equal(lines.length, 1);
    assert.deepEqual(r.metadata, lines[0]);
    assert.equal(r.content, `posted ${lines[0].id}`);
    assert.deepEqual({ ...lines[0], id: 0, at: 0 }, { id: 0, at: 0, from: "coder", to: "*", kind: "note", subject: "hi", body: "text", refs: ["a.md"], thread: null });
  });

  test("go: orch mailbox post with the body on stdin and --json last", async (t) => {
    const message = { id: "m-1", at: "2026-10-19T00:00:00.000Z", from: "coder", to: "human", kind: "question", subject: "hi", body: "line 1\n--not-a-flag", refs: ["a.md", "b.md"], thread: "t-1" };
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { stdout: message } });
    const r = await plugin.callTool("mailbox_post", { from: "coder", to: "human", kind: "question", subject: "hi", body: message.body, refs: message.refs, thread: "t-1" });
    const [call] = orchCalls(sandbox, "mailbox");
    assert.deepEqual(call.argv, ["mailbox", "post", "--root", sandbox.project, "--from", "coder", "--to", "human", "--kind", "question", "--subject", "hi", "--thread", "t-1", "--ref", "a.md", "--ref", "b.md", "--body", "-", "--json"]);
    assert.equal(call.stdin, message.body);
    assert.equal(call.cwd, fs.realpathSync(process.cwd()));
    assert.equal(r.content, "posted m-1");
    assert.deepEqual(r.metadata, message);
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("go: the defaults leave out --thread and send an empty body", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { stdout: { id: "m-2" } } });
    await plugin.callTool("mailbox_post", { from: "coder", subject: "hi" });
    const [call] = orchCalls(sandbox, "mailbox");
    assert.deepEqual(call.argv, ["mailbox", "post", "--root", sandbox.project, "--from", "coder", "--to", "*", "--kind", "note", "--subject", "hi", "--body", "-", "--json"]);
    assert.equal(call.stdin, "");
  });

  for (const [field, input] of [["subject", { subject: "--x" }], ["refs", { refs: ["ok", "--x"] }], ["thread", { thread: "--x" }]]) {
    test(`go: a ${field} beginning with "--" is refused before any spawn`, async (t) => {
      const { sandbox, plugin } = await loadGo(t);
      const r = await plugin.toolResult("mailbox_post", { from: "coder", subject: "hi", ...input });
      assert.equal(r.status, "error");
      assert.equal(String(r.error), `OrchCommandError: ${field} may not begin with "--" in the go runtime`);
      assert.equal(r.error.subcommand, "mailbox post");
      assert.equal(r.error.code, null);
      assert.deepEqual(orchCalls(sandbox, "mailbox"), []);
    });
  }

  test("go: exit 1 from orch is a tool error with the stderr line", async (t) => {
    const { plugin } = await loadGo(t, { "mailbox post": { code: 1, stderr: "Error: post needs from and subject\n" } });
    const r = await plugin.toolResult("mailbox_post", { from: "", subject: "hi" });
    assert.equal(r.status, "error");
    assert.equal(r.error.message, "orch mailbox post exited 1: Error: post needs from and subject");
  });
});

describe("mailbox_list", { skip }, () => {
  test("go: the content equals js mode's for the same mailbox", async (t) => {
    const js = await loadJs(t);
    const mailboxLib = await import(pathToFileURL(path.join(js.sandbox.orchestrator, "lib", "mailbox.mjs")).href);
    mailboxLib.post(js.sandbox.project, { from: "planner", to: "coder", kind: "brief", subject: "do it", body: "the brief", refs: ["plan.md"] });
    mailboxLib.post(js.sandbox.project, { from: "coder", subject: "done" });
    const expected = mailboxLib.list(js.sandbox.project, { to: null, unackedBy: null, kind: null, limit: 30 });
    const jsResult = await js.plugin.callTool("mailbox_list");

    const { sandbox, plugin } = await loadGo(t, { "mailbox list": { stdout: expected } });
    const goResult = await plugin.callTool("mailbox_list");
    assert.deepEqual(orchCalls(sandbox, "mailbox")[0].argv, ["mailbox", "list", "--root", sandbox.project, "--limit", "30", "--json"]);
    assert.equal(goResult.content, jsResult.content);
    assert.deepEqual(goResult.metadata, { count: 2 });
  });

  test("go: filters become flags", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox list": { stdout: [] } });
    await plugin.callTool("mailbox_list", { to: "coder", unacked_by: "coder", kind: "brief", limit: 5 });
    assert.deepEqual(orchCalls(sandbox, "mailbox")[0].argv, ["mailbox", "list", "--root", sandbox.project, "--to", "coder", "--unacked-by", "coder", "--kind", "brief", "--limit", "5", "--json"]);
  });

  // orch would read `--to --x` as a bare --to and list the whole mailbox; js filters on the literal value.
  for (const field of ["to", "unacked_by", "kind"]) {
    test(`go: a ${field} filter beginning with "--" is refused before any spawn`, async (t) => {
      const { sandbox, plugin } = await loadGo(t, { "mailbox list": { stdout: [] } });
      const r = await plugin.toolResult("mailbox_list", { [field]: "--x" });
      assert.equal(String(r.error), `OrchCommandError: ${field} may not begin with "--" in the go runtime`);
      assert.deepEqual(orchCalls(sandbox, "mailbox"), []);
    });
  }

  test("go: an empty list is (mailbox empty)", async (t) => {
    const { plugin } = await loadGo(t, { "mailbox list": { stdout: [] } });
    assert.deepEqual(await plugin.callTool("mailbox_list"), { content: "(mailbox empty)" });
  });
});

describe("manifest_create and manifest_verify", { skip }, () => {
  test("js: writes the manifest file, then verify sees OK and MISMATCH", async (t) => {
    const { sandbox, plugin } = await loadJs(t);
    fs.writeFileSync(path.join(sandbox.project, "a.txt"), "a\n");
    const created = await plugin.callTool("manifest_create", { task: "t1", author: "coder", paths: ["a.txt"] });
    const out = path.join(sandbox.project, ".orchestrator", "manifests", "t1.json");
    const manifest = JSON.parse(fs.readFileSync(out, "utf8"));
    assert.equal(created.content, `manifest .orchestrator/manifests/t1.json: 1 files, sha256 ${manifest.manifest_sha256}`);
    assert.deepEqual(created.metadata, { path: out, manifest_sha256: manifest.manifest_sha256 });
    assert.equal((await plugin.callTool("manifest_verify", { manifest: ".orchestrator/manifests/t1.json" })).content, "OK: all 1 files match");
    fs.writeFileSync(path.join(sandbox.project, "a.txt"), "changed\n");
    assert.equal((await plugin.callTool("manifest_verify", { manifest: ".orchestrator/manifests/t1.json" })).content, "MISMATCH: changed a.txt; missing -");
  });

  test("go: orch writes the manifest; the plugin reports it", async (t) => {
    const out = (sandbox) => path.join(sandbox.project, ".orchestrator", "manifests", "t1.json");
    const { sandbox, plugin } = await loadGo(t, { "manifest create": { stdout: { out: "ignored", file_count: 2, manifest_sha256: "abc123" } } });
    const r = await plugin.callTool("manifest_create", { task: "t1", author: "coder", paths: ["a.txt", "src"], baseline: "base.json" });
    assert.deepEqual(orchCalls(sandbox, "manifest")[0].argv, ["manifest", "create", "--root", sandbox.project, "--task", "t1", "--author", "coder", "--out", out(sandbox), "--baseline", "base.json", "a.txt", "src"]);
    assert.equal(r.content, "manifest .orchestrator/manifests/t1.json: 2 files, sha256 abc123");
    assert.deepEqual(r.metadata, { path: out(sandbox), manifest_sha256: "abc123" });
    assert.equal(fs.existsSync(out(sandbox)), false);
  });

  test("go: no paths surfaces orch's usage error (D-S10-1)", async (t) => {
    const { plugin } = await loadGo(t, { "manifest create": { code: 2, stderr: "usage: orch manifest create --task T --author A --out file.json [--baseline b.json] [--root dir] <paths...>\n" } });
    const r = await plugin.toolResult("manifest_create", { task: "t1", author: "coder", paths: [] });
    assert.equal(r.status, "error");
    assert.match(r.error.message, /^orch manifest create exited 2: usage: /);
  });

  test("go: a path beginning with \"--\" is refused before any spawn", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    const r = await plugin.toolResult("manifest_create", { task: "t1", author: "coder", paths: ["a.txt", "--root"] });
    assert.equal(r.error.message, 'paths may not begin with "--" in the go runtime');
    assert.deepEqual(orchCalls(sandbox, "manifest"), []);
  });

  // The fake orch answers by the tokens before the first flag, which include the manifest path.
  const verifyKey = (sandbox, file) => `manifest verify ${path.join(sandbox.project, file)}`;
  const verifyCases = [
    ["OK on exit 0", { stdout: '{"ok":true,"checked":2,"mismatched":[],"missing":[]}\n' }, "OK: all 2 files match"],
    ["MISMATCH on exit 1 with JSON", { code: 1, stdout: '{"ok":false,"checked":2,"mismatched":["a.txt"],"missing":["b.txt"]}\n' }, "MISMATCH: changed a.txt; missing b.txt"],
  ];
  for (const [name, answer, content] of verifyCases) {
    test(`go: verify gives ${name}`, async (t) => {
      const sandbox = createSandbox(t, { runtime: "go" });
      const { plugin } = await loadGoInto(t, sandbox, { [verifyKey(sandbox, ".orchestrator/manifests/t1.json")]: answer });
      const r = await plugin.callTool("manifest_verify", { manifest: ".orchestrator/manifests/t1.json" });
      assert.deepEqual(orchCalls(sandbox, "manifest")[0].argv, ["manifest", "verify", path.join(sandbox.project, ".orchestrator", "manifests", "t1.json"), "--root", sandbox.project]);
      assert.equal(r.content, content);
      assert.deepEqual(r.metadata, JSON.parse(answer.stdout));
    });
  }

  test("go: verify exit 1 without JSON is an OrchCommandError", async (t) => {
    const sandbox = createSandbox(t, { runtime: "go" });
    const { plugin } = await loadGoInto(t, sandbox, { [verifyKey(sandbox, "broken.json")]: { code: 1, stderr: "SyntaxError: Unexpected token\n" } });
    const r = await plugin.toolResult("manifest_verify", { manifest: "broken.json" });
    assert.equal(r.error.name, "OrchCommandError");
    assert.equal(r.error.message, "orch manifest verify exited 1: SyntaxError: Unexpected token");
  });
});

describe("incident", { skip }, () => {
  test("js: records into the sandbox incident log", async (t) => {
    const { sandbox, plugin } = await loadJs(t);
    const r = await plugin.callTool("incident", { kind: "note", summary: "a note" });
    const log = path.join(sandbox.home, ".local", "state", "opencode", "orchestrator", "incidents.jsonl");
    const entry = JSON.parse(fs.readFileSync(log, "utf8").trim());
    assert.equal(r.content, `recorded incident ${entry.fp}`);
    assert.deepEqual(entry.context, { agent: "conductor", session: "<session>" }); // ses_ ids are redacted
  });

  test("go: orch incident record with the detail on stdin and the context as --ctx", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "incident record": { stdout: '{"recorded":"fp123","at":"2026-10-19T00:00:00.000Z"}\n' } });
    const r = await plugin.callTool("incident", { kind: "misroute", summary: "wrong lane", detail: "--details--" });
    const [call] = orchCalls(sandbox, "incident");
    assert.deepEqual(call.argv, ["incident", "record", "--component", "agent", "--kind", "misroute", "--summary", "wrong lane", "--detail", "-", "--cwd", sandbox.project, "--ctx", "agent=conductor", "--ctx", "session=ses_test"]);
    assert.equal(call.stdin, "--details--");
    assert.equal(r.content, "recorded incident fp123");
  });

  test("go: execute.after records an orch_* error without --cwd when there is no directory", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "incident record": { stdout: '{"recorded":"fp9"}\n' } });
    plugin.api.location = {};
    await plugin.executeAfter({ status: "error", tool: "orch_mailbox_post", error: new Error("boom"), input: { a: 1 }, agent: "build" });
    const [call] = orchCalls(sandbox, "incident");
    assert.deepEqual(call.argv, ["incident", "record", "--component", "plugin", "--kind", "bug", "--summary", "tool orch_mailbox_post errored: boom", "--detail", "-", "--ctx", "agent=build"]);
    assert.equal(call.stdin, '{"a":1}');
  });

  test("go: a summary beginning with \"--\" is refused before any spawn", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    const r = await plugin.toolResult("incident", { kind: "note", summary: "--x" });
    assert.equal(r.error.message, 'summary may not begin with "--" in the go runtime');
    assert.deepEqual(orchCalls(sandbox, "incident"), []);
  });

  test("go: a kind beginning with \"--\" is refused before any spawn", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    const r = await plugin.toolResult("incident", { kind: "--x", summary: "s" });
    assert.equal(r.error.message, 'kind may not begin with "--" in the go runtime');
    assert.deepEqual(orchCalls(sandbox, "incident"), []);
  });
});

describe("spawn tools: the go argv is the js script argv", { skip }, () => {
  /** The spawned-script calls, with the sandbox root (and its realpath) replaced by <root>. */
  function scriptCalls(sandbox) {
    const roots = [fs.realpathSync(sandbox.root), sandbox.root];
    const normalize = (s) => roots.reduce((acc, r) => acc.replaceAll(r, "<root>"), s);
    return calls(sandbox)
      .filter((c) => SPAWNED_SCRIPTS.includes(c.self) || (c.self === "path" && SPAWNED_SCRIPTS.includes(c.argv[0])))
      .map((c) => ({ argv: (c.self === "path" ? c.argv : [c.self, ...c.argv]).map(normalize), cwd: normalize(c.cwd), stdin: normalize(c.stdin) }));
  }

  /** Run `drive` against a js and a go sandbox answering `answers`; return both call lists and results. */
  async function inBothRuntimes(t, answers, drive) {
    const js = await loadJs(t, answers);
    const jsResult = await drive(js.plugin);
    const jsCalls = scriptCalls(js.sandbox);
    const go = await loadGo(t, answers);
    const goResult = await drive(go.plugin);
    assert.deepEqual(poisonHits(go.sandbox), []);
    return { jsCalls, goCalls: scriptCalls(go.sandbox), jsResult, goResult };
  }

  const effortAnswers = (role, size = "medium") => {
    const effort = checkoutRegistry().effort_policy?.[role]?.[size] ?? null;
    return { [`effort --role ${role} --size ${size}`]: { stdout: `${effort}\n` } };
  };
  const bridgeOk = { stdout: '{"ok":true,"final_message":"done","session_id":"s-1","duration_ms":2000}\n' };

  const cases = [
    ["delegate claude with max_turns", { ...effortAnswers("coder"), "claude-exec": bridgeOk }, (p) => p.callTool("delegate", { role: "coder", prompt: "build it", harness: "claude", max_turns: 7, resume: "r-1", force: true })],
    ["delegate codex read-only", { ...effortAnswers("reviewer"), "codex-exec": bridgeOk }, (p) => p.callTool("delegate", { role: "reviewer", prompt: "review it", harness: "codex" })],
    ["worker_start", { "worker start": { stdout: "started\n" } }, (p) => p.callTool("worker_start", { brief: "b.md", harness: "codex", lane: "l1", model: "m", effort: "high", resume: "r-1", breadth: "broad" })],
    ["worker_status with a lane", { "worker status": { stdout: "lane l1: running\n" } }, (p) => p.callTool("worker_status", { lane: "l1" })],
    ["supervisor status", { "supervisor status": { stdout: "running\n" } }, (p) => p.callTool("supervisor", { action: "status" })],
    ["supervisor ack", { "supervisor ack": { stdout: "acked\n" } }, (p) => p.callTool("supervisor", { action: "ack", event: "e-1" })],
    ["retry on a conductor limit flips", { "flip decide": { stdout: '{"action":"flip","reason":"red"}\n' }, "flip run": { stdout: "flipped\n" } }, async (p) => {
      const e = { error: { message: "usage_limit reached" }, agent: "conductor" };
      await p.retry(e);
      return e.decision;
    }],
  ];
  for (const [name, answers, drive] of cases) {
    test(name, async (t) => {
      const { jsCalls, goCalls, jsResult, goResult } = await inBothRuntimes(t, answers, drive);
      assert.ok(jsCalls.length > 0);
      assert.deepEqual(goCalls, jsCalls);
      assert.deepEqual(goResult?.content, jsResult?.content);
    });
  }

  test("retry runs flip decide then flip run", async (t) => {
    const { goCalls } = await inBothRuntimes(t, cases.at(-1)[1], cases.at(-1)[2]);
    assert.deepEqual(goCalls.map((c) => c.argv.slice(0, 2)), [["flip", "decide"], ["flip", "run"]]);
  });

  test("delegate exit 75 is Refused in both runtimes", async (t) => {
    const answers = { ...effortAnswers("coder"), "claude-exec": { code: 75, stdout: '{"note":"anthropic is red","resets_at":"18:00"}\n' } };
    const { jsResult, goResult } = await inBothRuntimes(t, answers, (p) => p.callTool("delegate", { role: "coder", prompt: "x", harness: "claude" }));
    assert.match(jsResult.content, /^Refused: anthropic is red\. Resets 18:00\./);
    assert.equal(goResult.content, jsResult.content);
  });
});

test("go: a spawn failure is an OrchCommandError result, not a crash (D-S10-7)", { skip }, async (t) => {
  const { sandbox, plugin } = await loadGo(t);
  fs.rmSync(path.join(sandbox.fakebin, "orch"));
  const r = await plugin.toolResult("mailbox_post", { from: "coder", subject: "hi" });
  assert.equal(r.status, "error");
  assert.equal(r.error.name, "OrchCommandError");
  assert.match(r.error.message, /^orch mailbox post exited -1: .*ENOENT/);
});

test("go: every tool and hook runs with all JS libs and scripts poisoned", { skip }, async (t) => {
  const sandbox = createSandbox(t, { runtime: "go" });
  const { plugin } = await loadGoInto(t, sandbox, {
    ...Object.fromEntries(["planner", "coder", "reviewer"].map((role) => [`effort --role ${role} --size medium`, { stdout: "high\n" }])),
    "budget gate --vendor openai": { stdout: { allowed: true } },
    "claude-exec": { stdout: '{"ok":true}\n' },
    "codex-exec": { stdout: '{"ok":true}\n' },
    "mailbox post": { stdout: { id: "m-1" } },
    "mailbox list": { stdout: [] },
    "manifest create": { stdout: { file_count: 0, manifest_sha256: "x" } },
    [`manifest verify ${path.join(sandbox.project, "t.json")}`]: { stdout: { ok: true, checked: 0, mismatched: [], missing: [] } },
    "incident record": { stdout: { recorded: "fp" } },
    "worker start": { stdout: "started\n" },
    "worker status": { stdout: "\n" },
    "supervisor status": { stdout: "running\n" },
    "flip decide": { stdout: '{"action":"stay"}\n' },
  });
  const inputs = {
    usage_status: {},
    delegate: { role: "coder", prompt: "x", harness: "claude" },
    codex_review: {},
    mailbox_post: { from: "coder", subject: "hi" },
    mailbox_list: {},
    manifest_create: { task: "t", author: "a", paths: ["a.txt"] },
    manifest_verify: { manifest: "t.json" },
    worker_start: { brief: "b.md", harness: "codex" },
    worker_status: {},
    incident: { kind: "note", summary: "s" },
    supervisor: { action: "status" },
  };
  assert.deepEqual(Object.keys(inputs).sort(), Object.keys(plugin.tools).sort());
  for (const [name, input] of Object.entries(inputs)) assert.equal((await plugin.toolResult(name, input)).status, "ok", name);
  assert.equal((await plugin.context("conductor")).length, 1);
  await plugin.executeBefore({ tool: "subagent", input: { agent: "coder-oai" } });
  await plugin.executeAfter({ status: "error", tool: "orch_delegate", error: new Error("boom"), input: {}, agent: "conductor" });
  await plugin.retry({ error: { message: "usage_limit" }, agent: "conductor" });
  assert.ok(Object.keys(plugin.syncedAgents()).length > 0);
  assert.deepEqual(poisonHits(sandbox), []);
  assert.deepEqual(calls(sandbox).filter((c) => c.self !== "path" && c.self !== "codex"), []);
});

describe("runtime configuration details", { skip }, () => {
  test("the runtime value is trimmed and case-insensitive", async (t) => {
    const sandbox = createSandbox(t, { runtime: "go" });
    const doc = await routingDocument(usageFixture("green"));
    setAnswers(sandbox, { "routing --json": { stdout: doc } });
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: " GO " } });
    assert.equal((await plugin.callTool("usage_status")).metadata.runtime, "go");
  });

  test("option binary expands a leading ~ to the home directory", async (t) => {
    const sandbox = createSandbox(t, { runtime: "go", fakeOrchOnPath: false });
    writeFakeOrch(path.join(sandbox.home, "tools", "orch"), "home-tools");
    const doc = await routingDocument(usageFixture("green"));
    setAnswers(sandbox, { "routing --json": { stdout: doc } });
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "go", binary: "~/tools/orch" } });
    assert.equal((await plugin.callTool("usage_status")).metadata.orch, path.join(sandbox.home, "tools", "orch"));
  });

  test("a misconfigured runtime is logged once by model sync instead of swallowed", async (t) => {
    const warn = t.mock.method(console, "warn", () => {});
    const sandbox = createSandbox(t, { runtime: "js" });
    await loadPlugin(t, sandbox, { options: { runtime: "rust" } });
    assert.deepEqual(warn.mock.calls.map((c) => c.arguments[0]), ['[orchestrator] model sync skipped: unknown orchestrator runtime "rust" (expected "js" or "go")']);
  });

  test("a misconfigured runtime makes the usage-limit flip warn instead of failing silently", async (t) => {
    const warn = t.mock.method(console, "warn", () => {});
    const sandbox = createSandbox(t, { runtime: "js" });
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "rust", syncModelsFromRegistry: false } });
    const e = { error: { message: "usage_limit reached" }, agent: "conductor" };
    await plugin.retry(e);
    assert.deepEqual(e.decision, { retry: false });
    assert.deepEqual(warn.mock.calls.map((c) => c.arguments[0]), ['[orchestrator] usage-limit flip skipped: unknown orchestrator runtime "rust" (expected "js" or "go")']);
  });
});

// run(), the plugin's one process runner, under cancellation and misbehaving children. The graces
// and the orch deadlines are shortened through the test-only ORCH_PLUGIN_* variables.
describe("process runner", { skip }, () => {
  const KILL_GRACE_MS = 500;
  const PIPE_GRACE_MS = 500;
  // Fake start-up under a loaded full-suite run has taken over 2 s (see the harness).
  const SLACK_MS = 5000;
  const graces = { ORCH_PLUGIN_KILL_GRACE_MS: String(KILL_GRACE_MS), ORCH_PLUGIN_PIPE_GRACE_MS: String(PIPE_GRACE_MS) };
  const delegateInput = { role: "coder", prompt: "build it", harness: "claude" };

  async function loadGoWith(t, answers, env) {
    // Registered before the sandbox's own cleanup (hooks run in order), so the call log still exists.
    let sandbox;
    t.after(() => sandbox && reapLoggedProcesses(sandbox));
    sandbox = createSandbox(t, { runtime: "go" });
    const doc = await routingDocument(usageFixture("green"));
    setAnswers(sandbox, { "routing --json": { stdout: doc }, "routing --json --refresh": { stdout: doc }, "routing --cached --json": { stdout: doc }, ...answers });
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go", ...env } });
    return { sandbox, plugin };
  }

  const isAlive = (pid) => {
    try { process.kill(pid, 0); return true; } catch (error) { if (error.code === "ESRCH") return false; throw error; }
  };
  /** Kill every logged fake process (and `sleep` holder) of a test that is still running when it ends. */
  const reapLoggedProcesses = (sandbox) => {
    if (!fs.existsSync(sandbox.log)) return;
    for (const { pid } of calls(sandbox)) if (isAlive(pid)) process.kill(pid, "SIGKILL");
  };
  // Without the fixes these cases wait forever (or for the 30 s sleep): fail them instead.
  const bounded = { timeout: 20000 };

  test("a call cancelled before it starts spawns nothing", bounded, async (t) => {
    const { sandbox, plugin } = await loadGoWith(t, { effort: { stdout: "null\n" }, "claude-exec": { hang: true } }, graces);
    const started = Date.now();
    const r = await plugin.callTool("delegate", delegateInput, { signal: AbortSignal.abort() });
    assert.ok(Date.now() - started < SLACK_MS);
    assert.match(r.content, /\|FAILED \|[\s\S]*\n\naborted before start$/);
    assert.deepEqual(orchCalls(sandbox, "claude-exec"), []);
  });

  test("a child that ignores SIGTERM is killed after the kill grace", bounded, async (t) => {
    const { sandbox, plugin } = await loadGoWith(t, { effort: { stdout: "null\n" }, "claude-exec": { hang: true, ignoreTerm: true } }, graces);
    const abort = new AbortController();
    const result = plugin.callTool("delegate", delegateInput, { signal: abort.signal });
    assert.ok(await waitFor(() => orchCalls(sandbox, "claude-exec").length === 1, 15000));
    const { pid } = orchCalls(sandbox, "claude-exec")[0];
    const aborted = Date.now();
    abort.abort();
    const r = await result;
    const elapsed = Date.now() - aborted;
    // Timers may fire a few ms early against Date.now deltas; the lower bound proves SIGKILL waited.
    assert.ok(elapsed >= KILL_GRACE_MS - 50 && elapsed < KILL_GRACE_MS + SLACK_MS, `returned ${elapsed} ms after the abort`);
    assert.match(r.content, /\|FAILED \|/);
    assert.equal(isAlive(pid), false);
  });

  test("a descendant holding stdout delays the result by the pipe grace only", bounded, async (t) => {
    const { sandbox, plugin } = await loadGoWith(t, { "mailbox list": { stdout: [], holdStdout: true } }, graces);
    const started = Date.now();
    const r = await plugin.callTool("mailbox_list");
    const elapsed = Date.now() - started;
    const holder = calls(sandbox).find((c) => c.self === "holder");
    assert.deepEqual(r, { content: "(mailbox empty)" });
    assert.ok(elapsed < PIPE_GRACE_MS + SLACK_MS, `returned after ${elapsed} ms`);
    assert.equal(isAlive(holder.pid), true, "the holder outlived the call, so the pipe grace ended the wait");
  });

  test("a child that exited before its deadline is not timed out while a descendant holds stdout", bounded, async (t) => {
    // The 30 s mailbox deadline scaled to 3 s falls inside the 6 s pipe grace that follows the exit.
    const env = { ORCH_PLUGIN_PIPE_GRACE_MS: "6000", ORCH_PLUGIN_ORCH_TIMEOUT_SCALE: "0.1" };
    const { sandbox, plugin } = await loadGoWith(t, { "mailbox list": { stdout: [], holdStdout: true } }, env);
    assert.deepEqual(await plugin.callTool("mailbox_list"), { content: "(mailbox empty)" });
    const holder = calls(sandbox).find((c) => c.self === "holder");
    assert.equal(isAlive(holder.pid), true, "the call returned through the pipe grace, past the deadline");
  });

  test("a character split across two writes decodes intact", async (t) => {
    const message = { id: "m-1", at: "2026-10-19T00:00:00.000Z", from: "coder", to: "*", kind: "note", subject: "€", body: "", refs: [], thread: null };
    const bytes = Buffer.from(JSON.stringify([message]));
    const cut = bytes.indexOf(Buffer.from("€")) + 2; // between E2 82 and AC
    const { plugin } = await loadGoWith(t, { "mailbox list": { stdoutHex: [bytes.subarray(0, cut).toString("hex"), bytes.subarray(cut).toString("hex")] } });
    assert.equal((await plugin.callTool("mailbox_list")).content, `${message.at} [note] coder -> *: € (id m-1)`);
  });

  // The deadlines are scaled down 20 times; the messages carry the configured values.
  const deadlines = [
    ["orch routing", "routing --json --refresh", (p) => p.callTool("usage_status", { refresh: true }), 90],
    ["orch budget gate", "budget gate --vendor openai", (p) => p.callTool("codex_review", {}), 90],
    ["other orch calls", "mailbox list", (p) => p.callTool("mailbox_list"), 30],
  ];
  for (const [name, key, drive, seconds] of deadlines) {
    test(`${name} times out after ${seconds}s`, async (t) => {
      const { sandbox, plugin } = await loadGoWith(t, { [key]: { hang: true } }, { ORCH_PLUGIN_ORCH_TIMEOUT_SCALE: "0.05" });
      const subcommand = key.split(" --")[0];
      await assert.rejects(drive(plugin), { name: "OrchCommandError", message: `orch ${subcommand} timed out after ${seconds}s` });
      const hung = calls(sandbox).find((c) => c.argv.join(" ").startsWith(key));
      assert.equal(isAlive(hung.pid), false);
    });
  }
});
