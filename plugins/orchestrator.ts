// Orchestrator plugin for opencode v2 (promise API). No npm deps: Plugin.define is the identity,
// so a plain { id, setup } default export is what the loader needs.
//
// What it does:
//   1. Registers "orch" tools: usage_status, delegate, codex_review, mailbox_post, mailbox_list,
//      manifest_create, manifest_verify, worker_start, worker_status, supervisor.
//   2. Injects the live subscription budget + role routing into the system prompt of primary agents.
//   3. Syncs native agents' models from orchestrator/models.json so the audit skill can retarget
//      everything by editing one file.
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

const HOME = os.homedir();
const ORCH = path.join(HOME, ".config", "opencode", "orchestrator");
const BIN = path.join(ORCH, "bin");
const NODE = process.execPath.includes("opencode") ? "node" : process.execPath;

type Json = Record<string, unknown>;

async function lib(name: string) {
  return import(pathToFileURL(path.join(ORCH, "lib", name)).href);
}

// Bridges (orch claude-exec/codex-exec) bound their own CLI within 10 s of a signal and then write
// their run record, so a child that ignores SIGTERM is SIGKILLed only after KILL_GRACE_MS. Once the
// child has exited, a descendant may still hold its pipes: stop waiting for them after PIPE_GRACE_MS.
// Only the tests set these variables, to shorten the graces.
const KILL_GRACE_MS = Number(process.env.ORCH_PLUGIN_KILL_GRACE_MS) || 15 * 1000;
const PIPE_GRACE_MS = Number(process.env.ORCH_PLUGIN_PIPE_GRACE_MS) || 10 * 1000;

// A spawn failure (e.g. ENOENT) resolves with code -1 and the error text in stderr instead of
// raising an uncaught "error" event in the opencode host. A call cancelled before it starts spawns nothing.
type RunResult = { code: number; stdout: string; stderr: string; timedOut: boolean };
function run(cmd: string, args: string[], { cwd, input, signal, timeoutMs = 2 * 60 * 60 * 1000 }: { cwd: string; input?: string; signal?: AbortSignal; timeoutMs?: number }) {
  if (signal?.aborted) return Promise.resolve<RunResult>({ code: -1, stdout: "", stderr: "aborted before start", timedOut: false });
  return new Promise<RunResult>((resolve) => {
    const child = spawn(cmd, args, { cwd, stdio: ["pipe", "pipe", "pipe"], env: { ...process.env, CLAUDECODE: undefined } });
    // Decoding as a stream keeps a multi-byte character split across chunks intact.
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    let stdout = "", stderr = "", settled = false, timedOut = false, exited = false;
    let killTimer: ReturnType<typeof setTimeout> | undefined, pipeTimer: ReturnType<typeof setTimeout> | undefined;
    const sendSignal = (sig: NodeJS.Signals) => {
      // Bun may throw when the child has just exited; the process is gone, so there is nothing to do.
      try { child.kill(sig); } catch { /* gone */ }
    };
    const terminate = () => {
      if (exited || killTimer) return;
      sendSignal("SIGTERM");
      killTimer = setTimeout(() => sendSignal("SIGKILL"), KILL_GRACE_MS);
    };
    const timer = setTimeout(() => { timedOut = true; terminate(); }, timeoutMs);
    const finish = (result: RunResult) => {
      if (settled) return;
      settled = true;
      clearTimeout(killTimer); clearTimeout(pipeTimer);
      signal?.removeEventListener("abort", terminate);
      resolve(result);
    };
    child.stdout.on("data", (c: string) => (stdout += c));
    child.stderr.on("data", (c: string) => (stderr += c));
    signal?.addEventListener("abort", terminate, { once: true });
    child.on("error", (err) => finish({ code: -1, stdout, stderr: err.message, timedOut }));
    child.on("exit", (code) => {
      exited = true;
      // The child finished on its own: a deadline passing while we drain its pipes is not a timeout.
      clearTimeout(timer); clearTimeout(killTimer);
      pipeTimer = setTimeout(() => {
        child.stdout.destroy(); child.stderr.destroy();
        finish({ code: code ?? 1, stdout, stderr, timedOut });
      }, PIPE_GRACE_MS);
    });
    child.on("close", (code) => finish({ code: code ?? 1, stdout, stderr, timedOut }));
    child.stdin.on("error", () => { /* the child exited or never started; "error"/"close" report it */ });
    if (input !== undefined) child.stdin.end(input); else child.stdin.end();
  });
}

function parseJsonOrNull(text: string): any {
  try { return JSON.parse(text); } catch { return null; }
}

function lastJson(text: string): Json | null {
  const lines = text.trim().split("\n").reverse();
  for (const l of lines) { try { return JSON.parse(l); } catch { /* keep looking */ } }
  return null;
}

// ---- Runtime adapter: the JS libs or the Go `orch` binary behind one interface ------------
type RuntimeName = "js" | "go";
type CachedRouting = { usage: Json; stale: boolean; prompt: () => Promise<string> };
type MailboxMessage = { from: string; to: string; kind: string; subject: string; body: string; refs: string[]; thread: string | null };
type MailboxQuery = { to: string | null; unackedBy: string | null; kind: string | null; limit: number };
type ManifestRequest = { task: string; author: string; paths: string[]; baseline: string | null };
type ManifestCreated = { path: string; manifest_sha256: string; file_count: number };
type IncidentRequest = { component: string; kind: string; summary: string; detail: string; context: Record<string, unknown>; project: string | null | undefined };
/** How to launch an orchestrator script (claude-exec, worker, flip, ...): `cmd` plus the argv prefix. */
type ScriptCommand = { cmd: string; args: string[] };
interface OrchRuntime {
  /** null only for a runtime whose selection failed. */
  name: RuntimeName | null;
  binary: string | null;
  routing(refresh: boolean): Promise<any>;
  cachedRouting(): Promise<CachedRouting | null>;
  effortFor(registry: Json, role: string, size: string): Promise<string | null>;
  budgetGate(vendor: "openai" | "anthropic", force: boolean): Promise<any>;
  registry(): any;
  mailboxPost(root: string, msg: MailboxMessage): Promise<Json>;
  mailboxList(root: string, query: MailboxQuery): Promise<any[]>;
  manifestCreate(root: string, request: ManifestRequest): Promise<ManifestCreated>;
  manifestVerify(root: string, file: string): Promise<any>;
  recordIncident(request: IncidentRequest): Promise<{ fp: string }>;
  command(script: string): ScriptCommand;
}

/** The plugin's runtime configuration (runtime name or orch binary) is unusable. */
class OrchConfigError extends Error {
  constructor(message: string) { super(message); this.name = "OrchConfigError"; }
}

/** An `orch` subcommand failed (exit code not accepted, timeout) or was refused before spawning (code null). */
class OrchCommandError extends Error {
  subcommand: string; code: number | null; stderr: string;
  constructor(subcommand: string, code: number | null, stderr: string, message?: string) {
    const last = stderr.split("\n").map((l) => l.trim()).filter(Boolean).pop() || "(no stderr)";
    super(message ?? `orch ${subcommand} exited ${code}: ${last}`);
    this.name = "OrchCommandError"; this.subcommand = subcommand; this.code = code; this.stderr = stderr;
  }
}

function selectRuntime(options: any, env: NodeJS.ProcessEnv): RuntimeName {
  const isSet = (value: unknown) => value !== undefined && value !== "";
  const chosen = String(isSet(options.runtime) ? options.runtime : isSet(env.ORCH_RUNTIME) ? env.ORCH_RUNTIME : "js").trim().toLowerCase();
  if (chosen === "js" || chosen === "go") return chosen;
  throw new OrchConfigError(`unknown orchestrator runtime "${chosen}" (expected "js" or "go")`);
}

function isExecutableFile(file: string) {
  try {
    if (!fs.statSync(file).isFile()) return false;
    fs.accessSync(file, fs.constants.X_OK);
    return true;
  } catch {
    return false; // missing, unreadable or not executable
  }
}

// The plugin options come from a config file, where no shell expands a leading "~".
function expandHome(file: string) {
  return file === "~" || file.startsWith("~/") ? path.join(os.homedir(), file.slice(1)) : file;
}

// Explicit configuration (option `binary`, ORCH_BIN) that does not apply is an error, never a skip.
// A relative option `binary` resolves against the plugin's working directory.
function resolveOrchBinary(options: any, env: NodeJS.ProcessEnv): string {
  const explicit: [string, string | undefined][] = [["option binary", options.binary ? path.resolve(expandHome(String(options.binary))) : undefined], ["ORCH_BIN", env.ORCH_BIN || undefined]];
  for (const [source, file] of explicit) {
    if (file === undefined) continue;
    if (isExecutableFile(file)) return file;
    throw new OrchConfigError(`orch binary from ${source} is not an executable file: ${file}`);
  }
  const searched = [path.join(os.homedir(), ".local", "bin", "orch"), ...String(env.PATH || "").split(path.delimiter).filter(Boolean).map((dir) => path.join(dir, "orch"))];
  const found = searched.find(isExecutableFile);
  if (found) return found;
  throw new OrchConfigError(`orch binary not found (looked in: ${searched.join(", ")})`);
}

async function createJsRuntime(): Promise<OrchRuntime> {
  const usageLib = await lib("usage.mjs");
  return {
    name: "js",
    binary: null,
    routing: (refresh) => usageLib.getRouting({ refresh }),
    async cachedRouting() {
      const usage = usageLib.readCachedUsage(5 * 60 * 1000);
      if (!usage) return null;
      return {
        usage,
        stale: Boolean(usage.stale),
        prompt: async () => {
          const reg = usageLib.loadRegistry();
          const { resolveRoles, summarizeForPrompt } = await lib("routing.mjs");
          return summarizeForPrompt(resolveRoles(usage, reg, { channels: usageLib.detectChannels() }), usage);
        },
      };
    },
    effortFor: async (registry, role, size) => (await lib("routing.mjs")).effortFor(registry, role, size),
    budgetGate: async (vendor, force) => (await lib("budget.mjs")).budgetGate(vendor, { force }),
    registry: () => usageLib.loadRegistry(),
    mailboxPost: async (root, msg) => (await lib("mailbox.mjs")).post(root, msg),
    mailboxList: async (root, query) => (await lib("mailbox.mjs")).list(root, query),
    async manifestCreate(root, { task, author, paths, baseline }) {
      const m = (await lib("manifest.mjs")).buildManifest({ root, paths, task, author, baseline });
      const out = manifestPath(root, task);
      fs.mkdirSync(path.dirname(out), { recursive: true });
      fs.writeFileSync(out, JSON.stringify(m, null, 2) + "\n");
      return { path: out, manifest_sha256: m.manifest_sha256, file_count: m.file_count };
    },
    async manifestVerify(root, file) {
      const m = JSON.parse(fs.readFileSync(path.resolve(root, file), "utf8"));
      return (await lib("manifest.mjs")).verifyManifest({ root, manifest: m });
    },
    recordIncident: async (request) => (await lib("incident.mjs")).record(request),
    command: (script) => ({ cmd: NODE, args: [path.join(BIN, `${script}.mjs`)] }),
  };
}

function manifestPath(root: string, task: string) {
  return path.join(root, ".orchestrator", "manifests", `${task}.json`);
}

// "budget gate --vendor openai" -> "budget gate": the tokens before the first flag.
function subcommandOf(args: string[]) {
  const firstFlag = args.findIndex((a) => a.startsWith("--"));
  return (firstFlag === -1 ? args : args.slice(0, firstFlag)).join(" ");
}

// orch's argv loops (like the JS CLIs they mirror) read a value starting with "--" as a flag, so
// such free text cannot pass through argv exactly (D-S10-4). Bodies and details go on stdin instead.
function refuseFlagLike(subcommand: string, fields: Record<string, unknown>) {
  for (const [field, value] of Object.entries(fields)) {
    for (const item of Array.isArray(value) ? value : [value]) {
      if (typeof item === "string" && item.startsWith("--")) throw new OrchCommandError(subcommand, null, "", `${field} may not begin with "--" in the go runtime`);
    }
  }
}

// orch's routing and budget gate may collect usage: the Claude feed (Keychain 30 s + HTTP 15 s) runs
// alongside Codex (20 s), then channel detection takes up to 20 s, about 65 s in the worst case.
const ORCH_COLLECTING_TIMEOUT_MS = 90 * 1000;
// Only the tests set this, to reach the deadlines quickly; error messages keep the nominal value.
const ORCH_TIMEOUT_SCALE = Number(process.env.ORCH_PLUGIN_ORCH_TIMEOUT_SCALE) || 1;

function createGoRuntime(bin: string): OrchRuntime {
  // `subcommand` names the call in errors; pass it when a positional argument precedes the flags.
  async function callOrch(args: string[], { input, okCodes = [0], timeoutMs = 30 * 1000, subcommand = subcommandOf(args) }: { input?: string; okCodes?: number[]; timeoutMs?: number; subcommand?: string } = {}) {
    const res = await run(bin, args, { cwd: process.cwd(), input, timeoutMs: timeoutMs * ORCH_TIMEOUT_SCALE });
    if (res.timedOut) throw new OrchCommandError(subcommand, res.code, res.stderr, `orch ${subcommand} timed out after ${timeoutMs / 1000}s`);
    if (!okCodes.includes(res.code)) throw new OrchCommandError(subcommand, res.code, res.stderr);
    return res;
  }
  return {
    name: "go",
    binary: bin,
    async routing(refresh) {
      const res = await callOrch(["routing", "--json", ...(refresh ? ["--refresh"] : [])], { timeoutMs: ORCH_COLLECTING_TIMEOUT_MS });
      return JSON.parse(res.stdout);
    },
    async cachedRouting() {
      const res = await callOrch(["routing", "--cached", "--json"], { okCodes: [0, 3] });
      if (res.code === 3) return null; // no cache yet
      const r = JSON.parse(res.stdout);
      return { usage: r.usage, stale: r.usage.stale === true, prompt: async () => r.prompt };
    },
    async effortFor(_registry, role, size) {
      const effort = (await callOrch(["effort", "--role", role, "--size", size])).stdout.trim();
      return effort === "null" ? null : effort;
    },
    async budgetGate(vendor, force) {
      return JSON.parse((await callOrch(["budget", "gate", "--vendor", vendor, ...(force ? ["--force"] : [])], { timeoutMs: ORCH_COLLECTING_TIMEOUT_MS })).stdout);
    },
    // The registry orch itself reads (orch makes ORCH_HOME and ORCH_REGISTRY absolute); no subcommand needed for a file read.
    registry: () => JSON.parse(fs.readFileSync(process.env.ORCH_REGISTRY ? path.resolve(process.env.ORCH_REGISTRY) : path.resolve(process.env.ORCH_HOME || ORCH, "models.json"), "utf8")),
    async mailboxPost(root, { from, to, kind, subject, body, refs, thread }) {
      refuseFlagLike("mailbox post", { from, to, kind, subject, thread, refs });
      // A missing from/subject is left out so orch reports it ("post needs from and subject").
      const args = ["mailbox", "post", "--root", root, ...(from === undefined ? [] : ["--from", from]), "--to", to, "--kind", kind, ...(subject === undefined ? [] : ["--subject", subject])];
      if (thread !== null) args.push("--thread", thread);
      for (const ref of refs) args.push("--ref", ref);
      args.push("--body", "-", "--json"); // PE-3: only a bare, final --json counts
      return JSON.parse((await callOrch(args, { input: body })).stdout);
    },
    async mailboxList(root, { to, unackedBy, kind, limit }) {
      refuseFlagLike("mailbox list", { to, unacked_by: unackedBy, kind }); // orch would drop such a filter and list everything
      const args = ["mailbox", "list", "--root", root];
      if (to) args.push("--to", to);
      if (unackedBy) args.push("--unacked-by", unackedBy);
      if (kind) args.push("--kind", kind);
      args.push("--limit", String(limit), "--json");
      return JSON.parse((await callOrch(args)).stdout);
    },
    async manifestCreate(root, { task, author, paths, baseline }) {
      refuseFlagLike("manifest create", { task, author, baseline, paths });
      const out = manifestPath(root, task);
      const args = ["manifest", "create", "--root", root, "--task", task, "--author", author, "--out", out, ...(baseline ? ["--baseline", baseline] : []), ...paths];
      const created = JSON.parse((await callOrch(args)).stdout);
      return { path: out, manifest_sha256: created.manifest_sha256, file_count: created.file_count };
    },
    async manifestVerify(root, file) {
      const subcommand = "manifest verify";
      const res = await callOrch(["manifest", "verify", path.resolve(root, file), "--root", root], { okCodes: [0, 1], subcommand });
      const result = parseJsonOrNull(res.stdout);
      // Exit 1 is a mismatch report on stdout, or an unreadable/unparsable manifest with none.
      if (res.code === 1 && result === null) throw new OrchCommandError(subcommand, res.code, res.stderr);
      return result ?? JSON.parse(res.stdout);
    },
    async recordIncident({ component, kind, summary, detail, context, project }) {
      const ctxEntries = Object.entries(context).filter(([, value]) => value !== undefined);
      refuseFlagLike("incident record", { component, kind, summary, ...Object.fromEntries(ctxEntries.map(([key, value]) => [`context.${key}`, value])) });
      const args = ["incident", "record", "--component", component, "--kind", kind, "--summary", summary, "--detail", "-"];
      if (project) args.push("--cwd", project);
      for (const [key, value] of ctxEntries) args.push("--ctx", `${key}=${value}`);
      return { fp: JSON.parse((await callOrch(args, { input: detail })).stdout).recorded };
    },
    command: (script) => ({ cmd: bin, args: [script] }),
  };
}

// Selection or resolution failed: every method throws, so tools error and hooks degrade.
function createBrokenRuntime(name: RuntimeName | null, error: OrchConfigError): OrchRuntime {
  const fail = (): never => { throw error; };
  const failAsync = async (): Promise<never> => fail();
  return {
    name, binary: null, registry: fail, command: fail,
    routing: failAsync, cachedRouting: failAsync, effortFor: failAsync, budgetGate: failAsync,
    mailboxPost: failAsync, mailboxList: failAsync, manifestCreate: failAsync, manifestVerify: failAsync, recordIncident: failAsync,
  };
}

async function createRuntime(options: any): Promise<OrchRuntime> {
  let name: RuntimeName | null = null;
  try {
    name = selectRuntime(options, process.env);
    return name === "js" ? await createJsRuntime() : createGoRuntime(resolveOrchBinary(options, process.env));
  } catch (err) {
    if (err instanceof OrchConfigError) return createBrokenRuntime(name, err);
    throw err;
  }
}

const ROLE_TO_NATIVE_AGENT: Record<string, string> = { planner: "architect-oai", coder: "coder-oai", reviewer: "reviewer-oai", verifier: "verifier-oai", researcher: "researcher-oai", explorer: "explore", utility: "general" };
const NATIVE_AGENT_TIER: Record<string, string> = { "architect-oai": "frontier", "coder-oai": "strong", build: "strong", conductor: "strong", general: "mid", "reviewer-oai": "mid", "verifier-oai": "cheap", "researcher-oai": "cheap", explore: "cheap", title: "cheap", summary: "cheap", compaction: "cheap" };

export default {
  id: "orchestrator",
  async setup(ctx: any) {
    const options = ctx.options ?? {};
    const cwdOf = (input: any, c: any) => path.resolve(input?.cwd || c?.directory || c?.worktree || c?.location?.directory || ctx.location?.directory || process.cwd());
    const rt = await createRuntime(options);
    let lastSummary = "";
    let refreshing: Promise<unknown> | null = null;

    async function routing(refresh = false) {
      const r = await rt.routing(refresh);
      lastSummary = r.prompt;
      return r;
    }
    function refreshSoon() {
      if (refreshing) return;
      refreshing = routing(true).catch(() => null).finally(() => { refreshing = null; });
    }
    // Warm the cache without blocking startup.
    routing(false).catch(() => null);

    // ---- 1. Tools -------------------------------------------------------------------------
    await ctx.tool.transform((e: any) => {
      e.namespace({ name: "orch", description: "Orchestrator: subscription budgets, cross-harness delegation (Claude Code / Codex), mailbox, manifests, workers." });
      const add = (t: any) => e.add({ ...t, options: { namespace: "orch", ...(t.options || {}) } });

      add({
        name: "usage_status",
        description: "Live Claude (session/weekly/Fable) and Codex (5h/weekly) usage with the current budget-aware role->model routing. Call before planning a large batch of work or when deciding where to delegate.",
        input: { type: "object", properties: { refresh: { type: "boolean", description: "Force a fresh read (default: use cache <= 5 min)" } } },
        async execute(input: any) {
          const r = await routing(Boolean(input?.refresh));
          return { content: r.prompt, metadata: { postures: r.resolved.postures, roles: r.resolved.roles, generated_at: r.usage.generated_at, runtime: rt.name, orch: rt.binary } };
        },
      });

      add({
        name: "delegate",
        description: [
          "Delegate a bounded unit of work to the best-placed model/harness for a ROLE, honoring live budgets.",
          "Roles: planner (deep plan/architecture; Fable 5.1 when available), coder (writes code; Opus 5.5 by default),",
          "reviewer (cross-vendor code review), verifier (run tests/reproduce/verify claims), researcher, explorer.",
          "Anthropic models run through the Claude Code CLI on your Claude subscription; OpenAI models run natively here or via the Codex CLI.",
          "Returns the final message plus a session/thread id you can pass back as `resume` to continue that same worker with full context.",
          "If the role resolves to a native opencode subagent, the result tells you which subagent to call instead.",
        ].join(" "),
        input: {
          type: "object",
          required: ["role", "prompt"],
          properties: {
            role: { type: "string", enum: ["planner", "coder", "reviewer", "verifier", "researcher", "explorer"] },
            prompt: { type: "string", description: "Complete, self-contained brief: goal, acceptance criteria, files/paths in scope, constraints, what to report back. The worker has NO access to this conversation." },
            cwd: { type: "string", description: "Project directory (default: current project)" },
            resume: { type: "string", description: "Previous session_id (Claude) or thread_id (Codex) to continue" },
            harness: { type: "string", enum: ["auto", "claude", "codex"], description: "Force a harness; default auto (routing decides)" },
            model: { type: "string", description: "Override model id for the chosen harness" },
            size: { type: "string", enum: ["trivial", "small", "medium", "large", "hard"], description: "Task size; picks reasoning effort from the effort policy (cost vs quality). Default medium. Escalate on failure, not pre-emptively." },
            effort: { type: "string", description: "Explicit reasoning effort override (low|medium|high|xhigh|max); normally leave unset and pass size instead" },
            write: { type: "boolean", description: "Allow file edits (default true for coder, false for planner/reviewer/researcher)" },
            max_turns: { type: "integer", description: "Claude only: cap agentic turns" },
            timeout_min: { type: "integer", description: "Kill after N minutes (default 90)" },
            force: { type: "boolean", description: "Run even if the vendor budget is red" },
          },
        },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const r = await routing(false);
          let role = r.resolved.roles[input.role];
          const writeDefault = input.role === "coder";
          const write = input.write ?? writeDefault;
          let harness = input.harness && input.harness !== "auto" ? input.harness : null;
          // An explicit model name decides the harness when none was given (gpt-* -> codex, claude-* -> claude).
          if (!harness && input.model) harness = /^gpt-|^o\d/i.test(input.model) ? "codex" : /^claude/i.test(input.model) ? "claude" : null;
          const size = input.size || r.registry.effort_policy?.default_size || "medium";
          const policyEffort = await rt.effortFor(r.registry, input.role, size);
          const bigTask = size === "large" || size === "hard";
          // size=hard planning may use a frontier tier even when its weekly posture is amber (never red).
          if (!harness && input.role === "planner" && size === "hard" && role && role.tier !== "frontier") {
            for (const vendor of ["anthropic", "openai"]) {
              const p = r.resolved.postures[vendor];
              if (p?.available && p.frontier !== "red") { const t = r.registry.vendors[vendor].tiers.frontier; role = { vendor, tier: "frontier", model: t.opencode, cli_model: t.cli, variant: t.variant, effort: t.effort, via: r.channels[vendor] ? "opencode" : `${r.registry.vendors[vendor].cli}-cli` }; break; }
            }
          }
          if (!harness) {
            if (!role) return { content: `All vendors are red or unavailable for role ${input.role}. ${r.resolved.reasons.join(" ")} Wait for the nearest reset or pass force=true.` };
            // Native subagents have a fixed effort; large/hard tasks go through the CLI so the policy effort applies.
            if (role.via === "opencode" && !bigTask) return { content: `Role ${input.role} (size ${size}) is best served natively right now: call the subagent "${ROLE_TO_NATIVE_AGENT[input.role] || "general"}" (model ${role.model}). Budget: ${r.prompt.split("\n")[1] || ""}`, metadata: { native_agent: ROLE_TO_NATIVE_AGENT[input.role], role, size } };
            harness = role.via === "claude-cli" ? "claude" : "codex";
          } else if (!role || (harness === "claude" ? role.vendor !== "anthropic" : role.vendor !== "openai")) {
            // Forced harness: pick that vendor's tier for the role straight from the registry.
            const vendor = harness === "claude" ? "anthropic" : "openai";
            const tier = { planner: "frontier", coder: "strong", reviewer: "mid", verifier: "cheap", researcher: "cheap", explorer: "cheap" }[input.role as string] || "mid";
            const t = r.registry.vendors[vendor].tiers[tier];
            role = { vendor, tier, model: t.opencode, cli_model: t.cli, variant: t.variant, effort: t.effort, via: `${harness}-cli` };
          }
          const model = input.model || role.cli_model;
          const effort = input.effort || policyEffort || role.effort || role.variant || "medium";
          const bridge = rt.command(harness === "claude" ? "claude-exec" : "codex-exec");
          const args = [...bridge.args, "--cwd", cwd, "--model", model, "--effort", effort, "--timeout-min", String(input.timeout_min || 90), "--quiet"];
          if (input.resume) args.push("--resume", input.resume);
          if (input.force) args.push("--force");
          if (harness === "claude") { args.push("--permission-mode", write ? "auto" : "plan"); if (input.max_turns) args.push("--max-turns", String(input.max_turns)); }
          else args.push("--sandbox", write ? "workspace-write" : "read-only");
          const preface = `You are acting as the ${input.role.toUpperCase()} for an orchestrated project at ${cwd}. Work only within this brief, report precisely, mark anything not done or not verified. ${write ? "You may edit files in scope." : "Do NOT modify files; produce your output as text (or write only to .orchestrator/ if the brief says so)."}\n\n`;
          await c.progress?.({ title: `${input.role} -> ${harness} ${model}` });
          const res = await run(bridge.cmd, args, { cwd, input: preface + input.prompt, signal: c.signal, timeoutMs: (Number(input.timeout_min || 90) + 2) * 60 * 1000 });
          const out = lastJson(res.stdout) || {};
          refreshSoon();
          if (res.code === 75) return { content: `Refused: ${(out as any).note || "vendor budget is red"}. Resets ${(out as any).resets_at || "unknown"}. Try another role/harness or force=true.`, metadata: out };
          const id = (out as any).session_id || (out as any).thread_id || null;
          const usage = (out as any).usage || null;
          const header = `[${harness} ${model} effort=${effort} size=${size} |${(out as any).ok ? "ok" : "FAILED"} | ${Math.round(((out as any).duration_ms || 0) / 1000)}s | resume=${id} | record=${(out as any).record || "n/a"}]`;
          const body = (out as any).final_message || (out as any).errors?.join("; ") || res.stderr.slice(-2000) || "(no output)";
          return { content: `${header}\n\n${body}`, metadata: { ok: (out as any).ok, resume: id, harness, model, usage, files_changed: (out as any).files_changed, permission_denials: (out as any).permission_denials } };
        },
      });

      add({
        name: "codex_review",
        description: "Run Codex's built-in code review (its own harness and reviewer prompt) on uncommitted changes or against a base branch. Use as the cross-vendor reviewer for code written by Anthropic models.",
        input: { type: "object", properties: { cwd: { type: "string" }, base: { type: "string", description: "Base branch to diff against (default: uncommitted changes)" }, instructions: { type: "string", description: "Extra review focus" }, model: { type: "string" }, force: { type: "boolean" } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const budget = await rt.budgetGate("openai", Boolean(input.force));
          if (!budget.allowed) return { content: `Refused: ${budget.note}` };
          const args = ["review", "--skip-git-repo-check"];
          if (input.base) args.push("--base", input.base); else args.push("--uncommitted");
          if (input.model) args.push("-c", `model="${input.model}"`);
          if (input.instructions) args.push(input.instructions);
          const res = await run("codex", args, { cwd, signal: c.signal, timeoutMs: 40 * 60 * 1000 });
          refreshSoon();
          return { content: (res.stdout || res.stderr).trim().slice(-20000) };
        },
      });

      add({
        name: "mailbox_post",
        description: "Post a durable message to the project mailbox (.orchestrator/mailbox). Use kinds: brief, release, review, closure, question, handoff, blocked, note.",
        input: { type: "object", required: ["from", "subject"], properties: { cwd: { type: "string" }, from: { type: "string" }, to: { type: "string" }, kind: { type: "string" }, subject: { type: "string" }, body: { type: "string" }, refs: { type: "array", items: { type: "string" } }, thread: { type: "string" } } },
        async execute(input: any, c: any) {
          const m = await rt.mailboxPost(cwdOf(input, c), { from: input.from, to: input.to || "*", kind: input.kind || "note", subject: input.subject, body: input.body || "", refs: input.refs || [], thread: input.thread || null });
          return { content: `posted ${m.id}`, metadata: m };
        },
      });

      add({
        name: "mailbox_list",
        description: "Read the project mailbox. Check this at the start of a turn and after delegating work.",
        input: { type: "object", properties: { cwd: { type: "string" }, to: { type: "string" }, unacked_by: { type: "string" }, kind: { type: "string" }, limit: { type: "integer" } } },
        async execute(input: any, c: any) {
          const msgs = await rt.mailboxList(cwdOf(input, c), { to: input.to || null, unackedBy: input.unacked_by || null, kind: input.kind || null, limit: input.limit || 30 });
          if (!msgs.length) return { content: "(mailbox empty)" };
          return { content: msgs.map((m: any) => `${m.at} [${m.kind}] ${m.from} -> ${m.to}: ${m.subject} (id ${m.id})${m.refs?.length ? "\n  refs: " + m.refs.join(", ") : ""}${m.body ? "\n  " + String(m.body).slice(0, 800) : ""}`).join("\n\n"), metadata: { count: msgs.length } };
        },
      });

      add({
        name: "manifest_create",
        description: "Hash the released files (sha256) into .orchestrator/manifests/<task>.json so a reviewer can prove it reviewed exactly this source.",
        input: { type: "object", required: ["task", "author", "paths"], properties: { cwd: { type: "string" }, task: { type: "string" }, author: { type: "string" }, paths: { type: "array", items: { type: "string" } }, baseline: { type: "string" } } },
        async execute(input: any, c: any) {
          const root = cwdOf(input, c);
          const m = await rt.manifestCreate(root, { task: input.task, author: input.author, paths: input.paths, baseline: input.baseline || null });
          return { content: `manifest ${path.relative(root, m.path)}: ${m.file_count} files, sha256 ${m.manifest_sha256}`, metadata: { path: m.path, manifest_sha256: m.manifest_sha256 } };
        },
      });

      add({
        name: "manifest_verify",
        description: "Verify the working tree still matches a manifest (run before AND after reading code you are reviewing).",
        input: { type: "object", required: ["manifest"], properties: { cwd: { type: "string" }, manifest: { type: "string" } } },
        async execute(input: any, c: any) {
          const root = cwdOf(input, c);
          const r = await rt.manifestVerify(root, input.manifest);
          return { content: r.ok ? `OK: all ${r.checked} files match` : `MISMATCH: changed ${r.mismatched.join(", ") || "-"}; missing ${r.missing.join(", ") || "-"}`, metadata: r };
        },
      });

      add({
        name: "worker_start",
        description: "Start a detached, lock-protected worker that runs a brief file on Claude Code or Codex in the background (survives this session). Pair with the supervisor to get woken on completion.",
        input: { type: "object", required: ["brief", "harness"], properties: { cwd: { type: "string" }, brief: { type: "string" }, harness: { type: "string", enum: ["claude", "codex"] }, lane: { type: "string" }, model: { type: "string" }, effort: { type: "string" }, resume: { type: "string" }, breadth: { type: "string", enum: ["focused", "standard", "broad"] } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const worker = rt.command("worker");
          const args = [...worker.args, "start", "--brief", input.brief, "--harness", input.harness, "--cwd", cwd, "--detach"];
          for (const k of ["lane", "model", "effort", "resume", "breadth"]) if (input[k]) args.push(`--${k}`, String(input[k]));
          const res = await run(worker.cmd, args, { cwd, timeoutMs: 60000 });
          return { content: (res.stdout || res.stderr).trim() };
        },
      });

      add({
        name: "worker_status",
        description: "Status of all worker lanes in the project (.orchestrator/workers).",
        input: { type: "object", properties: { cwd: { type: "string" }, lane: { type: "string" } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const worker = rt.command("worker");
          const args = [...worker.args, "status", "--cwd", cwd]; if (input.lane) args.push("--lane", input.lane);
          const res = await run(worker.cmd, args, { cwd, timeoutMs: 30000 });
          return { content: (res.stdout || res.stderr).trim() || "(no workers)" };
        },
      });

      add({
        name: "incident",
        description: "Record an orchestrator incident for the next audit: a tool/contract mismatch, a misroute, a budget surprise, a crash, or a correction the human gave you about how the orchestrator should behave. Local, redacted, append-only. Use whenever the orchestrator (not the product code) misbehaved or the user corrected the workflow.",
        input: { type: "object", required: ["kind", "summary"], properties: { kind: { type: "string", enum: ["bug", "limit", "misroute", "permission", "crash", "timeout", "contract", "human-correction", "flip", "note"] }, component: { type: "string", description: "plugin|bridge|worker|supervisor|flip|usage|contract|agent (default agent)" }, summary: { type: "string" }, detail: { type: "string" }, cwd: { type: "string" } } },
        async execute(input: any, c: any) {
          const e = await rt.recordIncident({ component: input.component || "agent", kind: input.kind, summary: input.summary, detail: input.detail || "", context: { agent: c.agent, session: c.sessionID }, project: cwdOf(input, c) });
          return { content: `recorded incident ${e.fp}` };
        },
      });

      add({
        name: "supervisor",
        description: "Control the project supervisor daemon that watches workers and wakes the coordinator: start | status | stop | ack.",
        input: { type: "object", required: ["action"], properties: { cwd: { type: "string" }, action: { type: "string", enum: ["start", "status", "stop", "ack"] }, event: { type: "string" } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const supervisor = rt.command("supervisor");
          const args = [...supervisor.args, input.action, "--cwd", cwd]; if (input.event) args.push("--event", input.event);
          const res = await run(supervisor.cmd, args, { cwd, timeoutMs: 30000 });
          return { content: (res.stdout || res.stderr).trim() };
        },
      });
    });

    // ---- 2. Budget + routing in the system prompt of primary agents -----------------------
    const PRIMARY = new Set(["conductor", "build", "plan"]);
    await ctx.session.hook("context", async (e: any) => {
      if (!PRIMARY.has(String(e.agent))) return;
      try {
        const cached = await rt.cachedRouting();
        if (!cached || cached.stale) refreshSoon();
        if (!lastSummary && cached) lastSummary = await cached.prompt();
        if (lastSummary) e.system.push({ type: "text", text: `<orchestrator_budget updated="${cached?.usage.generated_at || "unknown"}">\n${lastSummary}\nRules: never start frontier-planning or large batches when the relevant window is red; prefer the vendor with headroom; schedule heavy work after the nearest reset; cross-vendor review is the default. Use orch_usage_status for fresh numbers.\n</orchestrator_budget>` });
      } catch { /* never block the turn on budget data */ }
    });

    // ---- 2b. Budget gate for native subagents + learn from provider limit errors ----------
    const NATIVE_OPENAI_AGENTS = new Set(Object.keys(NATIVE_AGENT_TIER));
    await ctx.tool.hook("execute.before", async (e: any) => {
      if (e.tool !== "subagent") return;
      const target = String((e.input as any)?.agent || "");
      if (!NATIVE_OPENAI_AGENTS.has(target)) return;
      try {
        const r = await routing(false);
        const p = r.resolved.postures.openai;
        if (p?.available && p.overall === "red") {
          const d = p.detail;
          throw new Error(`[orchestrator] OpenAI budget is red (5h ${d.five_hour?.pct}% resets ${d.five_hour?.resets_at}; weekly ${d.weekly?.pct}%). Native subagent "${target}" refused. Use orch_delegate(harness="claude", ...) for this role, or defer until the reset and record STATE.md.`);
        }
      } catch (err: any) {
        if (String(err?.message || "").startsWith("[orchestrator]")) throw err;
        /* usage unavailable: do not block */
      }
    });
    // When the conductor's own provider is exhausted, flip the project to the Claude door (headless worker lane)
    // instead of retrying into a red window. Cooldown and the lane lock prevent double flips.
    const LIMIT_RE = /usage_limit|usage limit|rate_limit|subscription_sharing/i;
    let lastFlipAttempt = 0;
    async function flipToClaude(cwd: string, reason: string) {
      if (Date.now() - lastFlipAttempt < 10 * 60 * 1000) return;
      lastFlipAttempt = Date.now();
      try {
        const flip = rt.command("flip");
        const d = await run(flip.cmd, [...flip.args, "decide", "--active", "opencode", "--cwd", cwd], { cwd, timeoutMs: 60000 });
        const decision = lastJson(d.stdout) as any;
        if (decision?.action === "flip") await run(flip.cmd, [...flip.args, "run", "--to", "claude", "--from", "opencode", "--reason", reason, "--cwd", cwd], { cwd, timeoutMs: 60000 });
      } catch (err) {
        // A failed flip stays best effort, but a misconfigured runtime must not disable it silently.
        if (err instanceof OrchConfigError) console.warn(`[orchestrator] usage-limit flip skipped: ${err.message}`);
      }
    }
    await ctx.session.hook("retry", async (e: any) => {
      const msg = String(e.error?.message || e.error?.name || "");
      if (!LIMIT_RE.test(msg)) return;
      refreshSoon();
      if (String(e.agent) === "conductor") {
        e.decision = { retry: false };
        await flipToClaude(ctx.location?.directory || process.cwd(), `opencode conductor hit a usage limit: ${msg.slice(0, 80)}`);
      }
    });
    await ctx.tool.hook("execute.after", async (e: any) => {
      // Our own tools failing is an orchestrator defect by definition: log it for the audit.
      if (e.status === "error" && String(e.tool).startsWith("orch")) {
        try { await rt.recordIncident({ component: "plugin", kind: "bug", summary: `tool ${e.tool} errored: ${String(e.error?.message || e.error).slice(0, 160)}`, detail: JSON.stringify(e.input).slice(0, 800), context: { agent: e.agent }, project: ctx.location?.directory }); } catch { /* never block */ }
      }
      if (e.status === "error" && LIMIT_RE.test(String(e.error?.message || e.error || ""))) {
        refreshSoon();
        if (e.tool === "subagent" && String(e.agent) === "conductor") await flipToClaude(ctx.location?.directory || process.cwd(), "native subagent hit the OpenAI usage limit");
      }
    });

    // ---- 3. Keep native agent models in sync with models.json ----------------------------
    if (options.syncModelsFromRegistry !== false) {
      try {
        const reg = rt.registry();
        const tiers = reg.vendors.openai.tiers;
        await ctx.agent.transform((e: any) => {
          for (const [id, tier] of Object.entries(NATIVE_AGENT_TIER)) {
            const t = (tiers as any)[tier]; if (!t) continue;
            const [providerID, modelID] = String(t.opencode).split("/");
            e.update(id, (a: any) => { a.model = { providerID, id: modelID, ...(t.effort ? { variant: t.effort } : {}) }; });
          }
        });
      } catch (err) {
        // The registry is optional; a misconfigured runtime is worth a line in the opencode log.
        if (err instanceof OrchConfigError) console.warn(`[orchestrator] model sync skipped: ${err.message}`);
      }
    }

    return async () => { /* nothing to clean up */ };
  },
};
