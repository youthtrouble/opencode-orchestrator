// Drives plugins/orchestrator.ts through a fake opencode context in a throwaway HOME. Both
// runtimes run against fakes only: go/parity/fakes sits on PATH (failing security, claude,
// codex, opencode), and the fake `orch` answers from a scripted map, so no keychain, endpoint or
// model is reachable. Not a *.test.mjs file: the test glob must not run it on its own.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO = fileURLToPath(new URL("../", import.meta.url));
const PLUGIN_URL = pathToFileURL(path.join(REPO, "plugins", "orchestrator.ts")).href;
const PARITY_FAKES = path.join(REPO, "go", "parity", "fakes");
const CACHES = JSON.parse(fs.readFileSync(path.join(REPO, "go", "testdata", "usage", "parity-caches.json"), "utf8"));
const POISONED_IN_GO = ["usage.mjs", "routing.mjs", "budget.mjs"];
const ENV_KEYS = ["HOME", "PATH", "ORCH_RUNTIME", "ORCH_BIN", "ORCH_HOME", "ORCH_REGISTRY", "PLUGIN_TEST_LOG", "PLUGIN_TEST_ANSWERS", "PLUGIN_TEST_POISON"];

export const typescriptSupported = Boolean(process.features.typescript);

/** A usage cache document, generated `ageMs` ago, from a parity fixture name or a {claude, codex} pair. */
export function usageFixture(fixture, { ageMs = 0 } = {}) {
  const generatedAt = Date.now() - ageMs;
  const soon = new Date(Date.now() + 10 * 60 * 1000).toISOString();
  const body = JSON.stringify(typeof fixture === "string" ? CACHES[fixture] : fixture).replaceAll('"<soon>"', JSON.stringify(soon));
  return { generated_at: new Date(generatedAt).toISOString(), ...JSON.parse(body) };
}

/** The `orch routing --json` document the real binary would print for this usage. */
export async function routingDocument(usage, { channels = { anthropic: false, openai: true }, registry = checkoutRegistry() } = {}) {
  const { resolveRoles, summarizeForPrompt } = await import(pathToFileURL(path.join(REPO, "orchestrator", "lib", "routing.mjs")).href);
  const resolved = resolveRoles(usage, registry, { channels });
  return { usage, registry, channels, resolved, prompt: summarizeForPrompt(resolved, usage) };
}

/** Claude red, Codex green: the OpenAI-native route for most roles. */
export function openaiHeadroom() {
  return { claude: CACHES.red.claude, codex: CACHES.green.codex };
}

export function checkoutRegistry() {
  return JSON.parse(fs.readFileSync(path.join(REPO, "orchestrator", "models.json"), "utf8"));
}

/**
 * A Node script that logs every call and answers like `orch` from $PLUGIN_TEST_ANSWERS: by the
 * full argv, then by the tokens before the first flag; unscripted calls exit 97. A `fixedAnswer`
 * replaces the lookup (the fake `codex`).
 */
export function writeFakeOrch(file, self, fixedAnswer = null) {
  const lookup = fixedAnswer ? JSON.stringify(fixedAnswer) : "answers[full] ?? answers[prefix]";
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, `#!${process.execPath}
const fs = require("node:fs");
const argv = process.argv.slice(2);
let stdin = "";
try { stdin = fs.readFileSync(0, "utf8"); } catch { /* no stdin */ }
fs.appendFileSync(process.env.PLUGIN_TEST_LOG, JSON.stringify({ self: ${JSON.stringify(self)}, argv, cwd: process.cwd(), stdin }) + "\\n");
const answers = JSON.parse(fs.readFileSync(process.env.PLUGIN_TEST_ANSWERS, "utf8"));
const firstFlag = argv.findIndex((a) => a.startsWith("--"));
const full = argv.join(" ");
const prefix = (firstFlag === -1 ? argv : argv.slice(0, firstFlag)).join(" ");
const answer = ${lookup};
if (!answer) { process.stderr.write("fake orch: unscripted " + full + "\\n"); process.exit(97); }
process.stdout.write(answer.stdout ?? "");
process.stderr.write(answer.stderr ?? "");
process.exit(answer.code ?? 0);
`, { mode: 0o755 });
}

/**
 * A temp root with home/ (orchestrator tree, usage cache, opencode auth), project/ and fakebin/.
 * In go sandboxes lib/{usage,routing,budget}.mjs are poison: importing one logs it and throws.
 */
export function createSandbox(t, { runtime, cache = "green", cacheAgeMs = 0, fakeOrchOnPath = runtime === "go" }) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "plugin-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const home = path.join(root, "home");
  const orchestrator = path.join(home, ".config", "opencode", "orchestrator");
  const sandbox = {
    runtime,
    root,
    home,
    orchestrator,
    project: path.join(root, "project"),
    fakebin: path.join(root, "fakebin"),
    log: path.join(root, "calls.jsonl"),
    poison: path.join(root, "poison.log"),
    answersFile: path.join(root, "answers.json"),
    answers: {},
  };
  fs.mkdirSync(sandbox.project, { recursive: true });
  fs.cpSync(path.join(REPO, "orchestrator", "lib"), path.join(orchestrator, "lib"), { recursive: true });
  fs.copyFileSync(path.join(REPO, "orchestrator", "models.json"), path.join(orchestrator, "models.json"));
  if (runtime === "go") for (const name of POISONED_IN_GO) writePoisonLib(path.join(orchestrator, "lib", name), name);
  if (cache) writeUsageCache(sandbox, usageFixture(cache, { ageMs: cacheAgeMs }));
  fs.mkdirSync(path.join(home, ".local", "share", "opencode"), { recursive: true });
  fs.writeFileSync(path.join(home, ".local", "share", "opencode", "auth.json"), JSON.stringify({ openai: { type: "oauth" } }));
  writeFakeOrch(path.join(sandbox.fakebin, "codex"), "codex", { stdout: "review ok\n" });
  if (fakeOrchOnPath) writeFakeOrch(path.join(sandbox.fakebin, "orch"), "path");
  fs.writeFileSync(sandbox.log, "");
  setAnswers(sandbox, {});
  return sandbox;
}

function writeUsageCache(sandbox, usage) {
  const dir = path.join(sandbox.home, ".local", "state", "opencode", "orchestrator");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, "usage.json"), JSON.stringify(usage, null, 2));
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

export function poisonHits(sandbox) {
  return fs.existsSync(sandbox.poison) ? fs.readFileSync(sandbox.poison, "utf8").split("\n").filter(Boolean) : [];
}

function writePoisonLib(file, name) {
  fs.writeFileSync(file, `import fs from "node:fs";\nfs.appendFileSync(process.env.PLUGIN_TEST_POISON, ${JSON.stringify(name)} + "\\n");\nthrow new Error("poison lib ${name} imported");\n`);
}

let caseNumber = 0;

/**
 * Load a fresh copy of the plugin into the sandbox environment and run setup with a fake ctx.
 * The environment stays in place until the test ends, because tools and hooks read it lazily.
 */
export async function loadPlugin(t, sandbox, { options = {}, env = {} } = {}) {
  const saved = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]));
  t.after(() => { for (const [k, v] of Object.entries(saved)) if (v === undefined) delete process.env[k]; else process.env[k] = v; });
  for (const k of ["ORCH_RUNTIME", "ORCH_BIN", "ORCH_HOME", "ORCH_REGISTRY"]) delete process.env[k];
  Object.assign(process.env, {
    HOME: sandbox.home,
    PATH: [sandbox.fakebin, PARITY_FAKES, path.dirname(process.execPath)].join(path.delimiter),
    PLUGIN_TEST_LOG: sandbox.log,
    PLUGIN_TEST_ANSWERS: sandbox.answersFile,
    PLUGIN_TEST_POISON: sandbox.poison,
    ...env,
  });

  const plugin = await import(`${PLUGIN_URL}?case=${++caseNumber}`);
  const ctx = fakeContext(sandbox, options);
  await plugin.default.setup(ctx.api);
  await settleWarmUp(sandbox);
  return ctx;
}

function fakeContext(sandbox, options) {
  const tools = {}, toolHooks = {}, sessionHooks = {}, agentTransforms = [];
  const api = {
    options,
    location: { directory: sandbox.project },
    tool: {
      transform: async (fn) => fn({ namespace() {}, add(t) { tools[t.name] = t; } }),
      hook: async (name, fn) => { toolHooks[name] = fn; },
    },
    session: { hook: async (name, fn) => { sessionHooks[name] = fn; } },
    agent: { transform: async (fn) => { agentTransforms.push(fn); } },
  };
  const callContext = () => ({ directory: sandbox.project, signal: new AbortController().signal, progress: async () => {}, agent: "conductor", sessionID: "ses_test" });
  return {
    api,
    tools,
    callTool: (name, input = {}) => tools[name].execute(input, callContext()),
    async context(agent) {
      const e = { agent, system: [] };
      await sessionHooks.context(e);
      return e.system;
    },
    executeBefore: (e) => toolHooks["execute.before"](e),
    /** Apply the model-sync transforms and return the updated agents by id. */
    syncedAgents() {
      const agents = {};
      for (const fn of agentTransforms) fn({ update(id, mutate) { agents[id] ??= {}; mutate(agents[id]); } });
      return agents;
    },
  };
}

// setup starts an un-awaited warm-up routing(false). Let it settle so it cannot race a case.
async function settleWarmUp(sandbox) {
  if (sandbox.runtime === "js") return new Promise((r) => setTimeout(r, 50));
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (calls(sandbox).some((c) => c.self !== "codex" && c.argv.join(" ") === "routing --json")) break;
    await new Promise((r) => setTimeout(r, 20));
  }
  await new Promise((r) => setTimeout(r, 50));
}

export async function waitFor(predicate, ms = 2000) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await new Promise((r) => setTimeout(r, 20));
  }
  return predicate();
}
