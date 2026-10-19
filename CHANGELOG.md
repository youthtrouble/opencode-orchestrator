# Changelog

## Unreleased

- Go port: `orch doctor` mirrors `bin/doctor.mjs` row for row (binaries, logins, usage feeds and
  routing, opencode config, plugin, agents, plugin log) and exit code; PE-18 adds `orch binary`
  and `orch self-test` rows and runs `go test ./...` for the `unit tests` row. The parity fakes
  gain a canned-answer mode; new `doctor` parity group, run in CI.
- Go port: `orch supervisor start|serve` replace `bin/supervisor.mjs start|serve`: `start` launches
  a detached, kept-awake `orch supervisor serve`; `serve` claims the same `supervisor.lock`, ticks
  every 5 s (lanes, events, wake channels, redispatch, conductor flip evaluation, all self-exec'd
  on `orch`) and stops through `supervisor.stop`. New `supervisor` parity cases run both daemons
  for one tick; PE-17 (a missing `opencode` does not stop `orch supervisor serve`) is asserted.
  Further serve cases cover the stop file at the first loop, live-pid lanes, a `null` config,
  running lanes (quiet and check), the conductor flip evaluation (each case now required to reach
  it, with `decide` over a seeded usage cache), conductor launches (paused relaunch, pending launch,
  `decide` flip, finished-early `--resume`, never-started) and a redispatch launch with their
  runners settled, the `claude-resume` channel, restarts across runtimes and an opt-in slow case
  (`ORCH_PARITY_SLOW=1`).
- Go port: `orch supervisor status|stop|ack` matches `bin/supervisor.mjs` byte for byte (stdout,
  stderr, exit codes, `.orchestrator/supervisor-acks.json`), every state and acks shape included
  (Object.prototype names acknowledged, `__proto__` included, as in the JS). The supervisor's lane
  tick (phases, the two-phase intent, events, redispatch of budget-deferred lanes) is ported and
  unit-tested with an injected clock; `start` and `serve` arrive in S8c and exit 2 until then.
  New PE-16 (hand-edited non-object shapes). New `supervisor` parity group, run in CI.
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
- usage (JS and Go): a cached reading is stale once a routing window it holds (Claude session,
  weekly or Fable weekly; Codex 5-hour or weekly) resets after the reading was taken, not only
  after the 5-minute TTL. A conductor therefore relaunches right at a reset. When the post-reset
  collection fails, the last good reading is kept and retried every 5 minutes, not on every read.
- Go port: `orch incident record|list|report|share` and `orch policy show|set|unattended` match
  `bin/incident.mjs` and `bin/policy.mjs` byte for byte (stdout, exit codes, incident log, share
  bundle, `policy.json`): the same redaction, fingerprints (JS `toLowerCase`, final sigma
  included) and report order (a port of Node 26's V8 sort; Node 22/24 may order malformed short
  logs differently, PE-5), the registry read exactly as `policy.mjs`
  reads it, and an explicitly empty `HOME` kept as Node keeps it. Entries carry `orch`'s version
  (PE-4). The new `incident` and `policy` parity groups run in CI.
- Go port: `orch claude-exec` matches `bin/claude-exec.mjs` byte for byte when both read the same
  registry (argv passed to `claude`, stdout JSON, `[claude]` progress lines, exit codes, run
  records, incident lines), lines split as Node's readline splits them (U+2028 and U+2029 too),
  including trailing bare value keys read as the JS `true` and Node's timer clamp. Documented
  differences: a bare `--prompt` is refused with exit 2 (PE-8), no Node warnings (PE-9), claude is
  killed 10 s after a signal it ignores (PE-10), SIGTERM/SIGINT are forwarded to claude and the run
  is still reported (PE-11), the post-run usage refresh runs as a detached, bounded `orch usage
  --refresh` that orch does not wait for, so it completes where the JS one exits first (PE-12;
  `ORCH_NO_POST_RUN_REFRESH=1` turns it off for the parity harness), and U+FFFD where a UTF-16 cut leaves a lone surrogate (PE-1). A `--prompt-file`
  that cannot be read prints Node's fs message (ENOENT, EISDIR, EACCES) and exits 2, as the JS does. The new `bridges`
  parity group replays the captured claude stream-json fixtures through a scripted fake and runs
  in CI.
- Contract: no sleep-waiting while independent work exists; re-read the mailbox between waits.
- Go port: `orch codex-exec` and `orch opencode-exec` match `bin/codex-exec.mjs` and
  `bin/opencode-exec.mjs` byte for byte (argv, the prompt on codex's stdin, environment, stdout
  JSON, `[codex]`/`[opencode]` progress lines, exit codes, run records, incident lines), latent
  JS defects included (`--approve-for-me` with `workspace-write`, opencode's ignored `--help` and
  `--effort`). PE-8 to PE-11 now cover all three bridges, PE-12 (the detached post-run refresh)
  claude and codex; new PE-13: codex exiting without reading its prompt is reported as a failed
  run where the JS may die of `write EPIPE`. A NUL byte in a bridge's argv fails as Node's spawn
  does. The `bridges` parity group gains `codex` and `opencode` subgroups over scripted fakes.
- Go port: `orch worker status|stop` matches `bin/worker.mjs` byte for byte (stdout, exit codes,
  lane state and lock files read the same way), with new PE-14: `stop` signals only a stored pid
  of at least 2, where the JS would SIGINT every process of the user for pid `1` or `true`.
  New `worker` parity group, run in CI.
- Go port: foreground `orch worker start` matches `bin/worker.mjs start` (stdout with the relayed
  bridge progress, stderr, exit codes, lane state and lock, mailbox, incidents) and runs
  `orch <harness>-exec` as its bridge. PE-10, PE-11 and PE-13 are extended to the worker: it never
  kills its bridge and waits at most 10 s for its pipes, records the Go bridge's report of an
  interrupted run, and survives a bridge that never reads its prompt where the JS dies of
  `write EPIPE`.
- Go port: `orch worker start --detach` matches `bin/worker.mjs`: the synchronous lane-locked
  refusal `flip.mjs` relies on (stderr, exit 3, stdout empty), stale-lock removal, the 0600 run log,
  the runner re-run without `--detach` in a session of its own, and keep-awake (`caffeinate` /
  `systemd-inhibit`, `runtime.keep_awake`), printing `{lane, runner_pid, log_path, run_id,
  keep_awake}`. The `worker` parity group gains multi-step detached cases (settle, a stopped
  running worker under PE-11) over new `which`, `caffeinate` and `systemd-inhibit` fakes.
- Go port: `orch flip decide|pause|gate|status` matches `bin/flip.mjs` byte for byte (stdout,
  stderr, exit codes, `.orchestrator/flip.json`, mailbox), every `flip.json` shape included
  (`gate` on a number, string or `true` dies of the JS TypeError; a bare `--reason` on `gate`
  posts no message, as in the JS). New PE-15: `decide` reads `flip.json`'s `at` in the ECMAScript
  date formats only, so a hand-written legacy date never holds back a hand-back. New `flip`
  parity group, run in CI.
- Go port: `orch flip run` matches `bin/flip.mjs run`: `.git/info/exclude` (lines split as the JS
  multiline regex splits them), the continue brief, `flip.json` and the handoff message, then
  the conductor launched as `orch worker start --detach` (so a Go-driven flip runs the conductor
  on the Go worker and bridges), or `pending_launch` when the lane is locked. PE-1, PE-2 and
  PE-10 are extended to its `launcher` text and wait; worktree `.git` files still crash, as in
  the JS. The `flip` parity group gains pending and full launches against the fakes.

## 0.4.7 — 2026-10-19

- Contract: pipelining. A slice waiting for a review the other vendor cannot give yet no longer
  idles the conductor; it starts the next independent slice and queues the review.
- usage: keep the last good reading for 30 minutes when a refresh fails (a 429, say) and show its
  age.
- routing: skip preferences whose tier is missing from the registry (was a TypeError in
  balancing).
- worker: detach checks the lane lock synchronously; the supervisor launches a flip target that
  never started.

## 0.4.6 — 2026-10-18

- routing: the 5-hour window steers routing; a weekly cap blocks a vendor only when red; coding is
  balanced across vendors by 5-hour headroom.
- routing: role resolution uses frontier tiers only on a green weekly posture and routine planning
  falls to Opus 5.5; a `size=hard` planning delegation may still take the first frontier tier that
  is not red (amber or unknown included), Fable before GPT-6 Astra, which is reserved for it.
- codex-exec: a completed turn with no tool calls and a tool-host failure message is a failure
  (`tool_host_unavailable`).

## 0.4.5 — 2026-10-18

- README operations cheat sheet; keep-README-current rule in the contracts and in review.

## 0.4.4 — 2026-10-18

- Autonomy policy (`policy.mjs`; verified slices are pushed automatically), `mailbox.mjs
  reply|inbox`, and `watch.mjs` shows open questions.

## 0.4.3 — 2026-10-18

- Human-gate state (`flip.mjs gate`) that the supervisor never relaunches.
- flip: a pending launch when the lane is still locked; the supervisor performs it after release.
- Headless rule: subagents run in the foreground only; the supervisor resumes a conductor that
  exited without a handoff.
- mailbox: `ack` accepts unique id prefixes; claude-exec no longer records "success" as an error.

## 0.4.2 — 2026-10-18

- No reset grace when starting work (an imminent reset no longer makes a window greener); relaunch
  failed or deferred conductor lanes; session-limit text is a deferral.

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
