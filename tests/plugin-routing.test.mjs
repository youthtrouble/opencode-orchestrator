// The plugin's runtime switch (S10a): selection, orch binary resolution, the fail-loudly broken
// runtime, and the routing-domain calls (routing, cached routing, effort, budget gate, registry)
// in both runtimes. Go sandboxes poison lib/{usage,routing,budget}.mjs, so any JS lib use fails.
import test, { describe } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { calls, checkoutRegistry, openaiHeadroom, createSandbox, loadPlugin, poisonHits, routingDocument, setAnswers, typescriptSupported, usageFixture, waitFor, writeFakeOrch } from "./plugin-harness.mjs";

const skip = typescriptSupported ? false : "Node cannot strip TypeScript types here (needs >= 22.18)";
const argvOf = (sandbox) => calls(sandbox).filter((c) => c.self !== "codex").map((c) => c.argv.join(" "));

/** A go sandbox whose fake orch answers routing like the real binary would for `cache`. */
async function goSandbox(t, { cache = "green", cacheAgeMs = 0, answers = {}, ...rest } = {}) {
  const sandbox = createSandbox(t, { runtime: "go", cache, cacheAgeMs, ...rest });
  const usage = usageFixture(cache, { ageMs: cacheAgeMs });
  const doc = await routingDocument(usage);
  const cachedDoc = await routingDocument(cacheAgeMs > 5 * 60 * 1000 ? { ...usage, stale: true } : usage);
  setAnswers(sandbox, {
    "routing --json": { stdout: doc },
    "routing --json --refresh": { stdout: doc },
    "routing --cached --json": { stdout: cachedDoc },
    ...answers,
  });
  return { sandbox, doc };
}

async function loadGo(t, opts = {}, load = {}) {
  const { sandbox, doc } = await goSandbox(t, opts);
  const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go" }, ...load });
  return { sandbox, doc, plugin };
}

async function loadJs(t, { cache = "green", cacheAgeMs = 0 } = {}, load = {}) {
  const sandbox = createSandbox(t, { runtime: "js", cache, cacheAgeMs });
  const plugin = await loadPlugin(t, sandbox, load);
  return { sandbox, plugin };
}

/** getRouting() from the lib copy in this sandbox: what JS mode produced before the switch. */
async function sandboxLibRouting(sandbox) {
  const usageLib = await import(pathToFileURL(path.join(sandbox.orchestrator, "lib", "usage.mjs")).href + "?expected");
  return usageLib.getRouting({});
}

describe("runtime selection", { skip }, () => {
  test("default is js: real libs, no orch call", async (t) => {
    const sandbox = createSandbox(t, { runtime: "js", fakeOrchOnPath: true });
    const plugin = await loadPlugin(t, sandbox);
    const r = await plugin.callTool("usage_status");
    assert.equal(r.metadata.runtime, "js");
    assert.equal(r.metadata.orch, null);
    assert.equal(r.content, (await sandboxLibRouting(sandbox)).prompt);
    assert.deepEqual(calls(sandbox), []);
  });

  test("ORCH_RUNTIME=go selects go", async (t) => {
    const { sandbox, plugin, doc } = await loadGo(t);
    const r = await plugin.callTool("usage_status");
    assert.equal(r.metadata.runtime, "go");
    assert.equal(r.metadata.orch, path.join(sandbox.fakebin, "orch"));
    assert.equal(r.content, doc.prompt);
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("option js beats env go", async (t) => {
    const sandbox = createSandbox(t, { runtime: "js", fakeOrchOnPath: true });
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "js" }, env: { ORCH_RUNTIME: "go" } });
    assert.equal((await plugin.callTool("usage_status")).metadata.runtime, "js");
    assert.deepEqual(calls(sandbox), []);
  });

  test("option go beats env js", async (t) => {
    const { sandbox, plugin } = await loadGo(t, {}, { options: { runtime: "go" }, env: { ORCH_RUNTIME: "js" } });
    assert.equal((await plugin.callTool("usage_status")).metadata.runtime, "go");
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("an unknown runtime is an OrchConfigError and nothing degrades loudly in the hooks", async (t) => {
    const { sandbox } = await goSandbox(t);
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "rust" }, brokenRuntime: true });
    await assert.rejects(plugin.callTool("usage_status"), { name: "OrchConfigError", message: 'unknown orchestrator runtime "rust" (expected "js" or "go")' });
    await assertDegraded(plugin, sandbox);
  });
});

/** A broken runtime: the context hook pushes nothing, the gate does not throw, no lib or orch is touched. */
async function assertDegraded(plugin, sandbox) {
  assert.deepEqual(await plugin.context("conductor"), []);
  await plugin.executeBefore({ tool: "subagent", input: { agent: "coder-oai" } });
  assert.deepEqual(plugin.syncedAgents(), {});
  assert.deepEqual(poisonHits(sandbox), []);
  assert.deepEqual(calls(sandbox), []);
}

describe("orch binary resolution", { skip }, () => {
  async function resolutionSandbox(t, present) {
    const { sandbox } = await goSandbox(t, { fakeOrchOnPath: present.includes("path") });
    const fakes = { option: path.join(sandbox.root, "opt", "orch"), env: path.join(sandbox.root, "envbin", "orch"), home: path.join(sandbox.home, ".local", "bin", "orch") };
    for (const self of present) if (fakes[self]) writeFakeOrch(fakes[self], self);
    return { sandbox, fakes };
  }
  const selves = (sandbox) => [...new Set(calls(sandbox).map((c) => c.self))];

  for (const [winner, present] of [["option", ["option", "env", "home", "path"]], ["env", ["env", "home", "path"]], ["home", ["home", "path"]], ["path", ["path"]]]) {
    test(`${winner} wins over the rest`, async (t) => {
      const { sandbox, fakes } = await resolutionSandbox(t, present);
      const env = { ORCH_RUNTIME: "go", ...(present.includes("env") ? { ORCH_BIN: fakes.env } : {}) };
      const options = present.includes("option") ? { binary: path.relative(process.cwd(), fakes.option) } : {};
      const plugin = await loadPlugin(t, sandbox, { options, env });
      const r = await plugin.callTool("usage_status");
      assert.deepEqual(selves(sandbox), [winner]);
      assert.equal(r.metadata.orch, fakes[winner] ?? path.join(sandbox.fakebin, "orch"));
    });
  }

  // Tools spawn orch in the project directory, so a relative binary must already be absolute there.
  test("a relative ORCH_BIN runs the absolute file it validated, from the project directory too", async (t) => {
    const { sandbox, fakes } = await resolutionSandbox(t, ["env", "path"]);
    setAnswers(sandbox, { ...sandbox.answers, "worker status": { stdout: "lane l1: running\n" } });
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go", ORCH_BIN: path.relative(process.cwd(), fakes.env) } });
    assert.equal((await plugin.callTool("usage_status")).metadata.orch, fakes.env);
    assert.equal((await plugin.callTool("worker_status")).content, "lane l1: running");
    assert.deepEqual(selves(sandbox), ["env"]);
  });

  test("a bare ORCH_BIN=orch is the plugin directory's orch, not a PATH lookup", async (t) => {
    const { sandbox, fakes } = await resolutionSandbox(t, ["env", "path"]);
    const pluginCwd = process.cwd();
    process.chdir(path.dirname(fakes.env));
    let plugin;
    try {
      plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go", ORCH_BIN: "orch" } });
    } finally {
      process.chdir(pluginCwd);
    }
    // process.cwd() reports the real path (macOS temp dirs live under the /var symlink).
    assert.equal((await plugin.callTool("usage_status")).metadata.orch, fs.realpathSync(fakes.env));
    assert.deepEqual(selves(sandbox), ["env"]);
  });

  test("an explicit ORCH_BIN that does not exist is an error, with no fall-through", async (t) => {
    const { sandbox } = await resolutionSandbox(t, ["home", "path"]);
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go", ORCH_BIN: "/nonexistent" }, brokenRuntime: true });
    await assert.rejects(plugin.callTool("usage_status"), { name: "OrchConfigError", message: "orch binary from ORCH_BIN is not an executable file: /nonexistent" });
    await assertDegraded(plugin, sandbox);
  });

  test("a non-executable option binary is an error naming it", async (t) => {
    const { sandbox } = await resolutionSandbox(t, ["path"]);
    const file = path.join(sandbox.root, "plain-orch");
    fs.writeFileSync(file, "", { mode: 0o644 });
    const plugin = await loadPlugin(t, sandbox, { options: { runtime: "go", binary: file }, brokenRuntime: true });
    await assert.rejects(plugin.callTool("usage_status"), { name: "OrchConfigError", message: `orch binary from option binary is not an executable file: ${file}` });
  });

  test("nothing found lists every candidate", async (t) => {
    const { sandbox } = await resolutionSandbox(t, []);
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go" }, brokenRuntime: true });
    const looked = [path.join(sandbox.home, ".local", "bin", "orch"), ...process.env.PATH.split(path.delimiter).map((d) => path.join(d, "orch"))];
    await assert.rejects(plugin.callTool("usage_status"), { name: "OrchConfigError", message: `orch binary not found (looked in: ${looked.join(", ")})` });
    await assertDegraded(plugin, sandbox);
  });
});

describe("usage_status", { skip }, () => {
  test("js: content and metadata equal today's getRouting()", async (t) => {
    const { sandbox, plugin } = await loadJs(t);
    const expected = await sandboxLibRouting(sandbox);
    const r = await plugin.callTool("usage_status", { refresh: false });
    assert.equal(r.content, expected.prompt);
    assert.deepEqual(r.metadata, { postures: expected.resolved.postures, roles: expected.resolved.roles, generated_at: expected.usage.generated_at, runtime: "js", orch: null });
  });

  for (const refresh of [false, true]) {
    test(`go: refresh ${refresh} runs routing --json${refresh ? " --refresh" : ""}`, async (t) => {
      const { sandbox, plugin, doc } = await loadGo(t);
      const before = argvOf(sandbox).length;
      const r = await plugin.callTool("usage_status", { refresh });
      assert.deepEqual(argvOf(sandbox).slice(before), [refresh ? "routing --json --refresh" : "routing --json"]);
      assert.equal(r.content, doc.prompt);
      assert.deepEqual(r.metadata, { postures: doc.resolved.postures, roles: doc.resolved.roles, generated_at: doc.usage.generated_at, runtime: "go", orch: path.join(sandbox.fakebin, "orch") });
      assert.equal(calls(sandbox).at(-1).cwd, fs.realpathSync(process.cwd()));
      assert.deepEqual(poisonHits(sandbox), []);
    });
  }

  test("go: a failing orch is an OrchCommandError with the last stderr line", async (t) => {
    const { plugin, sandbox } = await loadGo(t, { answers: { "routing --json --refresh": { code: 1, stderr: "collecting\norch routing: no network\n\n" } } });
    await assert.rejects(plugin.callTool("usage_status", { refresh: true }), { name: "OrchCommandError", subcommand: "routing", code: 1, message: "orch routing exited 1: orch routing: no network" });
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("go: an orch that cannot be spawned is an OrchCommandError, not a host crash", async (t) => {
    const { plugin, sandbox } = await loadGo(t);
    fs.rmSync(path.join(sandbox.fakebin, "orch"));
    await assert.rejects(plugin.callTool("usage_status"), { name: "OrchCommandError", code: -1, message: /^orch routing exited -1: spawn .*orch ENOENT$/ });
  });
});

describe("delegate on the native path", { skip }, () => {
  const input = { role: "coder", size: "medium", prompt: "do it" };
  const assertNative = (r) => {
    assert.match(r.content, /^Role coder \(size medium\) is best served natively right now: call the subagent "coder-oai"/);
    assert.equal(r.metadata.native_agent, "coder-oai");
  };

  test("js", async (t) => {
    const { sandbox, plugin } = await loadJs(t, { cache: openaiHeadroom() });
    assertNative(await plugin.callTool("delegate", input));
    assert.deepEqual(calls(sandbox), []);
  });

  test("go: effort comes from orch effort, nothing else is spawned", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { cache: openaiHeadroom(), answers: { "effort --role coder --size medium": { stdout: "null\n" } } });
    const before = argvOf(sandbox).length;
    assertNative(await plugin.callTool("delegate", input));
    assert.deepEqual(argvOf(sandbox).slice(before), ["routing --json", "effort --role coder --size medium"]);
    assert.deepEqual(poisonHits(sandbox), []);
  });
});

describe("codex_review budget gate", { skip }, () => {
  const refused = (r) => assert.match(r.content, /^Refused: openai budget is red; refusing to start/);
  // Only `codex review`: the post-review refreshSoon may also start `codex app-server` in JS mode.
  const codexRuns = (sandbox) => calls(sandbox).filter((c) => c.self === "codex" && c.argv[0] === "review").length;

  test("js: red refuses without spawning codex; force runs it", async (t) => {
    const { sandbox, plugin } = await loadJs(t, { cache: "red" });
    refused(await plugin.callTool("codex_review", {}));
    assert.equal(codexRuns(sandbox), 0);
    assert.equal((await plugin.callTool("codex_review", { force: true })).content, "review ok");
    assert.equal(codexRuns(sandbox), 1);
  });

  test("go: red refuses without spawning codex; force passes --force", async (t) => {
    const red = { allowed: false, level: "red", resets_at: "2099-01-01T05:00:00.000Z", note: "openai budget is red; refusing to start (pass --force to override). Resets 2099-01-01T05:00:00.000Z." };
    const { sandbox, plugin } = await loadGo(t, { cache: "red", answers: { "budget gate --vendor openai": { stdout: red }, "budget gate --vendor openai --force": { stdout: { allowed: true, level: "red" } } } });
    refused(await plugin.callTool("codex_review", {}));
    assert.equal(codexRuns(sandbox), 0);
    assert.equal((await plugin.callTool("codex_review", { force: true })).content, "review ok");
    assert.deepEqual(argvOf(sandbox).filter((a) => a.startsWith("budget")), ["budget gate --vendor openai", "budget gate --vendor openai --force"]);
    assert.equal(codexRuns(sandbox), 1);
    assert.deepEqual(poisonHits(sandbox), []);
  });
});

describe("context hook", { skip }, () => {
  const budgetBlock = (system, usage) => {
    assert.equal(system.length, 1);
    assert.ok(system[0].text.startsWith(`<orchestrator_budget updated="${usage.generated_at}">\n`), system[0].text.slice(0, 80));
  };

  test("js: a fresh cache pushes one block and does not refresh", async (t) => {
    const { sandbox, plugin } = await loadJs(t);
    const before = fs.readFileSync(path.join(sandbox.home, ".local", "state", "opencode", "orchestrator", "usage.json"), "utf8");
    const system = await plugin.context("conductor");
    budgetBlock(system, JSON.parse(before));
    assert.ok(system[0].text.includes((await sandboxLibRouting(sandbox)).prompt));
    await new Promise((r) => setTimeout(r, 100));
    assert.equal(fs.readFileSync(path.join(sandbox.home, ".local", "state", "opencode", "orchestrator", "usage.json"), "utf8"), before);
  });

  test("go: a fresh cache pushes one block and starts no refresh", async (t) => {
    const { sandbox, plugin, doc } = await loadGo(t);
    const system = await plugin.context("conductor");
    budgetBlock(system, doc.usage);
    assert.ok(system[0].text.includes(doc.prompt));
    await new Promise((r) => setTimeout(r, 200));
    assert.deepEqual(argvOf(sandbox), ["routing --json", "routing --cached --json"]);
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("go: a stale cache triggers routing --json --refresh", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { cacheAgeMs: 10 * 60 * 1000 });
    assert.equal((await plugin.context("conductor")).length, 1);
    assert.ok(await waitFor(() => argvOf(sandbox).includes("routing --json --refresh")));
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("go: no cache (exit 3) refreshes and pushes nothing", async (t) => {
    const { sandbox, plugin } = await loadGo(t, { answers: { "routing --json": { code: 1, stderr: "orch routing: unavailable\n" }, "routing --json --refresh": { code: 1, stderr: "orch routing: unavailable\n" }, "routing --cached --json": { code: 3, stderr: "orch routing: no cache\n" } } });
    assert.deepEqual(await plugin.context("conductor"), []);
    assert.ok(await waitFor(() => argvOf(sandbox).includes("routing --json --refresh")));
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("go: a non-primary agent spawns nothing", async (t) => {
    const { sandbox, plugin } = await loadGo(t);
    const before = calls(sandbox).length;
    assert.deepEqual(await plugin.context("title"), []);
    assert.equal(calls(sandbox).length, before);
  });
});

describe("execute.before gate for native OpenAI subagents", { skip }, () => {
  const gate = { tool: "subagent", input: { agent: "coder-oai" } };
  const redError = /^\[orchestrator\] OpenAI budget is red \(5h .*Native subagent "coder-oai" refused/;

  test("js: red throws, green passes", async (t) => {
    await assert.rejects((await loadJs(t, { cache: "red" })).plugin.executeBefore(gate), { message: redError });
    await (await loadJs(t)).plugin.executeBefore(gate);
  });

  test("go: red throws, green passes", async (t) => {
    const red = await loadGo(t, { cache: "red" });
    await assert.rejects(red.plugin.executeBefore(gate), { message: redError });
    const green = await loadGo(t);
    await green.plugin.executeBefore(gate);
    assert.deepEqual([...poisonHits(red.sandbox), ...poisonHits(green.sandbox)], []);
  });
});

describe("model sync from the registry", { skip }, () => {
  const strong = checkoutRegistry().vendors.openai.tiers.strong;
  const [providerID, id] = String(strong.opencode).split("/");

  test("js: coder-oai follows the registry", async (t) => {
    const { plugin } = await loadJs(t);
    assert.deepEqual(plugin.syncedAgents()["coder-oai"], { model: { providerID, id, ...(strong.effort ? { variant: strong.effort } : {}) } });
  });

  test("go: ORCH_REGISTRY is the registry read", async (t) => {
    const { sandbox } = await goSandbox(t);
    const registry = checkoutRegistry();
    registry.vendors.openai.tiers.strong = { ...registry.vendors.openai.tiers.strong, opencode: "openai/gpt-test-strong", effort: "low" };
    const file = path.join(sandbox.root, "models-modified.json");
    fs.writeFileSync(file, JSON.stringify(registry));
    const plugin = await loadPlugin(t, sandbox, { env: { ORCH_RUNTIME: "go", ORCH_REGISTRY: file } });
    assert.deepEqual(plugin.syncedAgents()["coder-oai"], { model: { providerID: "openai", id: "gpt-test-strong", variant: "low" } });
    assert.deepEqual(poisonHits(sandbox), []);
  });

  test("go: relative ORCH_HOME and ORCH_REGISTRY are made absolute for every child and the registry read", async (t) => {
    const { sandbox } = await goSandbox(t, { answers: { "worker status": { stdout: "\n" } } });
    const registry = checkoutRegistry();
    registry.vendors.openai.tiers.strong = { ...registry.vendors.openai.tiers.strong, opencode: "openai/gpt-test-relative", effort: "low" };
    const file = path.join(sandbox.root, "models-relative.json");
    fs.writeFileSync(file, JSON.stringify(registry));
    const env = { ORCH_RUNTIME: "go", ORCH_HOME: path.relative(process.cwd(), sandbox.orchestrator), ORCH_REGISTRY: path.relative(process.cwd(), file) };
    const plugin = await loadPlugin(t, sandbox, { env });
    await plugin.callTool("worker_status");
    assert.deepEqual(plugin.syncedAgents()["coder-oai"], { model: { providerID: "openai", id: "gpt-test-relative", variant: "low" } });
    const seen = calls(sandbox).filter((c) => c.self === "path").map((c) => [c.argv[0], c.cwd === sandbox.project || c.cwd === fs.realpathSync(sandbox.project), c.orchHome, c.orchRegistry]);
    assert.deepEqual(seen, [["routing", false, sandbox.orchestrator, file], ["worker", true, sandbox.orchestrator, file]]);
  });

  test("go: the default registry is the orchestrator tree's models.json", async (t) => {
    const { plugin, sandbox } = await loadGo(t);
    assert.deepEqual(plugin.syncedAgents()["coder-oai"], { model: { providerID, id, ...(strong.effort ? { variant: strong.effort } : {}) } });
    assert.deepEqual(poisonHits(sandbox), []);
  });
});
