# Changelog

## Unreleased

- Go port (`go/`, not yet used by the plugin or commands): `orch usage [--refresh] [--json]`
  produces output byte-identical to `bin/usage.mjs` and shares its usage cache in both directions,
  including keeping the last good reading when a refresh fails within 30 minutes of the last
  cache write. The window rolls, as in the JS: repeated failures keep the reading, aged by
  `last_good_at`. Also adds the plugin-facing `orch routing [--refresh | --cached] [--json]`,
  `orch budget gate --vendor V [--force] [--frontier]` and `orch effort --role R [--size S]`.
  The `usage` parity group compares all four against the JS.
- Go port: `orch mailbox post|list|ack|reply|inbox` matches `bin/mailbox.mjs` byte for byte
  (stdout, exit codes, `.orchestrator/mailbox/*.jsonl`) and appends one write per line, so JS
  and Go writers can share a mailbox. `post --json` prints the whole message. Uncaught JS errors
  print only their error line. The new `mailbox` parity group runs in CI.
- Go port: `orch manifest create|verify|diff` matches `bin/manifest.mjs` byte for byte (stdout,
  exit codes, manifest files), including the walk's symlink and FIFO handling and UTF-16 key
  order, and `internal/runrecord` writes run records with the same names and bytes as
  `writeRunRecord`. The new `manifest` parity group runs in CI.

## 0.4.1 — 2026-10-18

- `kickoff.mjs` (preflight + door selection + launch, pause when both red) and `watch.mjs` (single dashboard).

## 0.4.0 — 2026-10-18

- Incident log (`lib/incident.mjs`, `bin/incident.mjs`, `orch_incident` tool): redacted, fingerprinted, local; written automatically by bridges, worker, supervisor and plugin; read by `/retro` and the audit skill; `share` makes a sanitized bundle, never uploads.
- Keep-awake: detached workers and the supervisor run under `caffeinate -i -s` (macOS) / `systemd-inhibit` (Linux); `runtime.keep_awake` toggle.

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
