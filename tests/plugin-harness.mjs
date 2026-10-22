// Drives plugins/orchestrator.ts through a fake opencode context in a throwaway HOME. Everything
// runs against fakes only: go/parity/fakes sits on PATH (failing security, claude, codex,
// opencode), and the fake `orch` answers from a scripted map, so no keychain, endpoint or model is
// reachable. Not a *.test.mjs file: the test glob must not run it on its own.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// Poll real process I/O even when a test virtualizes the plugin's timers and wall clock.
const realSetTimeout = globalThis.setTimeout;

const REPO = fileURLToPath(new URL("../", import.meta.url));
const PLUGIN_URL = pathToFileURL(path.join(REPO, "plugins", "orchestrator.ts")).href;
const PARITY_FAKES = path.join(REPO, "go", "parity", "fakes");
const ROUTING_FIXTURES = path.join(REPO, "tests", "fixtures", "routing");
/** The orch subcommands the plugin spawns as processes of their own (bridges, worker, supervisor, flip). */
export const SPAWNED_SCRIPTS = ["claude-exec", "codex-exec", "worker", "supervisor", "flip"];
const ENV_KEYS = ["HOME", "PATH", "ORCH_RUNTIME", "ORCH_BIN", "ORCH_HOME", "ORCH_REGISTRY", "PLUGIN_TEST_LOG", "PLUGIN_TEST_ANSWERS", "ORCH_PLUGIN_KILL_GRACE_MS", "ORCH_PLUGIN_PIPE_GRACE_MS", "ORCH_PLUGIN_ORCH_TIMEOUT_SCALE", "ORCH_PLUGIN_BEAT_INTERVAL_MS", "PLUGIN_TEST_HANG_CEILING_MS"];

export const typescriptSupported = Boolean(process.features.typescript);

/**
 * The `orch routing --json` document for a usage scenario, read from tests/fixtures/routing/<name>.json
 * (green, green-stale, red, openai-headroom). Every file is a {usage, registry, channels, resolved,
 * prompt} document with channels {anthropic: false, openai: true} and orchestrator/models.json as
 * the registry. They were generated once with this harness's routingDocument (resolveRoles and
 * summarizeForPrompt from lib/routing.mjs in the orchestrator/ tree of fb2b761, before the JS was deleted),
 * over the go/testdata/usage/parity-caches.json caches: green and red are those entries,
 * green-stale is green aged 10 minutes with `stale: true`, and openai-headroom pairs red's claude
 * with green's codex. Regenerating them later needs `orch routing --cached --json` on a host whose
 * detected channels are {anthropic: false, openai: true} (routing has no channel flag), then a manual
 * check of the diff: the output is not expected to be byte-identical (timestamps and `stale` come
 * from the cache). Timestamps are frozen at generation time, which is harmless: the fake orch
 * replays the document and the plugin recomputes nothing from it.
 */
export function routingFixture(name) {
  return JSON.parse(fs.readFileSync(path.join(ROUTING_FIXTURES, `${name}.json`), "utf8"));
}

export function checkoutRegistry() {
  return JSON.parse(fs.readFileSync(path.join(REPO, "orchestrator", "models.json"), "utf8"));
}

/**
 * A Node script that logs every call and answers like `orch` from $PLUGIN_TEST_ANSWERS: by the
 * full argv, then by the tokens before the first flag; unscripted calls exit 97. A `fixedAnswer`
 * replaces the lookup (the fake `codex`). It uses getBuiltinModule, so it runs whatever module
 * type Node picks for it.
 * Besides {stdout, stderr, code}, an answer can misbehave for the process-runner tests:
 * `waitForFile` gates the response on a test-owned file; `ignoreTerm` traps SIGTERM,
 * `hang` never answers, `holdStdout` leaves a logged `sleep 30` holding
 * stdout after the exit, and `stdoutHex` writes its hex chunks 100 ms apart instead of `stdout`.
 * Every fake process ends by itself, so a test never has to signal a pid from the log: the holder
 * after 30 s, and a hung call after $PLUGIN_TEST_HANG_CEILING_MS (default 60 s), with exit code 98.
 * Each logged call also carries the ORCH_HOME and ORCH_REGISTRY it saw (orchHome, orchRegistry).
 */
export function writeFakeOrch(file, self, fixedAnswer = null) {
  const lookup = fixedAnswer ? JSON.stringify(fixedAnswer) : "answers[full] ?? answers[prefix]";
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, `#!${process.execPath}
const fs = process.getBuiltinModule("node:fs");
const argv = process.argv.slice(2);
const log = (entry) => fs.appendFileSync(process.env.PLUGIN_TEST_LOG, JSON.stringify(entry) + "\\n");
let stdin = "";
try { stdin = fs.readFileSync(0, "utf8"); } catch { /* no stdin */ }
const answers = JSON.parse(fs.readFileSync(process.env.PLUGIN_TEST_ANSWERS, "utf8"));
const firstFlag = argv.findIndex((a) => a.startsWith("--"));
const full = argv.join(" ");
const prefix = (firstFlag === -1 ? argv : argv.slice(0, firstFlag)).join(" ");
const answer = ${lookup};
// Trapped before the call is logged, so a test that signals a logged call always meets the trap.
if (answer?.ignoreTerm) process.on("SIGTERM", () => {});
log({ self: ${JSON.stringify(self)}, argv, cwd: process.cwd(), stdin, pid: process.pid, orchHome: process.env.ORCH_HOME, orchRegistry: process.env.ORCH_REGISTRY });
if (!answer) { process.stderr.write("fake orch: unscripted " + full + "\\n"); process.exit(97); }
if (answer.waitForFile) {
  const ceiling = setTimeout(() => process.exit(98), Number(process.env.PLUGIN_TEST_HANG_CEILING_MS) || 60000);
  const gate = setInterval(() => {
    if (!fs.existsSync(answer.waitForFile)) return;
    clearTimeout(ceiling); clearInterval(gate); respond();
  }, 10);
} else respond();
function respond() {
  if (answer.hang) setTimeout(() => { process.stderr.write("fake orch: hang ceiling reached\\n"); process.exit(98); }, Number(process.env.PLUGIN_TEST_HANG_CEILING_MS) || 60000);
  else if (answer.stdoutHex) writeSlowly(answer.stdoutHex.map((hex) => Buffer.from(hex, "hex")));
  else {
    if (answer.holdStdout) {
      const holder = process.getBuiltinModule("node:child_process").spawn("/bin/sleep", ["30"], { stdio: ["ignore", "inherit", "ignore"] });
      log({ self: "holder", argv: ["sleep", "30"], cwd: process.cwd(), stdin: "", pid: holder.pid });
    }
    process.stdout.write(answer.stdout ?? "");
    process.stderr.write(answer.stderr ?? "");
    process.exit(answer.code ?? 0);
  }
}
function writeSlowly(chunks) {
  if (!chunks.length) process.exit(answer.code ?? 0);
  fs.writeSync(1, chunks[0]);
  setTimeout(() => writeSlowly(chunks.slice(1)), 100);
}
`, { mode: 0o755 });
}

/**
 * A temp root with home/ (holding only the orchestrator tree's models.json), project/ and fakebin/
 * (a logging fake `codex`, and a fake `orch` unless `fakeOrchOnPath` is false).
 */
export function createSandbox(t, { fakeOrchOnPath = true } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "plugin-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const home = path.join(root, "home");
  const orchestrator = path.join(home, ".config", "opencode", "orchestrator");
  const sandbox = {
    root,
    home,
    orchestrator,
    project: path.join(root, "project"),
    fakebin: path.join(root, "fakebin"),
    log: path.join(root, "calls.jsonl"),
    answersFile: path.join(root, "answers.json"),
    answers: {},
  };
  fs.mkdirSync(sandbox.project, { recursive: true });
  fs.mkdirSync(orchestrator, { recursive: true });
  fs.copyFileSync(path.join(REPO, "orchestrator", "models.json"), path.join(orchestrator, "models.json"));
  writeFakeOrch(path.join(sandbox.fakebin, "codex"), "codex", { stdout: "review ok\n" });
  if (fakeOrchOnPath) writeFakeOrch(path.join(sandbox.fakebin, "orch"), "path");
  fs.writeFileSync(sandbox.log, "");
  setAnswers(sandbox, {});
  return sandbox;
}

/** Replace the fake orch's answer map: argv key -> {stdout, stderr, code} (stdout objects are JSON-encoded). */
export function setAnswers(sandbox, answers) {
  sandbox.answers = answers;
  const encoded = Object.fromEntries(Object.entries(answers).map(([key, a]) => [key, { ...a, stdout: typeof a.stdout === "string" || a.stdout === undefined ? a.stdout : JSON.stringify(a.stdout, null, 2) }]));
  fs.writeFileSync(sandbox.answersFile, JSON.stringify(encoded));
}

export function calls(sandbox) {
  return fs.readFileSync(sandbox.log, "utf8").split("\n").filter(Boolean).map((l) => JSON.parse(l));
}

let caseNumber = 0;

/**
 * Load a fresh copy of the plugin into the sandbox environment and run setup with a fake ctx.
 * The environment stays in place until the test ends, because tools and hooks read it lazily.
 * Pass `brokenRuntime` when the configuration cannot resolve a runtime: setup then makes no
 * warm-up call, so there is none to wait for. The claim is checked (see requireBrokenRuntime).
 */
export async function loadPlugin(t, sandbox, { options = {}, env = {}, brokenRuntime = false } = {}) {
  const saved = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]));
  t.after(() => { for (const [k, v] of Object.entries(saved)) if (v === undefined) delete process.env[k]; else process.env[k] = v; });
  for (const k of ["ORCH_RUNTIME", "ORCH_BIN", "ORCH_HOME", "ORCH_REGISTRY"]) delete process.env[k];
  Object.assign(process.env, {
    HOME: sandbox.home,
    PATH: [sandbox.fakebin, PARITY_FAKES, path.dirname(process.execPath)].join(path.delimiter),
    PLUGIN_TEST_LOG: sandbox.log,
    PLUGIN_TEST_ANSWERS: sandbox.answersFile,
    ...env,
  });

  const plugin = await import(`${PLUGIN_URL}?case=${++caseNumber}`);
  const ctx = fakeContext(sandbox, options);
  ctx.dispose = await plugin.default.setup(ctx.api);
  t.after(() => ctx.dispose());
  if (brokenRuntime) await requireBrokenRuntime(ctx);
  else await settleWarmUp(sandbox);
  return ctx;
}

// A configuration wrongly claimed broken would skip settleWarmUp and let the warm-up call race
// the case, so the claim is probed. The probe cannot disturb the calls a case counts: a broken
// runtime never spawns orch, since every tool fails with OrchConfigError while resolving it, and
// a runtime that does resolve fails the load here, before the case runs.
async function requireBrokenRuntime(ctx) {
  const probe = await ctx.toolResult("usage_status");
  if (probe.status !== "error" || probe.error.name !== "OrchConfigError") {
    throw new Error(
      "loadPlugin was passed brokenRuntime, but the plugin resolved a runtime, " +
        "whose warm-up call would race the case",
    );
  }
}

function fakeContext(sandbox, options) {
  const tools = {}, toolHooks = {}, sessionHooks = {}, agentTransforms = [], subscriptions = [];
  const api = {
    options,
    location: { directory: sandbox.project },
    tool: {
      transform: async (fn) => fn({ namespace() {}, add(t) { tools[t.name] = t; } }),
      hook: async (name, fn) => { toolHooks[name] = fn; },
    },
    session: { hook: async (name, fn) => { sessionHooks[name] = fn; } },
    agent: { transform: async (fn) => { agentTransforms.push(fn); } },
    event: { subscribe: (options) => subscribeEvents(options, subscriptions) },
  };
  const callContext = (signal = new AbortController().signal) => ({ directory: sandbox.project, signal, progress: async () => {}, agent: "conductor", sessionID: "ses_test" });
  return {
    api,
    tools,
    callTool: (name, input = {}, { signal } = {}) => tools[name].execute(input, callContext(signal)),
    /** Run a tool the way opencode does: a throw becomes an error result. */
    async toolResult(name, input = {}) {
      try { return { status: "ok", ...(await tools[name].execute(input, callContext())) }; } catch (error) { return { status: "error", error }; }
    },
    /** Run the context hook; a `sessionID` names the session, as opencode's payload does. */
    async context(agent, sessionID) {
      const e = { agent, system: [], ...(sessionID === undefined ? {} : { sessionID }) };
      await sessionHooks.context(e);
      return e.system;
    },
    executeBefore: (e) => toolHooks["execute.before"](e),
    executeAfter: (e) => toolHooks["execute.after"](e),
    retry: (e) => sessionHooks.retry(e),
    /** Every event.subscribe call: its options and whether its iteration has ended. */
    subscriptions,
    /** Deliver a bus event ({type, data}) to every open subscription. */
    emit: (event) => { for (const sub of subscriptions) sub.push(event); },
    /** Apply the model-sync transforms and return the updated agents by id. */
    syncedAgents() {
      const agents = {};
      for (const fn of agentTransforms) fn({ update(id, mutate) { agents[id] ??= {}; mutate(agents[id]); } });
      return agents;
    },
  };
}

// opencode 2.0.32's promise API: event.subscribe({signal}) returns an endless async iterable of bus
// events that ends when the signal aborts. `failWith` makes the next read throw instead.
function subscribeEvents(options, subscriptions) {
  const queued = [];
  let wake = null;
  const sub = {
    options,
    ended: false,
    failWith: null,
    push(event) { queued.push(event); wake?.(); },
    fail(error) { sub.failWith = error; wake?.(); },
  };
  options?.signal?.addEventListener("abort", () => wake?.());
  subscriptions.push(sub);
  return {
    async *[Symbol.asyncIterator]() {
      try {
        while (!options?.signal?.aborted) {
          if (sub.failWith) throw sub.failWith;
          if (queued.length) { yield queued.shift(); continue; }
          await new Promise((resolve) => { wake = resolve; });
          wake = null;
        }
      } finally {
        sub.ended = true;
      }
    },
  };
}

// The fake orch is a Node process; under host contention even startup has exceeded 5 s.
// This bounds fixture I/O, not plugin scheduling (which tests can advance virtually).
const PROCESS_IO_BOUND_MS = 15000;

// A log entry means the fake started, not that its response completed. Wait for its exit
// before tests replace answers or install virtual timers; no guessed settling sleep.
async function settleWarmUp(sandbox) {
  const warmedUp = () => {
    const call = calls(sandbox).find((c) => c.self !== "codex" && c.argv.join(" ") === "routing --json");
    if (!call) return false;
    try { process.kill(call.pid, 0); return false; } catch (error) {
      if (error.code === "ESRCH") return true;
      throw error;
    }
  };
  if (!(await waitFor(warmedUp))) throw new Error(`the plugin's warm-up \`orch routing --json\` did not finish within ${PROCESS_IO_BOUND_MS} ms (pass brokenRuntime if setup cannot resolve a runtime)`);
}

export async function waitFor(predicate, ms = PROCESS_IO_BOUND_MS) {
  const deadline = performance.now() + ms;
  while (performance.now() < deadline) {
    if (predicate()) return true;
    await new Promise((r) => realSetTimeout(r, 20));
  }
  return predicate();
}
