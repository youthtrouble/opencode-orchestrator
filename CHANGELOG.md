# Changelog

## 0.3.1 — 2026-10-18

- `flip.mjs pause` + supervisor relaunch of a paused conductor as soon as either window is usable (home door preferred); previously only the other-door case relaunched automatically.

## 0.3.0 — 2026-10-18

- Conductor flip: `orchestrator/bin/flip.mjs` (decide/run/status) with a tested policy
  (`lib/flip.mjs`): forced flip when the active conductor's vendor is red, hand-back to the home
  door at brief boundaries once it is stably green, cooldown, pause when both are red.
- Plugin flips the opencode conductor to the Claude door on usage-limit errors; the supervisor
  evaluates flips once a minute when a project has a conductor lane or has flipped before.
- `opencode-exec.mjs` bridge and `worker.mjs --harness opencode --agent` so either conductor can
  run headless as the `conductor` worker lane.
- `.orchestrator/` is added to `.git/info/exclude` on first flip/use.

## 0.2.0 — 2026-10-18

- Effort policy by task size (`effort_policy` in `orchestrator/models.json`); `orch_delegate` takes
  `size`; static agent efforts lowered to each model's recommended default (escalation per task).
- Budget gate for native OpenAI subagents: refused with guidance when the OpenAI window is red;
  provider usage-limit errors trigger an immediate usage refresh.
- `orch_delegate` infers the harness from an explicit model name (fixes the "Codex bridge failed
  before starting" observed during the first release run).
- Native provider detection reads opencode v2's SQLite credential store.
- Claude Code front door: `claude/agents/*` (conductor, architect, coder, reviewer, verifier,
  researcher and their hard variants), `claude/skills/orchestrator`, `install-claude.sh`.
- Contract: size every delegation; a conductor whose own provider is exhausted records STATE.md and
  hands off instead of retrying.

## 0.1.0 — 2026-10-18

First release.

- Sanitized initial release of the two-vendor opencode v2 orchestration configuration, role
  prompts, usage readers, routing, CLI bridges, mailbox, manifests, workers and supervisor.
- Reversible managed-entry symlink installation, optional copy mode, private force backups,
  ownership checks and archive-preserving uninstall.
- Root setup, routing, security, recovery and contribution documentation; MIT license.
- Retained 14 fast runtime unit tests and added 29 isolated installer integration cases under
  separate root `tests/`, outside the doctor's unit-test timeout.
- Initial packaging observation: Codex exec CLI argument parsing failed before delegation in that
  environment; this is not a fresh reproduction on the latest live CLIs. The original runtime is
  retained, not represented as end-to-end verified.

See [orchestrator/CHANGELOG.md](orchestrator/CHANGELOG.md) for the original implementation notes.
