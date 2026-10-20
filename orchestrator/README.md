# orchestrator: scripts, state and policy reference

This is the technical reference for the JS runtime: the scripts in `bin/`, the pure logic in
`lib/`, the registry `models.json`, and the files they read and write. For what the project is and
how to use it day to day, start with the [root README](../README.md). The Go port of these scripts
is documented in [go/README.md](../go/README.md); the Claude Code front door in
[claude/README.md](../claude/README.md).

Paths below assume the default install, where this directory is
`~/.config/opencode/orchestrator`. Shell examples use `ORCH=~/.config/opencode/orchestrator/bin`.

## Contents

- [Installed layout](#installed-layout)
- [Scripts](#scripts)
- [Commands, skills and plugin tools](#commands-skills-and-plugin-tools)
- [Roles and routing](#roles-and-routing)
- [Budgets and the usage cache](#budgets-and-the-usage-cache)
- [The conductor loop](#the-conductor-loop)
- [Autonomy policy and human gates](#autonomy-policy-and-human-gates)
- [Flipping between the two doors](#flipping-between-the-two-doors)
- [Workers and the supervisor](#workers-and-the-supervisor)
- [Mailbox and manifests](#mailbox-and-manifests)
- [Project state files](#project-state-files)
- [Incident log and keep-awake](#incident-log-and-keep-awake)
- [Install, update, uninstall and recovery](#install-update-uninstall-and-recovery)
- [Doctor, audit and troubleshooting](#doctor-audit-and-troubleshooting)
- [Security and data access](#security-and-data-access)
- [Tests, verification and limitations](#tests-verification-and-limitations)
- [Design notes](#design-notes)

## Installed layout

```text
~/.config/opencode/            real directory; managed entries link to the checkout
  opencode.jsonc               global config (default agent: conductor)
  AGENTS.md                    operating contract loaded into every session
  agents/*.md                  conductor (primary) + native OpenAI subagents (*-oai)
  commands/*.md                /usage /plan /brief /ship /review /handoff /continue /retro
                               /delegate-claude /delegate-codex /audit-orchestrator
  skills/*/SKILL.md            deep-planning, clean-code, cross-review, handoff-protocol,
                               delegation, orchestrator-audit
  plugins/orchestrator.ts      orch_* tools, budget injection, model sync (js or go runtime)
  orchestrator/
    models.json                SOURCE OF TRUTH: vendors, tiers, roles, thresholds, effort,
                               conductor, runtime, autonomy
    bin/*.mjs                  CLI scripts (below)
    lib/*.mjs                  pure logic: routing, budget, flip, supervise, mailbox, manifest,
                               incident, usage readers, keep-awake
    tests/                     fast unit tests (the doctor runs these)
  install.sh, uninstall.sh     links back to the checkout's installer
  service.json                 opencode runtime file; stays outside the checkout
```

In the checkout, root `tests/` holds the installer matrix and the plugin tests, and `go/` holds the
Go runtime.

## Scripts

Every script is dependency-free Node and has a Go twin, `orch <script>` (see
[go/README.md](../go/README.md)). The first lines of each script are its usage text.

| Script | Usage | What it does |
|---|---|---|
| `kickoff.mjs` | `--brief FILE [--brief FILE2 ...] [--door auto\|claude\|opencode] [--headless] [--unattended] [--cwd DIR] [--dry-run]` | Refresh usage, pick the door, preflight (policy with `--unattended`, clear any human gate without queueing a relaunch, start the supervisor, add `.orchestrator/` to `.git/info/exclude`), then launch the conductor interactively or as the headless `conductor` lane. Both vendors red: records a pause and exits 75. |
| `watch.mjs` | `[--cwd DIR] [--follow] [--interval 20]` | One-screen dashboard: budgets, routing, supervisor, flip state, lanes, open questions with the reply command, human gate, last messages, incidents of the last 24 h, next action from `STATE.md`. |
| `usage.mjs` | `[--refresh] [--json] [--brief]` | Live Claude + Codex usage -> posture -> role routing. |
| `mailbox.mjs` | `post \| list \| ack \| reply \| inbox` | Durable project mailbox (see [below](#mailbox-and-manifests)). |
| `policy.mjs` | `show \| set key=value... \| unattended [--cwd DIR]` | Effective autonomy policy: registry `autonomy` merged with `.orchestrator/policy.json`. |
| `flip.mjs` | `decide --active D \| run --to D [--from D] [--reason R] [--resume ID] [--force] \| pause [--active D] [--reason R] \| gate [--active D] [--reason R] [--question ID] [--clear [--no-relaunch]] \| status [--cwd DIR]` | Move the conductor between doors, record budget pauses and human gates. |
| `worker.mjs` | `start --brief F --harness claude\|codex\|opencode [--lane L] [--model M] [--effort E] ... [--detach] \| status [--lane L] \| stop --lane L` | One bounded brief on one harness as a tracked, lockable, resumable lane. |
| `supervisor.mjs` | `start \| serve \| status \| stop \| ack --event ID [--by NAME] [--cwd DIR]` | Per-project daemon: watches lanes, wakes the coordinator, relaunches conductors. |
| `claude-exec.mjs` | `[--model M] [--effort E] [--permission-mode P] ... [--timeout-min 60] [--force] [--prompt T \| --prompt-file F \| stdin]` | Bounded task on Claude Code (`claude -p`, stream-json) -> JSON result + session id. |
| `codex-exec.mjs` | `[--model M] [--effort E] [--sandbox S] ... [--resume THREAD] [--timeout-min 60] [--force]` | Bounded task on Codex (`codex exec --json`) -> JSON result + thread id. |
| `opencode-exec.mjs` | `[--agent conductor] [--cwd DIR] [--resume SESSION] [--timeout-min 120]` | Bounded task on `opencode run`; used for the headless opencode conductor. |
| `manifest.mjs` | `create --task T --author A [--baseline F] --out F <paths...> \| verify F \| diff A B` | sha256 fingerprints of released files; `verify` exits 1 on any mismatch. |
| `incident.mjs` | `record ... \| list \| report \| share --out F` | Local, redacted orchestrator defect log. |
| `doctor.mjs` | (no arguments) | Health check: binaries, logins, usage feeds, routing, config, plugin, agents, unit tests, recent plugin errors, `orch binary`. |
| `setup.sh` | | Legacy auth/setup helper; not the reversible installer. |

Bridge exit codes: 0 success, 1 failure, 2 usage error, 75 refused because the vendor's budget is
red (override with `--force`, not as routine).

## Commands, skills and plugin tools

| Command | Purpose |
|---|---|
| `/usage` | Refresh usage and explain routing |
| `/plan`, `/brief` | Plan work and write bounded worker briefs |
| `/ship` | Follow the build/review/verify loop |
| `/review` | Request independent review and verification |
| `/delegate-claude`, `/delegate-codex` | Request a bounded task on a specific CLI harness |
| `/continue`, `/handoff` | Resume or write continuation state |
| `/retro` | Inspect recent run/review outcomes and incidents |
| `/audit-orchestrator` | Recheck model catalogs, CLI compatibility and policy sources |

Skills (`skills/`): `deep-planning`, `clean-code`, `cross-review`, `handoff-protocol`,
`delegation`, `orchestrator-audit`.

The plugin registers the `orch` namespace (often displayed as `orch_*`):

| Tool | Purpose |
|---|---|
| `usage_status` | Cached/fresh usage and role routing |
| `delegate` | Route a bounded brief to a CLI bridge, or tell the conductor which native subagent to call |
| `codex_review` | Invoke Codex's review harness |
| `mailbox_post`, `mailbox_list` | Write/read project messages |
| `manifest_create`, `manifest_verify` | SHA-256 release fingerprints and verification |
| `worker_start`, `worker_status` | Detached worker launch and lane state |
| `supervisor` | Start/status/stop/ack supervision |
| `incident` | Record a redacted orchestrator incident for the next audit |

The plugin also injects the live budget into primary agents, syncs native model choices from
`models.json`, and flips the conductor when its model dies on a usage-limit error. The presence of
a tool does not mean its external CLI path has passed end-to-end verification.

## Roles and routing

`models.json` is strict JSON; `$comment` keys document the schema. `vendors` define labels,
windows and tiers; `roles.*.prefer` defines fallback order; `thresholds`, `effort_policy`,
`conductor`, `runtime` and `autonomy` are described in the sections below. These are configured
defaults, not a promise that every model id is available on every account.

| Tier | Anthropic (Claude Code) | OpenAI (Codex / opencode) |
|---|---|---|
| frontier | Fable 5.1 (`high`) | GPT-6 Astra (`high`) |
| strong | Opus 5.5 (`medium`) | GPT-6.1 Sol (`medium`) |
| mid | Sonnet 5.5 (`medium`) | GPT-6.1 Sol (`medium`) |
| cheap | Haiku 5.5 | GPT-6 Luna (`low`) |

| Role | Preferred | Fallback |
|---|---|---|
| Planner | Fable 5.1 while Claude's frontier posture is green; else Opus 5.5 | GPT-6.1 Sol. A `size=hard` planning delegation takes a frontier tier that is not red: Fable, else GPT-6 Astra |
| Coder | Opus 5.5 | GPT-6.1 Sol; it goes first when it has more than 15 points more five-hour headroom |
| Reviewer | Other vendor's mid tier: Sonnet 5.5 or GPT-6.1 Sol | Other vendor's cheap tier; then same-vendor mid tier, explicitly marked |
| Verifier | Sonnet 5.5 | GPT-6 Luna; then Haiku 5.5 |
| Researcher | Sonnet 5.5 | GPT-6 Luna |
| Explorer / utility | Haiku 5.5 | GPT-6 Luna |

**Effort** is never pinned to a model. An explicit `effort` wins; otherwise `effort_policy` picks
it from the task `size` given to `delegate` (`trivial`, `small`, `medium` (default), `large`,
`hard`). Planner: medium -> high, hard -> max. Coder: medium -> medium, large -> high, hard ->
xhigh. The tier's own `variant`/`effort` is the last fallback. Native subagents run at their
model's default effort, so large and hard tasks go through the CLI bridges, where the policy effort
applies.

**Native vs bridge.** Native OpenAI subagents are `architect-oai`, `coder-oai`, `reviewer-oai`,
`verifier-oai` and `researcher-oai`. When routing selects a native agent, `delegate` returns which
subagent to call instead of launching the CLI bridge. Claude subscription work always goes through
the Claude Code bridge (`claude -p`): opencode has no native Claude subscription sign-in, and
Anthropic API-key access is a separate, unrelated option.

**Hard-planning override.** A `delegate` call with `role: planner` and `size: hard` that names no
harness (nor a `gpt-*`, `o<digit>...` or `claude*` model, which implies one) replaces a non-frontier
planner with the first frontier tier whose vendor is available and whose frontier posture is not
red (amber or unknown included), Anthropic (Fable) before OpenAI (Astra). With an explicit harness
the override does not apply: the routed role is kept if it is on that harness's vendor, otherwise
that vendor's registry tier for the role is used, whatever its posture; the bridges' own budget
check still applies.

To change policy, edit `models.json`, keep it valid JSON, run the unit tests and reload opencode.
The plugin syncs native model choices from the registry; audit static agent/config fallbacks too
when changing model ids.

## Budgets and the usage cache

`usage.mjs` reads the Claude OAuth usage endpoint (session %, weekly %, Fable weekly %) and the
Codex app-server (`account/rateLimits/read`: 5-hour %, weekly %).

- Thresholds (`thresholds`): **amber at 60% used, red at 85%**, judged on the raw percentage. An
  imminent reset never makes a window greener; a reset within `soon_minutes` (20) is only reported
  as resetting soon. A rate-limit lock makes the window red.
- The **five-hour window** (Claude session, Codex 5-hour) drives routing. A weekly cap blocks its
  vendor only when red.
- A frontier tier (Fable) is used only while that vendor's frontier posture is green; Claude's
  frontier posture also counts the Fable weekly window.
- **Balancing:** when both vendors can serve the coder, verifier, researcher, explorer or utility
  role at an equivalent tier, the one with more five-hour headroom wins if it leads by more than
  `balance_margin` (15) points; otherwise registry order decides.
- **Red** routes the role to the other vendor, or pauses. The bridges refuse a known-red budget
  with exit 75 unless `--force`. This is advisory, not a hard quota: bridge checks proceed when
  usage is unknown.

The usage cache is `~/.local/state/opencode/orchestrator/usage.json` (not retargeted by
`XDG_STATE_HOME`). A reading is reused for 5 minutes, or until a routing window it holds resets. If
a refresh fails, the last good reading is kept, marked `last_good_at` and `read_error`, and retried
every 5 minutes rather than on every read. Both runtimes share this cache.

## The conductor loop

1. **Orient:** read project `AGENTS.md`, `.orchestrator/STATE.md`, the mailbox and budgets.
2. **Plan:** a planner writes `.orchestrator/plans/<slug>.md` with bounded briefs: acceptance
   criteria and explicit scope.
3. **Build:** a coder implements one slice, runs checks, writes a report and hashes the files it
   touched into a manifest.
4. **Review:** the other vendor reviews that fingerprint; the author fixes findings and releases a
   new manifest. Nobody approves their own work. If the other vendor is unavailable, the unmet
   cross-vendor gate is disclosed, not papered over with same-vendor review.
5. **Verify:** the verifier runs the real checks. Unrun checks stay "not run"; unmet criteria stay
   "partial".
6. **Record:** update `STATE.md` (the exact next action) and the mailbox so any session can resume.

## Autonomy policy and human gates

`models.json` -> `autonomy` says what a conductor may do without asking; a project overrides it in
`.orchestrator/policy.json` (same keys).

| Key | Default | Meaning |
|---|---|---|
| `push_to_main` | `auto` | Push a verified slice (review pass + checks green) |
| `release`, `delete_code`, `spend`, `deploy` | `ask` | Post a `question`, keep working on independent slices |
| `force_push` | `never` | Cannot be set to anything else |
| `product_questions` | `continue_with_assumption` | Record the assumption in `STATE.md` and the report, post the question, continue |

`policy.mjs set` refuses `key=never` except for `force_push`. `policy.mjs unattended` (also
`kickoff --unattended`) sets `push_to_main=auto` and `product_questions=continue_with_assumption`.

A headless conductor that reaches a human gate with nothing independent left posts the question and
runs `flip.mjs gate --active <door> --reason "..." --question <id>`. The supervisor will not
relaunch while the gate is set. `kickoff` and `flip.mjs run` clear it and start a conductor.
`flip.mjs gate --clear`, and `mailbox.mjs reply` to the gate's question (its full id or a prefix of
it, as recorded by `--question`), clear it and record `pending_launch` for the gated door, which the
supervisor launches once the conductor lane is idle and unlocked (within a minute); `gate --clear`
then prints `{"awaiting_human":false,"relaunch":"<door>"}`. Clearing when no gate is set queues
nothing, and `gate --clear --no-relaunch` (what kickoff runs) only removes the marker. `pause`
is for budgets only. To pause a running project by hand, set a gate and then `worker.mjs stop --lane
conductor`; a stopped (`interrupted`) lane without a gate is relaunched by the supervisor.

Prompts and permission rules are not a security sandbox. `opencode.jsonc` separately sets the
opencode shell permission for `git push*` to ask and denies `git push --force*`.

## Flipping between the two doors

One conductor drives a project at a time. `models.json` -> `conductor.home` names the default door
(`opencode`); `green_grace_minutes` and `flip_cooldown_minutes` are both 10.

| Situation at a brief boundary | Active conductor |
|---|---|
| its own vendor is red | flips to the other door (also triggered when its model dies on a usage-limit error) |
| it is the away door and home is stably green (green for `green_grace_minutes`) | hands back home |
| it is the away door, home still red | keeps going |
| both red | writes the remaining briefs, runs `flip.mjs pause`, stops; the supervisor relaunches at the first reset, home door first if it comes back first |

`flip.mjs run --to <door>` adds `.orchestrator/` to `.git/info/exclude`, writes
`.orchestrator/briefs/conductor-continue.md`, replaces `flip.json` (clearing `paused` and
`awaiting_human`), posts a `handoff`, and starts the target conductor headless as worker lane
`conductor` (`--breadth broad`; `--permission-mode auto` for claude). The lane lock means one
driver. If the lane is still locked, usually by the calling conductor, `flip.json` gains
`pending_launch` and the supervisor launches the target once the lock is released. Flips never
happen mid-brief, so a hand-back is delayed by at most one brief.

## Workers and the supervisor

**Workers.** `worker.mjs start` runs one brief on one harness. State lives in
`.orchestrator/workers/<lane>.json`, the lock in `<lane>.lock`, the log in
`.orchestrator/logs/<lane>-<run>.log`. `--detach` launches a kept-awake runner. The bridge timeout
for a worker is 120 minutes unless `--timeout-min` says otherwise. On completion the worker posts
`release`, `blocked` or `deferred` (exit 75 / budget red) to the mailbox. `stop --lane L` sends
SIGINT to the lane's process group and the lane ends `interrupted`. From the conductor,
`worker_start` accepts `brief`, `harness`, optional `lane`, `model`, `effort`, `resume`, `breadth`.
Workers are instructed to publish a report and a mailbox release; check the actual state and
messages rather than assuming every successful run did.

**Supervisor.** One per project (`supervisor.lock`), started by `kickoff` or `supervisor.mjs
start`. It ticks every 5 seconds: observes every lane, posts events to the mailbox (kind `wake`)
first and then tries the wake channels, re-dispatches budget-deferred lanes after the reset, and
once a minute evaluates the conductor:

- `awaiting_human` in `flip.json`: do nothing.
- `pending_launch`: launch the target once the lane is idle and unlocked.
- an idle conductor lane that ended `failed`, `deferred` or `interrupted`, or `finished` without a
  handoff, or a paused project: relaunch through `flip.mjs run`, resuming the previous session when
  the door is the same; a flip or hand-back decision picks the other door.

Stopping the supervisor (`supervisor.mjs stop`) does not stop workers. `ack --event ID` records
receipt of a reminder, not acceptance.

**Wake channels** are configured in the project's `.orchestrator/supervisor.json`:

```json
{
  "wake": [
    { "type": "opencode-run", "session": "<session-id>" },
    { "type": "codex-queue", "thread": "<thread-id>" },
    { "type": "claude-resume", "session": "<session-id>" }
  ],
  "auto_redispatch_deferred": false
}
```

`command` channels can run an explicit `argv` array, so treat the file as executable
configuration. Wake delivery is best-effort, not exactly-once. Deferred-worker redispatch is on
when `auto_redispatch_deferred` is omitted; the example turns it off to avoid surprise work after a
reset.

## Mailbox and manifests

The mailbox is append-only JSONL under `.orchestrator/mailbox/`. Kinds in use include `note`,
`question`, `release`, `blocked`, `deferred`, `handoff` and `wake`.

```sh
node $ORCH/mailbox.mjs post --from A [--to B] [--kind release] --subject S [--body text|-] [--ref path]... [--thread id]
node $ORCH/mailbox.mjs list [--to B] [--unacked-by B] [--kind k] [--since iso] [--limit n] [--json]
node $ORCH/mailbox.mjs ack --id ID --by B [--note text]
node $ORCH/mailbox.mjs inbox                       # open questions for you, each with its reply command
node $ORCH/mailbox.mjs reply <id-or-prefix> "answer"  # threaded note from "human" + ack
```

`--root DIR` or `ORCHESTRATOR_PROJECT` selects the project; the default is the current directory.

Manifests are sha256 fingerprints of the files a slice released:

```sh
node $ORCH/manifest.mjs create --task T --author A --out .orchestrator/manifests/T.json <paths...>
node $ORCH/manifest.mjs verify .orchestrator/manifests/T.json   # exit 1 on any mismatch
node $ORCH/manifest.mjs diff before.json after.json
```

A manifest identifies released bytes; it does not prove a review happened or that the bytes are
safe.

## Project state files

Each work project gets a private `.orchestrator/` (kickoff and flip add it to
`.git/info/exclude`):

| Path | Contents |
|---|---|
| `STATE.md` | Continuation cursor: status, decisions, exact next action |
| `plans/`, `briefs/` | Plans and bounded briefs (`conductor-kickoff.md`, `conductor-continue.md` are generated) |
| `reports/`, `reviews/` | Coder reports and review findings |
| `manifests/` | sha256 manifests |
| `mailbox/` | JSONL messages and acks |
| `runs/` | One JSON record per bridge run (prompt excerpt, result, session/thread id) |
| `workers/` | Lane state and locks |
| `logs/` | Worker and supervisor logs |
| `flip.json` | Last flip, `paused`, `awaiting_human`, `pending_launch` |
| `policy.json` | Autonomy overrides |
| `supervisor.json` | Wake channels (optional) |
| `supervisor-state.json`, `supervisor-acks.json`, `supervisor.lock`, `supervisor.stop` | Supervisor state |

Per-user state lives in `~/.local/state/opencode/orchestrator/` (`usage.json`,
`incidents.jsonl`). Installer receipts and backups are separate, under
`$XDG_STATE_HOME/opencode-orchestrator` (default `~/.local/state/opencode-orchestrator`).

## Incident log and keep-awake

Orchestrator defects (tool/contract mismatches, misroutes, budget surprises, bridge crashes, flips,
and corrections the human gives about the workflow) are appended, redacted, to
`~/.local/state/opencode/orchestrator/incidents.jsonl` by the bridges, worker, supervisor, plugin
and the `orch_incident` tool. `incident.mjs report` groups them by fingerprint; `/retro` and the
audit skill turn repeats into fixes. Nothing is uploaded: `incident.mjs share --out file.json`
writes a sanitized bundle you can attach to an issue by hand.

Detached workers and the supervisor are wrapped in `caffeinate -i -s` on macOS
(`systemd-inhibit --what=idle:sleep` on Linux), so long runs survive idle sleep; the assertion ends
with the process. Closing a laptop lid still sleeps the machine unless it runs in clamshell mode
(external display and power). Disable with `runtime.keep_awake: false` in `models.json`.

## Install, update, uninstall and recovery

**Requirements.** macOS or Linux, Bash 3.2+, Node 22+, Git, opencode v2, Claude Code and Codex on
`PATH`, each logged in. Config and installer state on the same filesystem (atomic renames). The
runtime expects `~/.config/opencode`; a nondefault `XDG_CONFIG_HOME` is rejected. Optional: Go
1.25+ to build `orch` locally. Release checks used macOS, Node v26.5.1, opencode v2.0.32, Claude
Code 2.1.287 and codex-cli 0.162.1; Linux and Node 22 are supported assumptions, not exercised
release environments. No npm install is needed to run the orchestrator.

**Install.** `./install.sh [--copy] [--force] [--skip-binary] [--no-reload]`. The default creates a real
`~/.config/opencode` and links `AGENTS.md`, `opencode.jsonc`, `agents`, `commands`, `skills`,
`plugins`, `orchestrator`, `install.sh` and `uninstall.sh` to the checkout, so runtime files such
as `service.json` stay out of the checkout; keep the checkout at a stable path. `--copy` copies the
managed entries instead (excluding history, dependencies, logs and runtime state, and refusing
symlinks inside them). Any existing target is refused without `--force`. Force does not merge: it
moves the old target, with its custom config, runtime files and `node_modules`, into private backup
storage. Stop opencode sessions, the background service, supervisors and workers first. The
installer then runs `opencode reload` and the installed doctor; a reload or doctor failure exits
nonzero and keeps the target, receipt and backup for recovery. `--no-reload` skips both (for
hermetic tests and scripted installs) and says so; the receipt still becomes active although
neither reload nor doctor ran, so run `opencode reload` and the doctor yourself afterwards.
The `orch` binary step is described in [go/README.md](../go/README.md#install). A release download
gives up after 20 s without a connection, 60 s below 1 KiB/s or 15 min in total, and fails with
`Download failed` and the usual recoverable state. The 15-minute cap holds even while bytes flow,
so a healthy link slower than about 20 KB/s can hit it; build `orch` locally with Go instead, or
retry on a faster link.

Installer state lives in `$XDG_STATE_HOME/opencode-orchestrator` (default
`~/.local/state/opencode-orchestrator`), private (0700, receipts 0600); state inside the checkout or
config is rejected. There is one active receipt at a time.

**Update** (symlink install): pull in the checkout, run both test suites, rebuild `orch`, reload,
check health:

```sh
git pull
node --test orchestrator/tests/*.test.mjs
node --test tests/*.test.mjs
(cd go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ~/.local/bin/orch ./cmd/orch)
opencode reload
node ~/.config/opencode/orchestrator/bin/doctor.mjs
```

Copy installs do not follow Git: uninstall, update the checkout, reinstall with `--copy` (and
`--force` if the restored previous config occupies the target).

**Upgrading from 0.4.x.** A 0.4.x receipt has no `orch`, and a reinstall is not migrated. Either
`./uninstall.sh` then `./install.sh`, so the receipt records the binary, or build `orch` by hand into
`~/.local/bin` (the `go build` line above). A hand-built `orch` is not tracked: uninstall leaves it,
and a later `./install.sh` refuses while it exists (remove it, set `ORCH_BIN_DIR`, or pass
`--skip-binary`).

**Uninstall.** `./uninstall.sh` (or `bash ~/.config/opencode/uninstall.sh`). It verifies the
receipt, target identity, managed contents and backup identity, restores the previous config when
one existed, and archives the removed installation and receipt under a private `uninstalled-*`
directory. It never recursively deletes. A recorded `orch` is moved into the archive as
`removed-binary` (left in place, with a message, when it is not a regular file, was never placed by
an interrupted install, or the archive already holds one). Repeating uninstall with no receipt is a
no-op. It needs only Bash and Node and does not run service commands; reload opencode yourself.

**Recovery.** Changed or unrecognized targets, missing or replaced backups and invalid receipts are
refused. Git updates behind a symlink install are expected; local content or mode changes in a
copy install are detected. For an "Installed config changed" refusal:

1. Stop affected processes. Back up the current config and installer state privately, outside the
   checkout; do not edit the receipt or fingerprint.
2. Keep the installed root directory in place. Inspect changes against the version originally
   installed, not an updated checkout.
3. Move only identified local additions (such as new dependency files) aside. Restore known managed
   entries to their installed state, preserving changed content first, then retry uninstall.
4. If the installed state cannot be established, stop for manual recovery. Do not delete unfamiliar
   files or bypass ownership checks.

An interrupted operation can leave `operation.lock`; confirm no installer is running before
reconciling it. Do not delete an opaque backup because a command failed.

## Doctor, audit and troubleshooting

```sh
node $ORCH/doctor.mjs
node $ORCH/usage.mjs --refresh
```

The doctor checks binaries, logins and usage feeds, routing, config/plugin/agents, the fast unit
tests and recent plugin errors. Its `orch binary` row runs `orch version` on `ORCH_BIN`, else
`~/.local/bin/orch`, else the first `orch` on `PATH`, and only warns when none is found. Expect
`OK` rows, including `OK unit tests — pass N`. `WARN` can mean unclear login or recent plugin
errors; any `FAIL` exits nonzero. An all-OK doctor does not test a delegated model turn, install
reversal or a long unattended run.

| Symptom | Check |
|---|---|
| Missing binary/login | Install the CLI and use its own login flow; never paste tokens into config |
| Claude usage unavailable | Claude Code login and keychain/credential access |
| Codex usage unavailable | Codex login and `codex app-server` compatibility |
| Routing paused | `/usage` and reset times; both vendors may be constrained |
| Plugin/agents missing | Managed links, `opencode plugin list`, `opencode debug agents`, then `opencode reload` |
| Installer refusal | Keep the receipt/backup, reconcile, follow [recovery](#install-update-uninstall-and-recovery) |
| Doctor unit-test row fails | `node --test orchestrator/tests/*.test.mjs` in the checkout (the doctor has a 20 s limit) |
| Codex bridge argument error | See [limitations](#tests-verification-and-limitations) |

`/audit-orchestrator` (skill `orchestrator-audit`) re-verifies model ids, prices, limits and CLI
flags against official sources and live catalogs, updates `models.json`, and calls for tests and
smoke runs. Smoke runs spend quota. Some of its examples assume the global config is a Git repo;
with managed links, do Git work in the checkout.

## Security and data access

- Claude usage: checks legacy opencode Anthropic OAuth files, then the macOS keychain entry
  `Claude Code-credentials` and a Claude Code credentials file, and sends that bearer token to
  `https://api.anthropic.com/api/oauth/usage`. The token is not written to the usage cache.
- Codex usage: starts local `codex app-server` over stdio (`account/read`,
  `account/rateLimits/read`). Codex owns its auth and network traffic.
- Provider availability checks read local opencode credential metadata or CLI login status. The
  usage cache holds account plan/tier/limit information; keep it private.
- Delegation sends briefs and repository context to the selected model through opencode or a vendor
  CLI, and those harnesses can run tools, MCP servers, shell commands and web requests. There is no
  promise that all traffic goes only to Anthropic and OpenAI.
- Run records, logs, mailbox, reports and backups can contain private content. Ignoring files in
  Git is not access control. Never commit credentials, runtime state or recovery backups.

## Tests, verification and limitations

```sh
node --test orchestrator/tests/*.test.mjs   # fast unit suite (the doctor runs it)
node --test tests/*.test.mjs                # installer matrix + plugin tests, temp HOME, stubbed CLIs
```

The Go checks are in [CONTRIBUTING.md](../CONTRIBUTING.md) and [go/README.md](../go/README.md#checks).
`tests/plugin-orch-parity.test.mjs` builds `orch` into a temp dir (or uses `ORCH_TEST_BIN`) and
skips without either; under a temp `HOME`, point `GOCACHE` at a shared cache.

- **Verified:** the unit suite (routing, flip, supervision, mailbox, incident, usage cache), the
  installer and plugin suites in both runtimes against stubs, the Go build/vet/gofmt/tests and the
  JS-vs-Go parity harness in CI on macOS and Linux. Installer tests never touch a real config or
  authenticate a model.
- **Known issue:** on codex-cli 0.162.1, `codex-exec` passes `--approve-for-me` with the default
  `workspace-write` sandbox, which that CLI rejects (exit 2) before a model turn. Do not assume
  `/delegate-codex` or Codex-backed workers work because the usage feed passes. Native OpenAI
  subagents are a separate path.
- **Not verified:** a real install over an existing user config, Linux/Node 22 execution, every
  bridge/review combination, storage or power failures, long unattended runs on large repositories.

## Design notes

Policy lives in one JSON registry, and the routing logic is pure, so it is testable without account
access. Small CLI adapters isolate each vendor's interface, while project-local files carry
resumable state across harnesses. Managed links separate published code from global runtime
writes; private receipts and archive-first removal favour recoverability over aggressive cleanup.
Budget-aware routing and cross-vendor review are guardrails, not substitutes for human approval and
real tests.
