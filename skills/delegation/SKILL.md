---
name: delegation
description: How and when to hand work between Claude Code and Codex (and native subagents): choosing role/harness by budget and task shape, writing self-contained briefs, resuming sessions, background workers with the supervisor, and reading run records. Load when about to delegate or when a vendor window is red.
---
# Delegation

## Choose by task shape, then by budget
- Deep planning, root-cause on gnarly bugs, architecture: **planner** (Fable 5.1 max; Astra when Fable is red).
- Writing code: **coder** (Opus 5.5 high; GPT-6.1 Sol high when Claude is red). Long autonomous
  multi-hour implementation with heavy tool use: Codex CLI (`harness="codex"`, Sol high, sandbox
  workspace-write) is well suited and runs in its own sandbox.
- Review: **reviewer** = the other vendor. `orch_codex_review` gives Codex's native review harness.
- Verification, research, exploration: cheap tiers.
- Check `<orchestrator_budget>`; red = route elsewhere or defer; amber = no frontier/batches.

## Writing a brief (the worker cannot see your conversation)
Goal; acceptance criteria; in-scope paths; out of scope; context facts (with file paths); exact
checks to run; what to report back and where; constraints (no push, no deploy, no secrets). Include
the plan section verbatim if it matters. Keep to one bounded unit.

## Mechanics
- `orch_delegate(role, prompt, cwd?, resume?, harness?, write?)` runs synchronously and returns the
  message + `resume` id. Pass `resume` to continue the same worker with its memory.
- `orch_worker_start(brief, harness)` runs a brief file in the background with a lane lock, state
  file and log; `orch_supervisor(action="start")` watches lanes and posts `wake` mailbox messages (and
  wakes a configured session) on completion, failure, quiet, or budget deferral.
- Every run leaves `.orchestrator/runs/*.json` (model, tokens, cost, files changed, resume id).
- Budget gate: bridges exit 75 when the vendor window is red; `force` overrides deliberately.

## Codex-specific
`codex exec` has no `--full-auto` any more; the bridge uses `--sandbox workspace-write --approve-for-me`
for writes and `read-only` otherwise. Efforts: low|medium|high|xhigh|max|ultra (ultra spawns Codex
subagents; expensive). Fast mode costs 2.5x usage; never enable it automatically.

## Claude-specific
`claude -p` with `--permission-mode acceptEdits` for writes, `plan` for read-only; `--effort`;
`--max-turns`; `--resume <session>`. `--bare` ignores OAuth (API key only), so never use it on the
subscription.
