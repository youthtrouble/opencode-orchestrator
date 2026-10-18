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

function run(cmd: string, args: string[], { cwd, input, signal, timeoutMs = 2 * 60 * 60 * 1000 }: { cwd: string; input?: string; signal?: AbortSignal; timeoutMs?: number }) {
  return new Promise<{ code: number; stdout: string; stderr: string }>((resolve) => {
    const child = spawn(cmd, args, { cwd, stdio: ["pipe", "pipe", "pipe"], env: { ...process.env, CLAUDECODE: undefined } });
    let stdout = "", stderr = "";
    child.stdout.on("data", (c) => (stdout += c));
    child.stderr.on("data", (c) => (stderr += c));
    const timer = setTimeout(() => child.kill("SIGTERM"), timeoutMs);
    const onAbort = () => { try { child.kill("SIGTERM"); } catch { /* gone */ } };
    signal?.addEventListener("abort", onAbort, { once: true });
    child.on("close", (code) => { clearTimeout(timer); signal?.removeEventListener("abort", onAbort); resolve({ code: code ?? 1, stdout, stderr }); });
    if (input !== undefined) child.stdin.end(input); else child.stdin.end();
  });
}

function lastJson(text: string): Json | null {
  const lines = text.trim().split("\n").reverse();
  for (const l of lines) { try { return JSON.parse(l); } catch { /* keep looking */ } }
  return null;
}

const ROLE_TO_NATIVE_AGENT: Record<string, string> = { planner: "architect-oai", coder: "coder-oai", reviewer: "reviewer-oai", verifier: "verifier-oai", researcher: "researcher-oai", explorer: "explore", utility: "general" };
const NATIVE_AGENT_TIER: Record<string, string> = { "architect-oai": "frontier", "coder-oai": "strong", build: "strong", conductor: "strong", general: "mid", "reviewer-oai": "mid", "verifier-oai": "cheap", "researcher-oai": "cheap", explore: "cheap", title: "cheap", summary: "cheap", compaction: "cheap" };

export default {
  id: "orchestrator",
  async setup(ctx: any) {
    const options = ctx.options ?? {};
    const cwdOf = (input: any, c: any) => path.resolve(input?.cwd || c?.directory || c?.worktree || c?.location?.directory || ctx.location?.directory || process.cwd());
    const usageLib = await lib("usage.mjs");
    let lastSummary = "";
    let refreshing: Promise<unknown> | null = null;

    async function routing(refresh = false) {
      const r = await usageLib.getRouting({ refresh });
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
          return { content: r.prompt, metadata: { postures: r.resolved.postures, roles: r.resolved.roles, generated_at: r.usage.generated_at } };
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
          const { effortFor } = await lib("routing.mjs");
          const size = input.size || r.registry.effort_policy?.default_size || "medium";
          const policyEffort = effortFor(r.registry, input.role, size);
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
          const args = [path.join(BIN, harness === "claude" ? "claude-exec.mjs" : "codex-exec.mjs"), "--cwd", cwd, "--model", model, "--effort", effort, "--timeout-min", String(input.timeout_min || 90), "--quiet"];
          if (input.resume) args.push("--resume", input.resume);
          if (input.force) args.push("--force");
          if (harness === "claude") { args.push("--permission-mode", write ? "auto" : "plan"); if (input.max_turns) args.push("--max-turns", String(input.max_turns)); }
          else args.push("--sandbox", write ? "workspace-write" : "read-only");
          const preface = `You are acting as the ${input.role.toUpperCase()} for an orchestrated project at ${cwd}. Work only within this brief, report precisely, mark anything not done or not verified. ${write ? "You may edit files in scope." : "Do NOT modify files; produce your output as text (or write only to .orchestrator/ if the brief says so)."}\n\n`;
          await c.progress?.({ title: `${input.role} -> ${harness} ${model}` });
          const res = await run(NODE, args, { cwd, input: preface + input.prompt, signal: c.signal, timeoutMs: (Number(input.timeout_min || 90) + 2) * 60 * 1000 });
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
          const budget = await (await lib("budget.mjs")).budgetGate("openai", { force: Boolean(input.force) });
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
          const m = (await lib("mailbox.mjs")).post(cwdOf(input, c), { from: input.from, to: input.to || "*", kind: input.kind || "note", subject: input.subject, body: input.body || "", refs: input.refs || [], thread: input.thread || null });
          return { content: `posted ${m.id}`, metadata: m };
        },
      });

      add({
        name: "mailbox_list",
        description: "Read the project mailbox. Check this at the start of a turn and after delegating work.",
        input: { type: "object", properties: { cwd: { type: "string" }, to: { type: "string" }, unacked_by: { type: "string" }, kind: { type: "string" }, limit: { type: "integer" } } },
        async execute(input: any, c: any) {
          const msgs = (await lib("mailbox.mjs")).list(cwdOf(input, c), { to: input.to || null, unackedBy: input.unacked_by || null, kind: input.kind || null, limit: input.limit || 30 });
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
          const { buildManifest } = await lib("manifest.mjs");
          const m = buildManifest({ root, paths: input.paths, task: input.task, author: input.author, baseline: input.baseline || null });
          const out = path.join(root, ".orchestrator", "manifests", `${input.task}.json`);
          fs.mkdirSync(path.dirname(out), { recursive: true });
          fs.writeFileSync(out, JSON.stringify(m, null, 2) + "\n");
          return { content: `manifest ${path.relative(root, out)}: ${m.file_count} files, sha256 ${m.manifest_sha256}`, metadata: { path: out, manifest_sha256: m.manifest_sha256 } };
        },
      });

      add({
        name: "manifest_verify",
        description: "Verify the working tree still matches a manifest (run before AND after reading code you are reviewing).",
        input: { type: "object", required: ["manifest"], properties: { cwd: { type: "string" }, manifest: { type: "string" } } },
        async execute(input: any, c: any) {
          const root = cwdOf(input, c);
          const { verifyManifest } = await lib("manifest.mjs");
          const m = JSON.parse(fs.readFileSync(path.resolve(root, input.manifest), "utf8"));
          const r = verifyManifest({ root, manifest: m });
          return { content: r.ok ? `OK: all ${r.checked} files match` : `MISMATCH: changed ${r.mismatched.join(", ") || "-"}; missing ${r.missing.join(", ") || "-"}`, metadata: r };
        },
      });

      add({
        name: "worker_start",
        description: "Start a detached, lock-protected worker that runs a brief file on Claude Code or Codex in the background (survives this session). Pair with the supervisor to get woken on completion.",
        input: { type: "object", required: ["brief", "harness"], properties: { cwd: { type: "string" }, brief: { type: "string" }, harness: { type: "string", enum: ["claude", "codex"] }, lane: { type: "string" }, model: { type: "string" }, effort: { type: "string" }, resume: { type: "string" }, breadth: { type: "string", enum: ["focused", "standard", "broad"] } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const args = [path.join(BIN, "worker.mjs"), "start", "--brief", input.brief, "--harness", input.harness, "--cwd", cwd, "--detach"];
          for (const k of ["lane", "model", "effort", "resume", "breadth"]) if (input[k]) args.push(`--${k}`, String(input[k]));
          const res = await run(NODE, args, { cwd, timeoutMs: 60000 });
          return { content: (res.stdout || res.stderr).trim() };
        },
      });

      add({
        name: "worker_status",
        description: "Status of all worker lanes in the project (.orchestrator/workers).",
        input: { type: "object", properties: { cwd: { type: "string" }, lane: { type: "string" } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const args = [path.join(BIN, "worker.mjs"), "status", "--cwd", cwd]; if (input.lane) args.push("--lane", input.lane);
          const res = await run(NODE, args, { cwd, timeoutMs: 30000 });
          return { content: (res.stdout || res.stderr).trim() || "(no workers)" };
        },
      });

      add({
        name: "incident",
        description: "Record an orchestrator incident for the next audit: a tool/contract mismatch, a misroute, a budget surprise, a crash, or a correction the human gave you about how the orchestrator should behave. Local, redacted, append-only. Use whenever the orchestrator (not the product code) misbehaved or the user corrected the workflow.",
        input: { type: "object", required: ["kind", "summary"], properties: { kind: { type: "string", enum: ["bug", "limit", "misroute", "permission", "crash", "timeout", "contract", "human-correction", "flip", "note"] }, component: { type: "string", description: "plugin|bridge|worker|supervisor|flip|usage|contract|agent (default agent)" }, summary: { type: "string" }, detail: { type: "string" }, cwd: { type: "string" } } },
        async execute(input: any, c: any) {
          const { record } = await lib("incident.mjs");
          const e = record({ component: input.component || "agent", kind: input.kind, summary: input.summary, detail: input.detail || "", context: { agent: c.agent, session: c.sessionID }, project: cwdOf(input, c) });
          return { content: `recorded incident ${e.fp}` };
        },
      });

      add({
        name: "supervisor",
        description: "Control the project supervisor daemon that watches workers and wakes the coordinator: start | status | stop | ack.",
        input: { type: "object", required: ["action"], properties: { cwd: { type: "string" }, action: { type: "string", enum: ["start", "status", "stop", "ack"] }, event: { type: "string" } } },
        async execute(input: any, c: any) {
          const cwd = cwdOf(input, c);
          const args = [path.join(BIN, "supervisor.mjs"), input.action, "--cwd", cwd]; if (input.event) args.push("--event", input.event);
          const res = await run(NODE, args, { cwd, timeoutMs: 30000 });
          return { content: (res.stdout || res.stderr).trim() };
        },
      });
    });

    // ---- 2. Budget + routing in the system prompt of primary agents -----------------------
    const PRIMARY = new Set(["conductor", "build", "plan"]);
    await ctx.session.hook("context", async (e: any) => {
      if (!PRIMARY.has(String(e.agent))) return;
      try {
        const cached = usageLib.readCachedUsage(5 * 60 * 1000);
        if (!cached || cached.stale) refreshSoon();
        if (!lastSummary && cached) { const reg = usageLib.loadRegistry(); const { resolveRoles, summarizeForPrompt } = await lib("routing.mjs"); lastSummary = summarizeForPrompt(resolveRoles(cached, reg, { channels: usageLib.detectChannels() }), cached); }
        if (lastSummary) e.system.push({ type: "text", text: `<orchestrator_budget updated="${cached?.generated_at || "unknown"}">\n${lastSummary}\nRules: never start frontier-planning or large batches when the relevant window is red; prefer the vendor with headroom; schedule heavy work after the nearest reset; cross-vendor review is the default. Use orch_usage_status for fresh numbers.\n</orchestrator_budget>` });
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
        const d = await run(NODE, [path.join(BIN, "flip.mjs"), "decide", "--active", "opencode", "--cwd", cwd], { cwd, timeoutMs: 60000 });
        const decision = lastJson(d.stdout) as any;
        if (decision?.action === "flip") await run(NODE, [path.join(BIN, "flip.mjs"), "run", "--to", "claude", "--from", "opencode", "--reason", reason, "--cwd", cwd], { cwd, timeoutMs: 60000 });
      } catch { /* best effort */ }
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
        try { const { record } = await lib("incident.mjs"); record({ component: "plugin", kind: "bug", summary: `tool ${e.tool} errored: ${String(e.error?.message || e.error).slice(0, 160)}`, detail: JSON.stringify(e.input).slice(0, 800), context: { agent: e.agent }, project: ctx.location?.directory }); } catch { /* never block */ }
      }
      if (e.status === "error" && LIMIT_RE.test(String(e.error?.message || e.error || ""))) {
        refreshSoon();
        if (e.tool === "subagent" && String(e.agent) === "conductor") await flipToClaude(ctx.location?.directory || process.cwd(), "native subagent hit the OpenAI usage limit");
      }
    });

    // ---- 3. Keep native agent models in sync with models.json ----------------------------
    if (options.syncModelsFromRegistry !== false) {
      try {
        const reg = usageLib.loadRegistry();
        const tiers = reg.vendors.openai.tiers;
        await ctx.agent.transform((e: any) => {
          for (const [id, tier] of Object.entries(NATIVE_AGENT_TIER)) {
            const t = (tiers as any)[tier]; if (!t) continue;
            const [providerID, modelID] = String(t.opencode).split("/");
            e.update(id, (a: any) => { a.model = { providerID, id: modelID, ...(t.effort ? { variant: t.effort } : {}) }; });
          }
        });
      } catch { /* registry optional */ }
    }

    return async () => { /* nothing to clean up */ };
  },
};
