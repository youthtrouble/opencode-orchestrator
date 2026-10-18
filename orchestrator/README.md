# opencode orchestrator

A global layer on top of opencode v2 that turns a short idea into shipped, reviewed, verified
software using two subscriptions with awareness of their limits:

- **Anthropic** (Claude subscription via Claude Code): Fable 5.1 plans, Opus 5.5 writes code, Sonnet 5.5 reviews/verifies,
  Haiku 5.5 explores. Reached through the Claude Code CLI (`bin/claude-exec.mjs`) because Anthropic
  does not allow subscription OAuth inside third-party tools.
- **OpenAI** (ChatGPT subscription via Codex / opencode): GPT-6 Astra plans, GPT-6.1 Sol writes code and reviews, GPT-6 Luna does cheap
  work. Native in opencode via "Sign in with ChatGPT", and through the Codex CLI (`bin/codex-exec.mjs`,
  `codex review`).

## Layout
```
~/.config/opencode/
  opencode.jsonc          global config (default agent: conductor; OpenAI native default model)
  AGENTS.md               operating contract loaded into every session
  agents/*.md             conductor (primary) + native OpenAI subagents
  commands/*.md           /usage /plan /ship /review /handoff /continue /brief /retro
                          /delegate-claude /delegate-codex /audit-orchestrator
  skills/*/SKILL.md       deep-planning, clean-code, cross-review, handoff-protocol, delegation, orchestrator-audit
  plugins/orchestrator.ts registers orch_* tools, injects live budget into primary agents, syncs models from models.json
  orchestrator/
    models.json           SOURCE OF TRUTH: vendor tiers, role preferences, thresholds
    bin/usage.mjs         live Claude + Codex usage -> posture -> role routing   (--refresh --json)
    bin/claude-exec.mjs   bounded task on Claude Code (print mode) -> JSON result + session id
    bin/codex-exec.mjs    bounded task on Codex CLI -> JSON result + thread id
    bin/worker.mjs        detached, lock-protected worker running a brief file on either harness
    bin/supervisor.mjs    watches workers, posts wake events to the mailbox + wake channels
    bin/mailbox.mjs       durable project mailbox (.orchestrator/mailbox)
    bin/manifest.mjs      sha256 source manifests for reviews
    bin/doctor.mjs        health check;   bin/setup.sh  first-time setup
    lib/                  pure logic (routing, supervise, usage readers)   tests/  node --test
```

## How a project runs
1. `opencode` in the repo. The conductor reads `.orchestrator/STATE.md`, the mailbox and the budget.
2. `/plan <idea>`: frontier planner writes `.orchestrator/plans/<slug>.md` with bounded briefs.
3. `/ship <idea>`: for each brief: coder (Opus 5.5 via Claude Code, or Sol) -> manifest -> cross-vendor
   review (Codex review / Sonnet) -> fixes -> verifier runs checks -> STATE.md updated.
4. Long runs: `orch_worker_start` + `orch_supervisor start` keep going in the background and wake the
   coordinator (mailbox + optional `opencode run --session`, `codex queue`, or Claude resume).

## Budget model
`usage.mjs` reads the Claude OAuth usage endpoint (session %, Fable weekly %) and the Codex
app-server (`account/rateLimits/read`: 5h %, weekly %). Thresholds in `models.json`: amber 60, red 85,
a window resetting within 20 min counts one level greener. Red = route the role to the other vendor
(or pause); the bridges refuse to start (exit 75) unless `--force`. Deferred workers are re-dispatched
by the supervisor after the reset.

## Keep it current
`/audit-orchestrator` (skill `orchestrator-audit`) re-verifies model ids, prices, limits and CLI flags
against official sources and the live catalogs, updates `models.json`, reruns tests and smoke runs.

## Other harnesses
The same scripts work from Claude Code or a shell: `node ~/.config/opencode/orchestrator/bin/usage.mjs`,
`.../codex-exec.mjs --prompt "..."`, `.../worker.mjs start --brief ... --harness codex`.
