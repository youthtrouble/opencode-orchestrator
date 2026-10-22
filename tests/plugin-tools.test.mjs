// The plugin's state-file tools and process spawns (S10b): mailbox, manifest and incident go
// through `orch mailbox|manifest|incident`, and every spawned process (claude-exec, codex-exec,
// worker, supervisor, flip) runs as `orch <subcommand>` with the argv the 0.5 scripts took.
// Sandboxes hold only models.json and fakes, so nothing JS can run.
import test, { describe } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { calls, checkoutRegistry, createSandbox, loadPlugin, routingFixture, setAnswers, SPAWNED_SCRIPTS, typescriptSupported, waitFor, writeFakeOrch } from "./plugin-harness.mjs";

const skip = typescriptSupported ? false : "Node cannot strip TypeScript types here (needs >= 22.18)";

/** A sandbox whose fake orch answers routing like the real binary, plus `answers`; `env` adds to the plugin's. */
async function loadGo(t, answers = {}, env = {}) {
  return loadGoInto(t, createSandbox(t), answers, env);
}

async function loadGoInto(t, sandbox, answers, env = {}) {
  const doc = routingFixture("green");
  setAnswers(sandbox, { "routing --json": { stdout: doc }, "routing --json --refresh": { stdout: doc }, "routing --cached --json": { stdout: doc }, ...answers });
  const plugin = await loadPlugin(t, sandbox, { env });
  return { sandbox, plugin };
}

/** The calls made to `orch <first>...` since the log was `from` entries long. */
const orchCalls = (sandbox, first, from = 0) => calls(sandbox).slice(from).filter((c) => c.self === "path" && c.argv[0] === first);

describe("mailbox_post", { skip }, () => {
  test("orch mailbox post with the body on stdin and --json last", async (t) => {
    const message = { id: "m-1", at: "2026-10-19T00:00:00.000Z", from: "coder", to: "human", kind: "question", subject: "hi", body: "line 1\n--not-a-flag", refs: ["a.md", "b.md"], thread: "t-1", recommended: "yes", blocking: false };
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { stdout: message } });
    const r = await plugin.callTool("mailbox_post", { from: "coder", to: "human", kind: "question", subject: "hi", body: message.body, refs: message.refs, thread: "t-1", recommended: "yes", blocking: false });
    const [call] = orchCalls(sandbox, "mailbox");
    assert.deepEqual(call.argv, ["mailbox", "post", "--root", sandbox.project, "--from", "coder", "--to", "human", "--kind", "question", "--subject", "hi", "--thread", "t-1", "--ref", "a.md", "--ref", "b.md", "--recommended", "yes", "--blocking", "false", "--body", "-", "--json"]);
    assert.equal(call.stdin, message.body);
    assert.equal(call.cwd, fs.realpathSync(process.cwd()));
    assert.equal(r.content, "posted m-1");
    assert.deepEqual(r.metadata, message);
  });

  test("the defaults leave out --thread and send an empty body", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { stdout: { id: "m-2" } } });
    await plugin.callTool("mailbox_post", { from: "coder", subject: "hi" });
    const [call] = orchCalls(sandbox, "mailbox");
    assert.deepEqual(call.argv, ["mailbox", "post", "--root", sandbox.project, "--from", "coder", "--to", "*", "--kind", "note", "--subject", "hi", "--body", "-", "--json"]);
    assert.equal(call.stdin, "");
  });

  for (const [field, input] of [["subject", { subject: "--x" }], ["refs", { refs: ["ok", "--x"] }], ["thread", { thread: "--x" }]]) {
    test(`a ${field} beginning with "--" is refused before any spawn`, async (t) => {
      const { sandbox, plugin } = await loadGo(t);
      const r = await plugin.toolResult("mailbox_post", { from: "coder", subject: "hi", ...input });
      assert.equal(r.status, "error");
      assert.equal(String(r.error), `OrchCommandError: ${field} may not begin with "--"`);
      assert.equal(r.error.subcommand, "mailbox post");
      assert.equal(r.error.code, null);
      assert.deepEqual(orchCalls(sandbox, "mailbox"), []);
    });
  }

  test("recommended and blocking are left out on other kinds", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { stdout: { id: "m-3" } } });
    await plugin.callTool("mailbox_post", { from: "coder", subject: "hi", recommended: "--x", blocking: false });
    assert.deepEqual(orchCalls(sandbox, "mailbox")[0].argv, ["mailbox", "post", "--root", sandbox.project, "--from", "coder", "--to", "*", "--kind", "note", "--subject", "hi", "--body", "-", "--json"]);
  });

  test("a question's recommended beginning with \"--\" is refused before any spawn", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    const r = await plugin.toolResult("mailbox_post", { from: "coder", kind: "question", subject: "hi", recommended: "--x" });
    assert.equal(String(r.error), 'OrchCommandError: recommended may not begin with "--"');
    assert.deepEqual(orchCalls(sandbox, "mailbox"), []);
  });

  test("an action question passes --ask after --blocking; an ask beginning with \"--\" is refused", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { stdout: { id: "m-4" } } });
    await plugin.callTool("mailbox_post", { from: "conductor", to: "human", kind: "question", subject: "run setup", body: "./setup.sh", blocking: true, ask: "action" });
    assert.deepEqual(orchCalls(sandbox, "mailbox")[0].argv.slice(-7), ["--blocking", "true", "--ask", "action", "--body", "-", "--json"]);
    const r = await plugin.toolResult("mailbox_post", { from: "conductor", kind: "question", subject: "hi", ask: "--x" });
    assert.equal(String(r.error), 'OrchCommandError: ask may not begin with "--"');
  });

  test("a question without a recommended answer surfaces orch's exit 2", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox post": { code: 2, stderr: 'mailbox post: a question needs --recommended "<the answer you recommend>"\n' } });
    const r = await plugin.toolResult("mailbox_post", { from: "coder", kind: "question", subject: "hi" });
    assert.equal(r.status, "error");
    assert.equal(r.error.message, 'orch mailbox post exited 2: mailbox post: a question needs --recommended "<the answer you recommend>"');
    assert.equal(orchCalls(sandbox, "mailbox")[0].argv.includes("--recommended"), false);
  });

  test("exit 1 from orch is a tool error with the stderr line", async (t) => {
    const { plugin } = await loadGo(t, { "mailbox post": { code: 1, stderr: "Error: post needs from and subject\n" } });
    const r = await plugin.toolResult("mailbox_post", { from: "", subject: "hi" });
    assert.equal(r.status, "error");
    assert.equal(r.error.message, "orch mailbox post exited 1: Error: post needs from and subject");
  });
});

describe("mailbox_list", { skip }, () => {
  // The content is what the 0.5 JS runtime rendered for the same two messages.
  test("each message renders as its header line, refs and body", async (t) => {
    const messages = [
      { id: "m-1", at: "2026-10-22T14:18:30.415Z", from: "planner", to: "coder", kind: "brief", subject: "do it", body: "the brief", refs: ["plan.md"], thread: null, acked_by: [] },
      { id: "m-2", at: "2026-10-22T14:18:30.415Z", from: "coder", to: "*", kind: "note", subject: "done", body: "", refs: [], thread: null, acked_by: [] },
    ];
    const { sandbox, plugin } = await loadGo(t, { "mailbox list": { stdout: messages } });
    const r = await plugin.callTool("mailbox_list");
    assert.deepEqual(orchCalls(sandbox, "mailbox")[0].argv, ["mailbox", "list", "--root", sandbox.project, "--limit", "30", "--json"]);
    assert.equal(r.content, "2026-10-22T14:18:30.415Z [brief] planner -> coder: do it (id m-1)\n  refs: plan.md\n  the brief\n\n2026-10-22T14:18:30.415Z [note] coder -> *: done (id m-2)");
    assert.deepEqual(r.metadata, { count: 2 });
  });

  test("filters become flags", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "mailbox list": { stdout: [] } });
    await plugin.callTool("mailbox_list", { to: "coder", unacked_by: "coder", kind: "brief", limit: 5 });
    assert.deepEqual(orchCalls(sandbox, "mailbox")[0].argv, ["mailbox", "list", "--root", sandbox.project, "--to", "coder", "--unacked-by", "coder", "--kind", "brief", "--limit", "5", "--json"]);
  });

  // orch would read `--to --x` as a bare --to and list the whole mailbox.
  for (const field of ["to", "unacked_by", "kind"]) {
    test(`a ${field} filter beginning with "--" is refused before any spawn`, async (t) => {
      const { sandbox, plugin } = await loadGo(t, { "mailbox list": { stdout: [] } });
      const r = await plugin.toolResult("mailbox_list", { [field]: "--x" });
      assert.equal(String(r.error), `OrchCommandError: ${field} may not begin with "--"`);
      assert.deepEqual(orchCalls(sandbox, "mailbox"), []);
    });
  }

  test("an empty list is (mailbox empty)", async (t) => {
    const { plugin } = await loadGo(t, { "mailbox list": { stdout: [] } });
    assert.deepEqual(await plugin.callTool("mailbox_list"), { content: "(mailbox empty)" });
  });
});

describe("manifest_create and manifest_verify", { skip }, () => {
  test("orch writes the manifest; the plugin reports it", async (t) => {
    const out = (sandbox) => path.join(sandbox.project, ".orchestrator", "manifests", "t1.json");
    const { sandbox, plugin } = await loadGo(t, { "manifest create": { stdout: { out: "ignored", file_count: 2, manifest_sha256: "abc123" } } });
    const r = await plugin.callTool("manifest_create", { task: "t1", author: "coder", paths: ["a.txt", "src"], baseline: "base.json" });
    assert.deepEqual(orchCalls(sandbox, "manifest")[0].argv, ["manifest", "create", "--root", sandbox.project, "--task", "t1", "--author", "coder", "--out", out(sandbox), "--baseline", "base.json", "a.txt", "src"]);
    assert.equal(r.content, "manifest .orchestrator/manifests/t1.json: 2 files, sha256 abc123");
    assert.deepEqual(r.metadata, { path: out(sandbox), manifest_sha256: "abc123" });
    assert.equal(fs.existsSync(out(sandbox)), false);
  });

  test("no paths surfaces orch's usage error (D-S10-1)", async (t) => {
    const { plugin } = await loadGo(t, { "manifest create": { code: 2, stderr: "usage: orch manifest create --task T --author A --out file.json [--baseline b.json] [--root dir] <paths...>\n" } });
    const r = await plugin.toolResult("manifest_create", { task: "t1", author: "coder", paths: [] });
    assert.equal(r.status, "error");
    assert.match(r.error.message, /^orch manifest create exited 2: usage: /);
  });

  test("a path beginning with \"--\" is refused before any spawn", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    const r = await plugin.toolResult("manifest_create", { task: "t1", author: "coder", paths: ["a.txt", "--root"] });
    assert.equal(r.error.message, 'paths may not begin with "--"');
    assert.deepEqual(orchCalls(sandbox, "manifest"), []);
  });

  test("an .orchestrator outside the project is refused before manifests/ is created (C2-F2)", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    fs.writeFileSync(path.join(sandbox.project, "a.txt"), "a\n");
    const outside = path.join(sandbox.root, "outside");
    fs.mkdirSync(outside);
    const state = path.join(sandbox.project, ".orchestrator");
    fs.rmSync(state, { recursive: true, force: true });
    fs.symlinkSync(outside, state);
    const r = await plugin.toolResult("manifest_create", { task: "t1", author: "coder", paths: ["a.txt"] });
    assert.equal(r.status, "error");
    assert.equal(r.error.message, `manifest directory is outside the project: ${path.join(state, "manifests")}`);
    assert.deepEqual(fs.readdirSync(outside), []);
    assert.deepEqual(orchCalls(sandbox, "manifest"), []);
  });

  test("a manifests directory outside the project is refused (R15)", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    fs.writeFileSync(path.join(sandbox.project, "a.txt"), "a\n");
    const outside = path.join(sandbox.root, "outside");
    fs.mkdirSync(outside);
    const dir = path.join(sandbox.project, ".orchestrator", "manifests");
    fs.mkdirSync(path.dirname(dir), { recursive: true });
    fs.symlinkSync(outside, dir);
    const r = await plugin.toolResult("manifest_create", { task: "t1", author: "coder", paths: ["a.txt"] });
    assert.equal(r.status, "error");
    assert.equal(r.error.message, `manifest directory is outside the project: ${dir}`);
    assert.deepEqual(fs.readdirSync(outside), []);
    assert.deepEqual(orchCalls(sandbox, "manifest"), []);
  });

  // The harness skips opencode's schema check, so a non-string task reaches the plugin as-is.
  for (const task of ["../x", "a/b", "a\\b", "..", ".", "", "a\0b", 42]) {
    test(`task ${JSON.stringify(task)} is refused and nothing is written`, async (t) => {
      const { sandbox, plugin } = await loadGo(t);
      fs.writeFileSync(path.join(sandbox.project, "a.txt"), "a\n");
      const r = await plugin.toolResult("manifest_create", { task, author: "coder", paths: ["a.txt"] });
      assert.equal(r.status, "error");
      assert.equal(r.error.message, `manifest task must be a single file name: ${JSON.stringify(task)}`);
      assert.deepEqual(orchCalls(sandbox, "manifest"), []);
      assert.equal(fs.existsSync(path.join(sandbox.project, ".orchestrator")), false);
    });
  }

  // The fake orch answers by the tokens before the first flag, which include the manifest path.
  const verifyKey = (sandbox, file) => `manifest verify ${path.join(sandbox.project, file)}`;
  const verifyCases = [
    ["OK on exit 0", { stdout: '{"ok":true,"checked":2,"mismatched":[],"missing":[]}\n' }, "OK: all 2 files match"],
    ["MISMATCH on exit 1 with JSON", { code: 1, stdout: '{"ok":false,"checked":2,"mismatched":["a.txt"],"missing":["b.txt"]}\n' }, "MISMATCH: changed a.txt; missing b.txt"],
  ];
  for (const [name, answer, content] of verifyCases) {
    test(`verify gives ${name}`, async (t) => {
      const sandbox = createSandbox(t);
      const { plugin } = await loadGoInto(t, sandbox, { [verifyKey(sandbox, ".orchestrator/manifests/t1.json")]: answer });
      const r = await plugin.callTool("manifest_verify", { manifest: ".orchestrator/manifests/t1.json" });
      assert.deepEqual(orchCalls(sandbox, "manifest")[0].argv, ["manifest", "verify", path.join(sandbox.project, ".orchestrator", "manifests", "t1.json"), "--root", sandbox.project]);
      assert.equal(r.content, content);
      assert.deepEqual(r.metadata, JSON.parse(answer.stdout));
    });
  }

  test("verify exit 1 without JSON is an OrchCommandError", async (t) => {
    const sandbox = createSandbox(t);
    const { plugin } = await loadGoInto(t, sandbox, { [verifyKey(sandbox, "broken.json")]: { code: 1, stderr: "SyntaxError: Unexpected token\n" } });
    const r = await plugin.toolResult("manifest_verify", { manifest: "broken.json" });
    assert.equal(r.error.name, "OrchCommandError");
    assert.equal(r.error.message, "orch manifest verify exited 1: SyntaxError: Unexpected token");
  });
});

describe("incident", { skip }, () => {
  test("the whole incident goes to orch on stdin, none of its text in argv", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "incident record": { stdout: '{"recorded":"fp123","at":"2026-10-19T00:00:00.000Z"}\n' } });
    const r = await plugin.callTool("incident", { kind: "misroute", summary: "wrong lane", detail: "--details--" });
    const [call] = orchCalls(sandbox, "incident");
    assert.deepEqual(call.argv, ["incident", "record", "--request", "-"]);
    assert.deepEqual(JSON.parse(call.stdin), { component: "agent", kind: "misroute", summary: "wrong lane", detail: "--details--", context: { agent: "conductor", session: "ses_test" }, project: sandbox.project });
    assert.equal(r.content, "recorded incident fp123");
  });

  test("execute.after records an orch_* error with a null project when there is no directory", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "incident record": { stdout: '{"recorded":"fp9"}\n' } });
    plugin.api.location = {};
    await plugin.executeAfter({ status: "error", tool: "orch_mailbox_post", error: new Error("boom"), input: { a: 1 }, agent: "build" });
    const [call] = orchCalls(sandbox, "incident");
    assert.deepEqual(call.argv, ["incident", "record", "--request", "-"]);
    assert.deepEqual(JSON.parse(call.stdin), { component: "plugin", kind: "bug", summary: "tool orch_mailbox_post errored: boom", detail: '{"a":1}', context: { agent: "build" }, project: null });
  });

  test("flag-like text reaches orch intact, since none of it is argv", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "incident record": { stdout: '{"recorded":"fp1"}\n' } });
    await plugin.callTool("incident", { kind: "--x", summary: "--y" });
    const [call] = orchCalls(sandbox, "incident");
    assert.equal(call.argv.some((a) => a.includes("--x") || a.includes("--y")), false);
    assert.deepEqual([JSON.parse(call.stdin).kind, JSON.parse(call.stdin).summary], ["--x", "--y"]);
  });
});

describe("spawn tools: orch <subcommand> takes the argv the 0.5 scripts took", { skip }, () => {
  /** The spawned-subcommand calls, with the sandbox root (and its realpath) replaced by <root>. */
  function spawnedCalls(sandbox) {
    const roots = [fs.realpathSync(sandbox.root), sandbox.root];
    const normalize = (s) => roots.reduce((acc, r) => acc.replaceAll(r, "<root>"), s);
    return calls(sandbox)
      .filter((c) => c.self === "path" && SPAWNED_SCRIPTS.includes(c.argv[0]))
      .map((c) => ({ argv: c.argv.map(normalize), cwd: normalize(c.cwd), stdin: normalize(c.stdin) }));
  }

  const effortAnswers = (role, size = "medium") => {
    const effort = checkoutRegistry().effort_policy?.[role]?.[size] ?? null;
    return { [`effort --role ${role} --size ${size}`]: { stdout: `${effort}\n` } };
  };
  const bridgeOk = { stdout: '{"ok":true,"final_message":"done","session_id":"s-1","duration_ms":2000}\n' };
  const preface = (role, rule) => `You are acting as the ${role} for an orchestrated project at <root>/project. Work only within this brief, report precisely, mark anything not done or not verified. ${rule}\n\n`;
  const inProject = (argv, stdin = "") => ({ argv, cwd: "<root>/project", stdin });

  // Each expected call list and result is what the 0.5 JS scripts were given and returned for the same input.
  // The model ids pinned below (claude-opus-5-5, gpt-6.1-sol) come from the routing fixtures in
  // tests/fixtures/routing, not from models.json: update them whenever the fixtures are regenerated.
  const cases = [
    ["delegate claude with max_turns", { ...effortAnswers("coder"), "claude-exec": bridgeOk },
      async (p) => (await p.callTool("delegate", { role: "coder", prompt: "build it", harness: "claude", max_turns: 7, resume: "r-1", force: true })).content,
      [inProject(["claude-exec", "--cwd", "<root>/project", "--model", "claude-opus-5-5", "--effort", "medium", "--timeout-min", "90", "--quiet", "--resume", "r-1", "--force", "--permission-mode", "auto", "--max-turns", "7"], preface("CODER", "You may edit files in scope.") + "build it")],
      "[claude claude-opus-5-5 effort=medium size=medium |ok | 2s | resume=s-1 | record=n/a]\n\ndone"],
    ["delegate codex read-only", { ...effortAnswers("reviewer"), "codex-exec": bridgeOk },
      async (p) => (await p.callTool("delegate", { role: "reviewer", prompt: "review it", harness: "codex" })).content,
      [inProject(["codex-exec", "--cwd", "<root>/project", "--model", "gpt-6.1-sol", "--effort", "medium", "--timeout-min", "90", "--quiet", "--sandbox", "read-only"], preface("REVIEWER", "Do NOT modify files; produce your output as text (or write only to .orchestrator/ if the brief says so).") + "review it")],
      "[codex gpt-6.1-sol effort=medium size=medium |ok | 2s | resume=s-1 | record=n/a]\n\ndone"],
    ["worker_start", { "worker start": { stdout: "started\n" } },
      async (p) => (await p.callTool("worker_start", { brief: "b.md", harness: "codex", lane: "l1", model: "m", effort: "high", resume: "r-1", breadth: "broad" })).content,
      [inProject(["worker", "start", "--brief", "b.md", "--harness", "codex", "--cwd", "<root>/project", "--detach", "--lane", "l1", "--model", "m", "--effort", "high", "--resume", "r-1", "--breadth", "broad"])],
      "started"],
    ["worker_status with a lane", { "worker status": { stdout: "lane l1: running\n" } },
      async (p) => (await p.callTool("worker_status", { lane: "l1" })).content,
      [inProject(["worker", "status", "--cwd", "<root>/project", "--lane", "l1"])],
      "lane l1: running"],
    ["supervisor status", { "supervisor status": { stdout: "running\n" } },
      async (p) => (await p.callTool("supervisor", { action: "status" })).content,
      [inProject(["supervisor", "status", "--cwd", "<root>/project"])],
      "running"],
    ["supervisor ack", { "supervisor ack": { stdout: "acked\n" } },
      async (p) => (await p.callTool("supervisor", { action: "ack", event: "e-1" })).content,
      [inProject(["supervisor", "ack", "--cwd", "<root>/project", "--event", "e-1"])],
      "acked"],
    ["retry on a conductor limit runs flip decide, then flip run", { "flip decide": { stdout: '{"action":"flip","reason":"red"}\n' }, "flip run": { stdout: "flipped\n" } },
      async (p) => {
        const e = { error: { message: "usage_limit reached" }, agent: "conductor" };
        await p.retry(e);
        return e.decision;
      },
      [
        inProject(["flip", "decide", "--active", "opencode", "--cwd", "<root>/project"]),
        inProject(["flip", "run", "--to", "claude", "--from", "opencode", "--reason", "opencode conductor hit a usage limit: usage_limit reached", "--cwd", "<root>/project"]),
      ],
      { retry: false }],
  ];
  for (const [name, answers, drive, expectedCalls, expectedResult] of cases) {
    test(name, async (t) => {
      const { sandbox, plugin } = await loadGo(t, answers);
      assert.deepEqual(await drive(plugin), expectedResult);
      assert.deepEqual(spawnedCalls(sandbox), expectedCalls);
    });
  }

  test("delegate exit 75 is Refused", async (t) => {
    const { plugin } = await loadGo(t, { ...effortAnswers("coder"), "claude-exec": { code: 75, stdout: '{"note":"anthropic is red","resets_at":"18:00"}\n' } });
    const r = await plugin.callTool("delegate", { role: "coder", prompt: "x", harness: "claude" });
    assert.equal(r.content, "Refused: anthropic is red. Resets 18:00. Try another role/harness or force=true.");
  });
});

// Incident 3ad1e76b: a verifier defaulted to plan mode / a read-only sandbox, which blocks the
// tests, builds and report writing that are its whole job.
describe("delegate: write access by role", { skip }, () => {
  const bridgeOk = { stdout: '{"ok":true,"final_message":"done"}\n' };

  /** The value after `flag` in the bridge argv a delegate call spawned. */
  /** The bridge call (argv and stdin) a delegate call spawned. */
  async function bridgeCall(t, input) {
    const bridge = `${input.harness}-exec`;
    const { sandbox, plugin } = await loadGo(t, { effort: { stdout: "null\n" }, [bridge]: bridgeOk });
    await plugin.callTool("delegate", { prompt: "x", ...input });
    return orchCalls(sandbox, bridge)[0];
  }

  /** The value after `flag` in the bridge argv a delegate call spawned. */
  async function bridgeFlag(t, input, flag) {
    const { argv } = await bridgeCall(t, input);
    return argv[argv.indexOf(flag) + 1];
  }

  const noProductEdits = /do NOT modify product code/;

  test("claude: the verifier gets --permission-mode auto by default", async (t) => {
    assert.equal(await bridgeFlag(t, { role: "verifier", harness: "claude" }, "--permission-mode"), "auto");
  });

  test("claude: the reviewer gets --permission-mode plan by default", async (t) => {
    assert.equal(await bridgeFlag(t, { role: "reviewer", harness: "claude" }, "--permission-mode"), "plan");
  });

  test("claude: an explicit write:false still gives the verifier plan", async (t) => {
    assert.equal(await bridgeFlag(t, { role: "verifier", harness: "claude", write: false }, "--permission-mode"), "plan");
  });

  test("codex: the verifier gets --sandbox workspace-write by default", async (t) => {
    assert.equal(await bridgeFlag(t, { role: "verifier", harness: "codex" }, "--sandbox"), "workspace-write");
  });

  test("codex: the reviewer gets --sandbox read-only by default", async (t) => {
    assert.equal(await bridgeFlag(t, { role: "reviewer", harness: "codex" }, "--sandbox"), "read-only");
  });

  test("the verifier is told not to modify product code; the coder is told it may edit", async (t) => {
    const verifier = await bridgeCall(t, { role: "verifier", harness: "claude" });
    assert.match(verifier.stdin, noProductEdits);
    assert.doesNotMatch(verifier.stdin, /You may edit files in scope/);
    const coder = await bridgeCall(t, { role: "coder", harness: "claude" });
    assert.match(coder.stdin, /You may edit files in scope\./);
    assert.doesNotMatch(coder.stdin, noProductEdits);
  });
});

test("a spawn failure is an OrchCommandError result, not a crash (D-S10-7)", { skip }, async (t) => {
  const { sandbox, plugin } = await loadGo(t);
  fs.rmSync(path.join(sandbox.fakebin, "orch"));
  const r = await plugin.toolResult("mailbox_post", { from: "coder", subject: "hi" });
  assert.equal(r.status, "error");
  assert.equal(r.error.name, "OrchCommandError");
  assert.match(r.error.message, /^orch mailbox post exited -1: .*ENOENT/);
});

test("every tool and hook runs on orch alone", { skip }, async (t) => {
  const sandbox = createSandbox(t);
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
  assert.deepEqual(calls(sandbox).filter((c) => c.self !== "path" && c.self !== "codex"), []);
});

describe("runtime configuration details", { skip }, () => {
  test("the runtime value is trimmed and case-insensitive", async (t) => {
    const sandbox = createSandbox(t);
    setAnswers(sandbox, { "routing --json": { stdout: routingFixture("green") } });
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: " GO " } });
    assert.equal((await plugin.callTool("usage_status")).metadata.runtime, "go");
  });

  test("option binary expands a leading ~ to the home directory", async (t) => {
    const sandbox = createSandbox(t, { fakeOrchOnPath: false });
    writeFakeOrch(path.join(sandbox.home, "tools", "orch"), "home-tools");
    setAnswers(sandbox, { "routing --json": { stdout: routingFixture("green") } });
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "go", binary: "~/tools/orch" } });
    assert.equal((await plugin.callTool("usage_status")).metadata.orch, path.join(sandbox.home, "tools", "orch"));
  });

  test("a misconfigured runtime is logged once by model sync instead of swallowed", async (t) => {
    const warn = t.mock.method(console, "warn", () => {});
    const sandbox = createSandbox(t);
    await loadPlugin(t, sandbox, { options: { runtime: "rust" }, brokenRuntime: true });
    assert.deepEqual(warn.mock.calls.map((c) => c.arguments[0]), ['[orchestrator] model sync skipped: unknown orchestrator runtime "rust" (expected "go")']);
  });

  test("a misconfigured runtime makes the usage-limit flip warn instead of failing silently", async (t) => {
    const warn = t.mock.method(console, "warn", () => {});
    const sandbox = createSandbox(t);
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "rust", syncModelsFromRegistry: false }, brokenRuntime: true });
    const e = { error: { message: "usage_limit reached" }, agent: "conductor" };
    await plugin.retry(e);
    assert.deepEqual(e.decision, { retry: false });
    assert.deepEqual(warn.mock.calls.map((c) => c.arguments[0]), ['[orchestrator] usage-limit flip skipped: unknown orchestrator runtime "rust" (expected "go")']);
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

  // Every fake ends by itself (see writeFakeOrch), so a test leaves no process to clean up.
  async function loadGoWith(t, answers, env) {
    return loadGoInto(t, createSandbox(t), answers, env);
  }

  // A read-only probe: a pid reused since the log was written can only make it answer wrongly,
  // never harm anything.
  const isAlive = (pid) => {
    try { process.kill(pid, 0); return true; } catch (error) { if (error.code === "ESRCH") return false; throw error; }
  };
  // Without the fixes these cases wait for the hang ceiling (or the 30 s sleep): fail them sooner.
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

  test("a spawn failure clears the call's deadline (R8)", bounded, async (t) => {
    // The 30 s mailbox deadline scaled to 1.5 s; with the binary gone, spawn emits "error" and "close", never "exit".
    const deadlineMs = 1500;
    const { sandbox, plugin } = await loadGoWith(t, {}, { ORCH_PLUGIN_ORCH_TIMEOUT_SCALE: "0.05" });
    fs.rmSync(path.join(sandbox.fakebin, "orch"));
    const deadlines = [];
    const cleared = new Set();
    const { setTimeout: realSetTimeout, clearTimeout: realClearTimeout } = globalThis;
    t.mock.method(globalThis, "setTimeout", (fn, ms, ...args) => {
      const handle = realSetTimeout(fn, ms, ...args);
      if (ms === deadlineMs) deadlines.push(handle);
      return handle;
    });
    t.mock.method(globalThis, "clearTimeout", (handle) => {
      cleared.add(handle);
      return realClearTimeout(handle);
    });
    await assert.rejects(plugin.callTool("mailbox_list"));
    assert.equal(deadlines.length, 1);
    assert.ok(deadlines.every((handle) => cleared.has(handle)), "the deadline timer outlived the failed spawn");
  });

  test("a hung fake ends by itself at the hang ceiling (R16)", bounded, async (t) => {
    const sandbox = createSandbox(t);
    setAnswers(sandbox, { "mailbox list": { hang: true } });
    const env = { ...process.env, PLUGIN_TEST_LOG: sandbox.log, PLUGIN_TEST_ANSWERS: sandbox.answersFile, PLUGIN_TEST_HANG_CEILING_MS: "300" };
    const child = spawn(path.join(sandbox.fakebin, "orch"), ["mailbox", "list"], { env, stdio: ["ignore", "ignore", "pipe"] });
    let stderr = "";
    child.stderr.setEncoding("utf8").on("data", (chunk) => (stderr += chunk));
    // The child is this test's own: stop it if it outlives the check.
    const ended = new Promise((resolve) => child.on("close", (code) => resolve(code)));
    const watchdog = setTimeout(() => child.kill("SIGKILL"), 5000);
    const code = await ended;
    clearTimeout(watchdog);
    assert.equal(code, 98);
    assert.equal(stderr, "fake orch: hang ceiling reached\n");
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

describe("conductor session heartbeat", { skip }, () => {
  const sessionFile = (sandbox) => path.join(sandbox.project, ".orchestrator", "sessions", "opencode.json");
  const readBeat = (sandbox) => { try { return JSON.parse(fs.readFileSync(sessionFile(sandbox), "utf8")); } catch { return null; } };
  const makeProject = (sandbox) => fs.mkdirSync(path.join(sandbox.project, ".orchestrator"), { recursive: true });
  const beatKey = (sandbox, session, state) => `session beat --door opencode --session ${session} --state ${state} --cwd ${sandbox.project}`;
  const beats = (sandbox) => orchCalls(sandbox, "session").map((c) => `${c.argv[5]} ${c.argv[7]}`);
  const interval = (ms) => ({ ORCH_PLUGIN_BEAT_INTERVAL_MS: String(ms) });
  const idle = (sessionID) => ({ type: "session.idle", data: { sessionID } });
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const OK = { "session beat": { stdout: "" } };

  // R1: opencode 2.0.32's event.subscribe returns an async iterable; a callback is never called.
  test("idle arrives through the event.subscribe async iterable, which unloading ends", async (t) => {
    const { sandbox, plugin } = await loadGo(t, OK, interval(50));
    makeProject(sandbox);
    assert.equal(plugin.subscriptions.length, 1);
    assert.ok(plugin.subscriptions[0].options.signal instanceof AbortSignal);
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    plugin.emit(idle("ses_1"));
    assert.ok(await waitFor(() => beats(sandbox).length === 2));
    assert.deepEqual(orchCalls(sandbox, "session").map((c) => c.argv), [beatKey(sandbox, "ses_1", "working").split(" "), beatKey(sandbox, "ses_1", "waiting").split(" ")]);
    await plugin.dispose();
    assert.ok(await waitFor(() => plugin.subscriptions[0].ended), "the pump stops iterating once the plugin unloads");
  });

  test("a failing event subscription is recorded as an incident", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "incident record": { stdout: { recorded: "fp" } } });
    plugin.subscriptions[0].fail(new Error("stream lost"));
    assert.ok(await waitFor(() => orchCalls(sandbox, "incident").length === 1));
    assert.equal(JSON.parse(orchCalls(sandbox, "incident")[0].stdin).summary, "event subscription failed: stream lost");
  });

  // R2: state changes used to bypass the limiter, one spawn per transition.
  test("rapid working/waiting transitions write once per interval, the latest state last", async (t) => {
    const { sandbox, plugin } = await loadGo(t, OK, interval(1000));
    makeProject(sandbox);
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    plugin.emit(idle("ses_1"));
    await plugin.context("conductor", "ses_1");
    plugin.emit(idle("ses_1"));
    await sleep(400);
    assert.deepEqual(beats(sandbox), ["ses_1 working"]);
    assert.ok(await waitFor(() => beats(sandbox).length === 2, 3000));
    await sleep(1200);
    assert.deepEqual(beats(sandbox), ["ses_1 working", "ses_1 waiting"]);
  });

  // R3: a slow working write used to race the idle write and could land last.
  test("a slow write finishes before the next beat starts", async (t) => {
    const { sandbox, plugin } = await loadGo(t, {}, interval(50));
    makeProject(sandbox);
    setAnswers(sandbox, { ...sandbox.answers, [beatKey(sandbox, "ses_1", "working")]: { stdoutHex: Array(6).fill("20") }, [beatKey(sandbox, "ses_1", "waiting")]: { stdout: "" } });
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    plugin.emit(idle("ses_1"));
    await sleep(300);
    assert.deepEqual(beats(sandbox), ["ses_1 working"], "waiting waits for the working write in flight");
    assert.ok(await waitFor(() => beats(sandbox).length === 2, 3000));
    assert.deepEqual(beats(sandbox), ["ses_1 working", "ses_1 waiting"]);
  });

  // R4: an idle from a session that no longer owns the door used to overwrite the active one.
  test("an idle from a superseded conductor session is ignored", async (t) => {
    const { sandbox, plugin } = await loadGo(t, OK, interval(50));
    makeProject(sandbox);
    await plugin.context("conductor", "ses_a");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    await plugin.context("conductor", "ses_b");
    assert.ok(await waitFor(() => beats(sandbox).length === 2));
    plugin.emit(idle("ses_a"));
    await sleep(300);
    assert.deepEqual(beats(sandbox), ["ses_a working", "ses_b working"]);
    plugin.emit(idle("ses_b"));
    assert.ok(await waitFor(() => beats(sandbox).length === 3));
    assert.equal(beats(sandbox)[2], "ses_b waiting");
  });

  // R5: unloading used to return while a beat was still being written, so the unloaded instance's
  // late write could land over the replacement instance's heartbeat.
  test("unloading waits for the beat in flight, so a reloaded plugin's beat is the last word", async (t) => {
    const sandbox = createSandbox(t);
    writeDelayedSessionWriter(sandbox, { ses_old: 600, ses_new: 0 });
    const old = await loadGoInto(t, sandbox, {}, interval(50));
    makeProject(sandbox);
    await old.plugin.context("conductor", "ses_old");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    await old.plugin.dispose();
    assert.equal(readBeat(sandbox)?.session_id, "ses_old", "dispose returned before the old write landed");
    const replacement = await loadGoInto(t, sandbox, {}, interval(50));
    await replacement.plugin.context("conductor", "ses_new");
    assert.ok(await waitFor(() => readBeat(sandbox)?.session_id === "ses_new"));
    await sleep(800);
    assert.equal(readBeat(sandbox).session_id, "ses_new");
  });

  test("a trailing beat pending at unload never starts", async (t) => {
    const { sandbox, plugin } = await loadGo(t, OK, interval(300));
    makeProject(sandbox);
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    plugin.emit(idle("ses_1"));
    await sleep(50);
    await plugin.dispose();
    await plugin.context("conductor", "ses_1");
    await sleep(600);
    assert.deepEqual(beats(sandbox), ["ses_1 working"]);
  });

  test("unloading during a failing beat settles after its incident is recorded", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "session beat": { stdoutHex: Array(4).fill("20"), code: 1 }, "incident record": { stdout: { recorded: "fp" } } }, interval(50));
    makeProject(sandbox);
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => beats(sandbox).length === 1));
    await plugin.dispose();
    assert.equal(orchCalls(sandbox, "incident").length, 1, "the incident is recorded before dispose returns");
  });

  test("no beat for another agent, an unseen idle session, or a directory without .orchestrator/", async (t) => {
    const { sandbox, plugin } = await loadGo(t, OK, interval(50));
    await plugin.context("conductor", "ses_1");
    makeProject(sandbox);
    await plugin.context("build", "ses_2");
    plugin.emit(idle("ses_3"));
    await sleep(300);
    assert.deepEqual(orchCalls(sandbox, "session"), []);
  });

  test("a failed beat is recorded as an incident and the turn still gets its budget block", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { "session beat": { code: 1, stderr: "session beat: could not write .orchestrator/sessions/opencode.json: EACCES\n" }, "incident record": { stdout: { recorded: "fp" } } });
    makeProject(sandbox);
    const system = await plugin.context("conductor", "ses_1");
    assert.equal(system.length, 1);
    assert.ok(await waitFor(() => orchCalls(sandbox, "incident").length === 1));
    const incident = JSON.parse(orchCalls(sandbox, "incident")[0].stdin);
    assert.equal(incident.summary, "session beat failed: orch session beat exited 1: session beat: could not write .orchestrator/sessions/opencode.json: EACCES");
    assert.deepEqual(incident.context, { session: "ses_1" });
  });

  test("a failed beat is retried by the next request after the interval, not before", async (t) => {
    const failing = { "session beat": { code: 1, stderr: "session beat: EACCES\n" }, "incident record": { stdout: { recorded: "fp" } } };
    const { sandbox, plugin } = await loadGo(t, failing, interval(400));
    makeProject(sandbox);
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => orchCalls(sandbox, "incident").length === 1));
    setAnswers(sandbox, { ...sandbox.answers, ...OK });
    await plugin.context("conductor", "ses_1");
    await sleep(150);
    assert.deepEqual(beats(sandbox), ["ses_1 working"], "no immediate retry inside the interval");
    await sleep(400);
    await plugin.context("conductor", "ses_1");
    assert.ok(await waitFor(() => beats(sandbox).length === 2));
  });
});

/**
 * Puts an `orch` in front of the sandbox's fake that really writes the session file for
 * `session beat`, after a delay chosen by session id; every other call goes to the fake.
 */
function writeDelayedSessionWriter(sandbox, delaysBySession) {
  const scripted = path.join(sandbox.fakebin, "orch-scripted");
  fs.renameSync(path.join(sandbox.fakebin, "orch"), scripted);
  fs.writeFileSync(path.join(sandbox.fakebin, "orch"), `#!${process.execPath}
const fs = process.getBuiltinModule("node:fs");
const path = process.getBuiltinModule("node:path");
const argv = process.argv.slice(2);
if (argv[0] !== "session") {
  const r = process.getBuiltinModule("node:child_process").spawnSync(${JSON.stringify(scripted)}, argv, { stdio: "inherit" });
  process.exit(r.status ?? 1);
}
fs.appendFileSync(process.env.PLUGIN_TEST_LOG, JSON.stringify({ self: "path", argv, cwd: process.cwd(), stdin: "" }) + "\\n");
const option = (name) => argv[argv.indexOf(name) + 1];
const delays = ${JSON.stringify(delaysBySession)};
setTimeout(() => {
  const file = path.join(option("--cwd"), ".orchestrator", "sessions", "opencode.json");
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify({ door: "opencode", session_id: option("--session"), state: option("--state") }));
}, delays[option("--session")] ?? 0);
`, { mode: 0o755 });
}
