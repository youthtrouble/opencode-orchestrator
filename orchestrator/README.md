# orchestrator: commands, state and policy reference

This is the technical reference for `orch`, the registry `models.json`, and the files they read
and write. The JS runtime is removed in 0.6.0. For what the project is and
how to use it day to day, start with the [root README](../README.md). The Go runtime
is documented in [go/README.md](../go/README.md); the Claude Code front door in
[claude/README.md](../claude/README.md).

Paths below assume the default install, where this directory is
`~/.config/opencode/orchestrator`. Commands require `orch` on `PATH`, including in agent environments.

## Contents

- [Installed layout](#installed-layout)
- [Commands](#commands)
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
                               /delegate-claude /delegate-codex /audit-orchestrator /away
  skills/*/SKILL.md            deep-planning, clean-code, cross-review, handoff-protocol,
                               delegation, orchestrator-audit
   plugins/orchestrator.ts      orch_* tools, budget injection, model sync (orch only)
  orchestrator/
    models.json                SOURCE OF TRUTH: vendors, tiers, roles, thresholds, effort,
                               conductor, runtime, autonomy
     bin/setup.sh               legacy auth/setup helper
     README.md, CHANGELOG.md     reference and implementation history
  install.sh, uninstall.sh     links back to the checkout's installer
  service.json                 opencode runtime file; stays outside the checkout
```

In the checkout, root `tests/` holds the installer matrix and the plugin tests, and `go/` holds the
Go runtime.

## Commands

Every command is an `orch <cmd>` subcommand (see [go/README.md](../go/README.md)).
The plugin uses the same building blocks as `orch <cmd>`.

| Command | Usage | What it does |
|---|---|---|
| `orch kickoff` | `--brief FILE [--brief FILE2 ...] [--door auto\|claude\|opencode] [--headless] [--unattended] [--cwd DIR] [--dry-run]` | Refresh usage, preflight, choose a door and launch. Dry-run changes nothing in the project but refreshes usage. Both red: save the prompt, pause and exit 75; exit 1 if the supervisor or pause fails. |
| `orch watch` | `[--cwd DIR] [--follow] [--interval 20]` | Dashboard: budgets, routing, supervisor, flip, lanes, questions, messages, incidents and next action. |
| `orch usage` | `[--refresh] [--json] [--brief]` | Live Claude + Codex usage, posture and routing. |
| `orch mailbox` | `post \| list \| ack \| reply \| inbox` | Durable project mailbox (see [below](#mailbox-and-manifests)). |
| `orch policy` | `show \| set key=value... \| unattended [--cwd DIR]` | Registry autonomy merged with project overrides. |
| `orch flip` | `decide --active D \| run --to D \| pause \| gate [--clear [--no-relaunch]] \| status [--cwd DIR]` | Move between doors, record budget pauses and human gates; full flags in the Go command reference. |
| `orch worker` | `start --brief F --harness claude\|codex\|opencode [--lane L] ... [--detach] \| status [--lane L] \| stop --lane L` | Tracked, lockable, resumable worker lane. |
| `orch supervisor` | `start \| serve \| status \| stop \| ack --event ID [--by NAME] [--cwd DIR]` | Watch lanes, wake the coordinator and relaunch conductors. |
| `orch claude-exec` | `[--model M] [--effort E] ... [--prompt T \| --prompt-file F \| stdin]` | Bounded Claude Code task, JSON result and session id. |
| `orch codex-exec` | `[--model M] [--effort E] [--sandbox S] ... [--resume THREAD]` | Bounded Codex task, JSON result and thread id. |
| `orch opencode-exec` | `[--agent conductor] [--cwd DIR] [--resume SESSION] [--timeout-min 120]` | Bounded `opencode run` task; headless opencode conductor. |
| `orch manifest` | `create --task T --author A [--baseline F] --out F <paths...> \| verify F \| diff A B` | sha256 fingerprints; verify exits 1 on mismatch. |
| `orch session` | `beat --door opencode\|claude [--state S] [--now TEXT] [--slice ID] [--progress N/M] [--session ID] [--hook] [--cwd DIR] \| show [--cwd DIR]` | Conductor heartbeat; hook mode is described under project state. |
| `orch incident` | `record ... \| list \| report \| share --out F` | Local, redacted defect log. |
| `orch doctor` | (no arguments) | Health check, including the running binary and `orch on PATH`. |
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
| `/away [--dry-run] [brief]` | Hand this session to one headless, unattended conductor lane and stop (the "Going away" rule in `AGENTS.md`); `--dry-run` only shows the door and prompt |

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

`orch usage` reads the Claude OAuth usage endpoint (session %, weekly %, Fable weekly %) and the
Codex app-server (`account/rateLimits/read`: 5-hour %, weekly %).

- Thresholds (`thresholds`): **amber at 60% used, red at 85%**, judged on the raw percentage. An
  imminent reset never makes a window greener; a reset within `soon_minutes` (20) is only reported
  as resetting soon. A rate-limit lock makes the window red.
- The **five-hour window** (Claude session, Codex 5-hour) drives routing. A weekly cap blocks its
  vendor only when red.
- Weekly-only Codex plans show `5h n/a`; the weekly window alone drives routing and flip at every
  level (amber steers, red blocks), headroom and reset times. Unreadable data stays unknown.
- A frontier tier (Fable) is used only while that vendor's frontier posture is green; Claude's
  frontier posture also counts the Fable weekly window.
- **Balancing:** when both vendors can serve the coder, verifier, researcher, explorer or utility
  role at an equivalent tier, the one with more five-hour headroom wins if it leads by more than
  `balance_margin` (15) points; otherwise registry order decides.
- **Red** routes the role to the other vendor, or pauses. The bridges refuse a known-red budget
  with exit 75 unless `--force`. This is advisory, not a hard quota: bridge checks proceed when
  usage is unknown.

### OpenAI credits

Codex also reports ChatGPT credits (`credits`: `hasCredits`, `unlimited`, `balance`) and banked
resets (`rateLimitResetCredits.availableCount`, cached as `reset_credits_available`). Codex spends
credits by itself once the plan limit is reached (ordinary usage refused). Banked resets are
promotional resets of the usage window, not money; nothing here redeems them. These are the
ChatGPT-login credits: Platform API credits are billed separately and only to Codex run with an API
key, which this setup does not use.

- **Shown, not steering:** the Codex line of `orch usage` (and so of `orch watch` and the injected
  `<orchestrator_budget>`) ends with the fact when Codex reported credits or banked resets, for
  example `...; weekly 100% [red] resets ...; credits 215.68 left; plan limit reached; 3 banked
  resets (window resets, not money).` `orch usage --json` adds `openai_credits` (`balance`,
  `has_credits`, `unlimited`, `plan_limit_reached`, `banked_resets`, or `null`) and
  `openai_credits_policy` (`mode`, `floor`). `orch doctor` adds an `OpenAI credits` row with the
  policy, and the Work Trains page an `openai credits ...` chip. The window's colour never changes.
- **Spent only when the owner says so:** policy `openai_credits` is `off` by default: a red Codex
  window means no OpenAI work, credits or not. With `openai_credits=on`, an OpenAI role whose window
  is red while Codex refuses ordinary usage may still route to OpenAI when credits are unlimited or
  the balance is above `openai_credits_floor` (default 0). Such a role is marked `credit_funded`,
  always runs through `codex-exec` (never natively in opencode), is never chosen to balance headroom,
  and never uses a frontier tier. `orch codex-exec` is the only bridge that draws credits.
  Before it draws, its gate re-reads usage and decides again on that fresh reading, so the floor is
  checked against the balance actually left; if the re-read fails, it refuses. Its run record gains
  `credits: {before, after}` (the balance at the gate and from a fresh reading after the run;
  `after` is `null` when that reading failed). `opencode-exec`, `codex_review` and native subagents
  still refuse a red window.
  There is no `security-only` mode: delegations carry no security/auth marker for routing to read.
  A `--force` Codex run past the plan limit spends credits regardless of the policy and records
  nothing about them. The policy gates only runs the orchestrator starts: Codex itself draws credits
  once the plan limit is hit, so a run that starts on an unreadable usage feed (the gate then
  proceeds) or on a reading that turned red since can spend credits with the policy off.

The usage cache is `~/.local/state/opencode/orchestrator/usage.json` (not retargeted by
`XDG_STATE_HOME`). A reading is reused for 5 minutes, or until a routing window it holds resets. If
a refresh fails, the last good reading is kept, marked `last_good_at` and `read_error`, and retried
every 5 minutes rather than on every read. The cache remains compatible with pre-0.6.0 state.

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
| `release`, `delete_code`, `spend`, `deploy` | `ask` | Post a `question` (with `--recommended` and `--blocking`), keep working on independent slices |
| `force_push` | `never` | Cannot be set to anything else |
| `product_questions` | `continue_with_assumption` | Record the assumption in `STATE.md` and the report, post the question (with `--recommended` and `--blocking`; an interactive conductor also posts the questions it asks in chat), continue |
| `openai_credits` | `off` | `on` lets OpenAI roles whose plan limit is reached run on ChatGPT credits through `codex-exec` (see [OpenAI credits](#openai-credits)) |
| `openai_credits_floor` | (unset: 0) | With `openai_credits=on`, stop drawing credits once the balance is at or below this number |

`orch policy set` refuses `key=never` except for `force_push`, an `openai_credits` other than `off`
or `on`, and an `openai_credits_floor` that is not a number, 0 or more. `orch policy unattended` (also
`kickoff --unattended`) sets `push_to_main=auto` and `product_questions=continue_with_assumption`.

A headless conductor that reaches a human gate with nothing independent left posts the question and
runs `orch flip gate --active <door> --reason "..." --question <id>`. The supervisor will not
relaunch while the gate is set, and `orch flip decide` answers `gate`: a headless conductor updates
`STATE.md` and stops, while an interactive conductor reports the gate and keeps working with the human.
`orch kickoff` and an explicit `orch flip run` without `--unless-gated` clear a readable gate and
start a conductor.
`orch flip gate --clear`, and `orch mailbox reply` to the gate's question (its full id or a prefix of
it, as recorded by `--question`), clear it and record `pending_launch` for the gated door, which the
supervisor launches once the conductor lane is idle and unlocked (within a minute); `gate --clear`
then prints `{"awaiting_human":false,"relaunch":"<door>"}`. Clearing when no gate is set queues
nothing, and `gate --clear --no-relaunch` (what kickoff runs) removes the marker and any queued launch or
pause. `pause`
is for budgets only. To pause a running project by hand, set a gate and then `orch worker stop --lane
conductor`; a stopped lane without a gate is relaunched by the supervisor.

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
| both red | writes the remaining briefs, runs `orch flip pause`, stops; the supervisor relaunches at the first reset, home door first if it comes back first |

`orch flip run --to <door>` adds `.orchestrator/` to the repository's `info/exclude` (`.git/info/exclude`, or for a `.git` file, the linked worktree's common dir or the submodule's git dir), writes
`.orchestrator/briefs/conductor-continue.md` (unless the record it replaces is a pause that names a
brief, as a both-red kickoff's does, or a pending launch that kept one, and that file exists: then it
launches that brief), replaces `flip.json` (clearing `paused` and
`awaiting_human`), posts a `handoff`, and starts the target conductor headless as worker lane
`conductor` (`--breadth broad`; `--permission-mode auto` for claude). The lane lock means one
driver. If the lane is still locked, usually by the calling conductor, `flip.json` gains
`pending_launch` (keeping that brief) and the supervisor launches the target once the lock is released. Flips never
happen mid-brief, so a hand-back is delayed by at most one brief.

The supervisor uses `orch flip run --to <door> --unless-gated` for every automatic conductor
relaunch. The command checks `awaiting_human` under the flip-state write lock; if set, it prints
`{"flipped":false,"reason":"awaiting human"}` and exits 1 without changing the state, continue
brief, git exclude or mailbox, or launching a worker. A gate installed after the supervisor's
earlier read therefore survives. Explicit human runs without the flag and real kickoff retain
their intentional gate-clearing behavior for readable state.

**Unreadable flip state.** Only a missing `.orchestrator/flip.json` means no recorded state.
Invalid JSON or a read error makes `orch flip decide --active <door>` print
`{"action":"gate","reason":"flip state unreadable: <code>; fix or remove .orchestrator/flip.json"}`
and exit 0 without collecting usage. `flip gate`, `gate --clear`, `pause` and `run` refuse with
exit 1, naming the file and leaving its contents unchanged. `mailbox reply` also exits 1 if
the gate cannot be read, but its note and acknowledgement may already have been written before
gate clearing refuses. Check the mailbox before repeating a terminal reply; a remote retry
finishes its recorded reply without posting a duplicate.

Repair the file's JSON or read permissions, or remove `.orchestrator/flip.json`. Removing it
drops any gate it held as well as its other flip state. If the gate must remain, stop supervision
before removal, then set it again with `orch flip gate --active <door> --reason "..."` (and
`--question <id>` if needed) before restarting supervision. Never remove the separate
`flip.json.lock`. Repair state before kickoff: its preflight currently discards the gate-clear
child's status, so a usable-vendor kickoff can
continue launching despite that refusal. A both-red kickoff exits 1 when its subsequent pause
cannot be recorded; there is no blanket kickoff exit-1 guarantee for unreadable flip state.

**Required option values.** A trailing bare `--active`, `--from`, `--reason`, `--question`,
`--resume`, `--to` or `--brief` on `orch flip` prints `--<option> requires a value` on one stderr
line, exits 2, and writes no state or mailbox message. Supply a value, such as
`--reason "human approval required"`. Omitting `--reason` (or passing `--reason ""`) still uses
the default and posts the gate handoff; a bare `--reason` is a usage error, not a defaulted gate.

## Workers and the supervisor

**Workers.** `orch worker start` runs one brief on one harness. State lives in
`.orchestrator/workers/<lane>.json`, the lock in `<lane>.lock`, the log in
`.orchestrator/logs/<lane>-<run>.log`. The runner holds a kernel record lock on `<lane>.lock`
for its whole run, so a lane has one runner; `orch worker stop` signals only that holder. A lock
file left by a runner that died is reclaimed by the next start, `--detach` included: never delete
one by hand, since deleting a held lock lets a second runner in. `--detach` launches a kept-awake runner. The bridge timeout
for a worker is 120 minutes unless `--timeout-min` says otherwise. A failed run posts `blocked`
and a budget refusal (exit 75 / budget red) posts `deferred` to the mailbox; a finished run posts
nothing itself (the agent posts its own `release`). `stop --lane L` sends SIGINT to the lane's
process group: the worker publishes `interrupted`, then records the bridge's failed result, so the
lane ends `failed`. `orch worker stop` signals only a worker that still holds the lane's lock
([PE-23](../go/README.md#crash-output-and-parity-exceptions)). From the conductor, `worker_start`
accepts `brief`, `harness`, optional `lane`, `model`, `effort`, `resume`, `breadth`.
Workers are instructed to publish a report and a mailbox release; check the actual state and
messages rather than assuming every successful run did.

**Supervisor.** One per project (`supervisor.lock`), started by `orch kickoff` or `orch supervisor
start`. It ticks every 5 seconds: observes every lane, posts events to the mailbox (kind `wake`)
first and then tries the wake channels, re-dispatches budget-deferred worker lanes after the reset
(a deferred conductor is relaunched only by the conductor evaluation below), and once a minute
evaluates the conductor:

- `awaiting_human` in `flip.json`, or unreadable/invalid flip state: do not relaunch; log the gate
  once until it is cleared or the state is repaired. Every automatic conductor launch uses
  `flip run --unless-gated` to recheck for a gate before writing.
- `pending_launch`: launch the target once the lane is idle and unlocked.
- an idle conductor lane that ended `failed`, `deferred` or `interrupted`, that `finished` (with
  or without a bare `handoff`), or whose runner died before writing its final status (the lane
  still says `running`/`starting`, its pid is dead and its lock free), or a paused project:
  relaunch through `orch flip run`, resuming the previous session when the door is the same; a flip
  or hand-back decision picks the other door. Only a gate or a pause (`decide` answering `pause`)
  holds a restart back, so a conductor that must stay stopped uses `orch flip gate`. The first
  relaunch of each run posts one informational `note` to `human` naming the previous run, the cause
  and the outcome.

Relaunch bookkeeping belongs to `.orchestrator/supervisor-state.json`, in `redispatch` keyed
by the old lane's run id (or `<pid>:<lane>`); a pending launch records under the conductor slot's
key, and a slot that still holds an unconfirmed supervisor launch is retried even after `flip run`
cleared the pause or pending launch that caused it. The supervisor never rewrites a
worker's lane file. A launch confirms only after exit 0 and JSON containing a run id and runner
pid (for a flip, the `run_id` and `runner_pid` fields of `orch flip run`'s answer, not its cut
`launcher` quote); a legacy `redispatched_at` still means the lane's restart or redispatch is done,
but never blocks a decided flip, hand-back or paused-project relaunch. A failed lane redispatch
note says `re-dispatch failed (attempt <n> of 5)`. Failed attempts
retry after 60, 120, 240 and 480 seconds, with a 30-minute backoff ceiling. After five failures
for a key, one `blocked` note includes the last output and a manual retry command; a worker
lane's retries then stop, while a conductor or pending launch is retried every 30 minutes until
a launch is confirmed or a gate or pause holds it, repeating the `blocked` note at most every
6 hours. A conductor launch that was confirmed but whose runner never published a lane (the slot
still shows the old run, idle and unlocked, 600 seconds later) counts as a failed attempt. A
lane that is not running shows `next_check` and `next_check_at` as null in the state. Conductor and pending-launch notes go to `human`; worker-lane notes go to
`coordinator`. The decided conductor path also keeps its 600-second project cooldown;
never-started flip targets keep their separate 120-second backoff. Gate refusals and answers
that defer to `pending_launch` do not count as attempts. Failed redispatch-note posts log a
diagnostic and let the tick continue; records for obsolete runs and pending launches are pruned.
Absent, null or primitive conductor state uses the `conductor:none` slot key. Pruning
retains the current conductor slot key, so failures accumulate to the five-attempt stop even
before a conductor file exists. A newly published real run replaces that slot key; the next
tick prunes `conductor:none`.

Stopping the supervisor (`orch supervisor stop`) does not stop workers. `ack --event ID` records
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
orch mailbox post --from A [--to B] [--kind release] --subject S [--body text|-] [--ref path]... [--thread id]
                           [--recommended TEXT] [--blocking true|false] [--ask decision|action]   # question only
orch mailbox list [--to B] [--unacked-by B] [--kind k] [--since iso] [--limit n] [--json]
orch mailbox ack --id ID --by B [--note text]
orch mailbox inbox                       # open questions for you, each with its reply command
orch mailbox reply <id-or-prefix> "answer"  # threaded note from "human" + ack
```

`--root DIR` or `ORCHESTRATOR_PROJECT` selects the project; the default is the current directory.
`list`, `ack`, `reply` and `inbox` refuse to run where that directory has no `.orchestrator/`
(exit 2, nothing written); `post` creates it. An `ack` or `reply` id that matches no message names
the mailbox it searched (exit 1).

A question must carry `--recommended "<the answer you recommend>"`; `--blocking` (default `true`;
bare means `true`) says whether it blocks the poster. The message line gains `"recommended"` and
`"blocking"` after `"thread"`; other kinds take neither option. A question without a non-empty
`--recommended`, a `--blocking` other than `true` or `false`, or either option on another kind is
refused with a usage line (exit 2, nothing written). The Go mailbox API returns
`ErrQuestionNeedsRecommendation` for a question without one.

`--ask` says what a question asks of the human: `decision` (the default, also when the line has no
`ask`) or `action`, for steps the human must do. An action question takes no `--recommended` (the
body carries the steps) and is written with `"blocking"` then `"ask": "action"` after `"thread"`;
Work Trains offers it no Accept, only an "I've done it" button and Reply. A recommended answer that
reads as a completion claim is refused: recommended answers are for decisions. After trimming and ASCII
case aside, a claim opens with `completed`, `all done`, `I ran`, `I've done` (either apostrophe), `I have
done` or `I did` as whole words; `done` followed by `:` `,` `.` `!` `—` or the end of the text or a clause
cue; or `ran`, `created`, `installed`, `finished`, `deployed` or `rebuilt` followed by `:` `,` or a clause
cue, where a clause cue is spaces or tabs then `it`, `the`, `that`, `this`, `successfully`, `ok` or `all`.
So "Done: token created" and "Ran the script" are refused, and "Done is better than perfect" and "Ran
Kirsh" pass. The lint is a guard rail, not a guarantee ("Yes, done" passes); what holds is that an action
question offers no Accept and that the conductor verifies a reply. `post()` treats an empty `ask` as a
decision, as `orch` does. Each of these, a `--ask`
other than `decision` or `action`, and `--ask` on another kind is a usage line (exit 2, nothing
written); the Go mailbox API returns `ErrActionTakesNoRecommendation` or
`ErrRecommendationClaimsDone` for the first two. A reply to an action
question is a claim the conductor verifies before relying on it. `inbox` ends the header of a blocking
question with ` (blocking)` and prints `recommended: <first 200 UTF-16 units, newlines as spaces>` under its body; older
questions without these keys print as before.

Manifests are sha256 fingerprints of the files a slice released:

```sh
orch manifest create --task T --author A --out .orchestrator/manifests/T.json <paths...>
orch manifest verify .orchestrator/manifests/T.json   # exit 1 on any mismatch
orch manifest diff before.json after.json
```

A manifest identifies released bytes; it does not prove a review happened or that the bytes are
safe.

## Project state files

Each work project gets a private `.orchestrator/` (kickoff and flip add it to the
repository's `info/exclude`):

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
| `sessions/<door>.json` | Conductor heartbeat: `door`, `session_id`, `state` (`working`/`waiting`/`done`/`ended`), `now`, `slice`, `progress` (`{done, total}`), `updated_at` |

The Claude conductor writes its heartbeat itself: `claude/agents/conductor.md` runs `orch session beat
--door claude --hook` from frontmatter hooks on `UserPromptSubmit` (working), `Stop` (waiting) and
`SessionEnd` (ended). Hook mode reads only `session_id`, `cwd` and `hook_event_name` from the hook's
JSON, never stores the prompt, writes nothing where the hook's `cwd` has no `.orchestrator/`, and
never exits 2 (which Claude Code reads as "block"). `orch` writes `sessions/` 0700 (tightening
an existing one this user owns) and its files 0600 through a private temp file and a rename.
Conductors set `--now`, `--slice` and `--progress` themselves; `--now`, `--slice` or `--session`
given as `""` is cleared, while `--progress` must always be `N/M`.

`orch` writes run records, lane state, `flip.json`, the
generated briefs and the supervisor's state and acks owner-only (0600) through a private temp file
and a rename, never through a planted symlink; the supervisor lock and log are created 0600 (an
existing log keeps its mode), and `runs/`, `workers/` and `logs/` are 0700. Details:
[PE-21](../go/README.md#crash-output-and-parity-exceptions).

Per-user state lives in `~/.local/state/opencode/orchestrator/` (`usage.json`,
`incidents.jsonl`). Installer receipts and backups are separate, under
`$XDG_STATE_HOME/opencode-orchestrator` (default `~/.local/state/opencode-orchestrator`).

## Incident log and keep-awake

Orchestrator defects (tool/contract mismatches, misroutes, budget surprises, bridge crashes, flips,
and corrections the human gives about the workflow) are appended, redacted, to
`~/.local/state/opencode/orchestrator/incidents.jsonl` by the bridges, worker, supervisor, plugin
and the `orch_incident` tool. `orch incident report` groups them by fingerprint; `/retro` and the
audit skill turn repeats into fixes. Nothing is uploaded: `orch incident share --out file.json`
writes a sanitized bundle you can attach to an issue by hand.

Detached workers and the supervisor are wrapped in `caffeinate -i -s` on macOS
(`systemd-inhibit --what=idle:sleep` on Linux), so long runs survive idle sleep; the assertion ends
with the process. Closing a laptop lid still sleeps the machine unless it runs in clamshell mode
(external display and power). Disable with `runtime.keep_awake: false` in `models.json`.

## Install, update, uninstall and recovery

**Requirements.** macOS or Linux, Bash 3.2+, Node 22+ (for `install.sh`, `uninstall.sh` and the
tests only; the runtime is `orch`), Git, opencode v2, Claude Code and Codex on `PATH`, each logged in. Config and installer state on the same filesystem (atomic renames). The
runtime expects `~/.config/opencode`; a nondefault `XDG_CONFIG_HOME` is rejected. Optional: Go
1.25+ to build `orch` locally. Release checks used macOS, Node v26.5.1, opencode v2.0.32, Claude
Code 2.1.287 and codex-cli 0.162.1; Linux and Node 22 are supported assumptions, not exercised
release environments. No npm install is needed to run the orchestrator.

**Install.** `./install.sh [--copy] [--force] [--skip-binary] [--no-reload]`. The default creates a
real `~/.config/opencode` and links `AGENTS.md`, `opencode.jsonc`, `agents`, `commands`, `skills`,
`plugins`, `orchestrator`, `install.sh` and `uninstall.sh` to the checkout, so runtime files such
as `service.json` stay out of the checkout; keep the checkout at a stable path. `--copy` copies the
managed entries instead (excluding history, dependencies, logs and runtime state, and refusing
symlinks inside them). Any existing target is refused without `--force`. Force does not merge: it
moves the old target, with its custom config, runtime files and `node_modules`, into private backup
storage. Stop opencode sessions, the background service, supervisors and workers first. The
installer then runs `opencode reload` and `orch doctor` (the `orch` it placed, or the existing one
with `--skip-binary`); a reload or doctor failure exits nonzero and keeps the target, receipt and
backup for recovery. `--no-reload` skips both (for hermetic tests and scripted installs) and says
so; the receipt still becomes active although neither reload nor doctor ran, so run
`opencode reload` and `orch doctor` yourself afterwards.
The `orch` binary step is described in [go/README.md](../go/README.md#install).
`--skip-binary` places no binary and uses an existing `orch`: `ORCH_BIN` when set (it must be an
executable file; nothing else is tried), else `~/.local/bin/orch`, else the first `orch` on `PATH`.
It must answer `orch version`, or the install is refused before anything changes. The receipt
records no binary, so uninstall leaves that `orch` in place. The installer prints
`orch: <path> (existing; not managed by this install)`, plus a note when its version differs from
the checkout's `go/internal/version/VERSION`. A release download
gives up after 20 s without a connection, 60 s below 1 KiB/s or 15 min in total, and fails with
`Download failed` and the usual recoverable state. The 15-minute cap holds even while bytes flow,
so a healthy link slower than about 20 KB/s can hit it; build `orch` locally with Go instead, or
retry on a faster link.

Installer state lives in `$XDG_STATE_HOME/opencode-orchestrator` (default
`~/.local/state/opencode-orchestrator`), private (0700, receipts 0600); state inside the checkout or
config is rejected. There is one active receipt at a time.

**Update** (symlink install): `git pull` alone does not change what runs, because the plugin and
every command go through the `orch` binary. Pull in the checkout, rebuild `orch`
beside the installed one and swap it in, reload, check health:

```sh
git pull
(cd go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ~/.local/bin/orch.new ./cmd/orch)
mv ~/.local/bin/orch.new ~/.local/bin/orch
orch version
opencode reload
orch doctor
```

The `mv` replaces the binary in one step, so nothing runs a half-written `orch`; `orch version` should
show the commit you pulled. Do not rerun `./install.sh` for this: it refuses while `orch` exists. A
running Work Trains LaunchAgent keeps the old binary until it restarts, and a Worker-first rollout
deploys the Worker before the rebuild; both are in the root README's
[Updating](../README.md#updating).

Contributor verification (including the Node test suite) is in
[CONTRIBUTING.md](../CONTRIBUTING.md), not required for an upgrade. Node remains required by
the installer and uninstaller, not by `orch`.

Copy installs do not follow Git: uninstall, update the checkout, reinstall with `--copy` (and
`--force` if the restored previous config occupies the target).

**Upgrading from 0.5.0.** 0.6.0 deletes the JS runtime: the Node scripts in `orchestrator/bin`
(only `setup.sh` stays), the libraries and their unit tests. After `git pull` every `node` command
that ran one of those scripts is gone: use `orch <name>`. Rebuild `orch` with the Update lines before
restarting the service. Remove `ORCH_RUNTIME=js` and any `runtime: "js"` option, then run
`opencode service restart`. A 0.5.0 receipt still uninstalls.

**Upgrading from 0.4.x.** A 0.4.x receipt has no `orch`, and a reinstall is not migrated. Either
`./uninstall.sh` then `./install.sh`, so the receipt records the binary, or build `orch` by hand into
`~/.local/bin` (the `go build` and `mv` lines above). A hand-built `orch` is not tracked: uninstall leaves it,
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
orch doctor
orch usage --refresh
```

The doctor checks binaries, logins and usage feeds, routing, config/plugin/agents, the Go unit
tests and recent plugin errors. Its `orch binary` row reports its own executable and version. Its
`orch on PATH` row warns when no `orch` is on `PATH` (agent commands `orch ...` would fail) or when
the one on `PATH` is not the running binary. Expect `OK` rows, including
`OK unit tests — go test ./... passed`. `WARN` can mean unclear login or recent plugin
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
| Doctor unit-test row fails | `(cd go && go test ./...)` in the checkout (the doctor has a 120 s limit) |
| `orch on PATH` warns | Add the directory of the `orch` you run (default `~/.local/bin`) to `PATH`, or remove the other `orch` it names |
| Codex bridge argument error | See [limitations](#tests-verification-and-limitations) |
| `not an orchestrator project: <dir> has no .orchestrator/ directory` | A read or stop command (`mailbox list\|ack\|reply\|inbox`, `watch`, `flip status`, `flip gate --clear`, `worker status\|stop`, `supervisor status\|stop\|ack`) ran outside the project: `cd` there or pass `--cwd` (`--root` for the mailbox). Exit 2, nothing written |

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
- Run records, worker and supervisor state and logs are owner-only (see
  [Project state files](#project-state-files)); a failed worker's recorded errors and `blocked`
  message have token-shaped text replaced with `<token>` (PE-24); the plugin passes
  incidents to `orch` on stdin, not in process arguments (PE-25)
  ([go/README.md](../go/README.md#crash-output-and-parity-exceptions)).

## Tests, verification and limitations

```sh
node --test tests/*.test.mjs                # installer matrix + plugin tests, temp HOME, stubbed CLIs
```

The Go checks are in [CONTRIBUTING.md](../CONTRIBUTING.md) and [go/README.md](../go/README.md#checks).
The parity harness extracts the pre-S12 JS at commit `9e3a456` (`go/parity/js-ref`) from git;
the last JS release, `v0.5.0`, is not the oracle. Under a temp `HOME`, point `GOCACHE` at a shared cache.

- **Verified:** the unit suite (routing, flip, supervision, mailbox, incident, usage cache), the
  installer and plugin suites against stubs, the Go build/vet/gofmt/tests and the
  JS-vs-Go parity harness in CI on macOS and Linux. Installer tests never touch a real config or
  authenticate a model.
- **Codex flags:** for codex-cli 0.162.1, `codex-exec` requests the default `workspace-write`
  sandbox with `--approve-for-me` alone (that CLI rejects it next to `-s`) and any other sandbox
  with `-s <mode>`; `exec`-only options (`--approve-for-me`, `-s`, `--add-dir`) come before
  `resume <id>`, which does not accept them.
- **Not verified:** a real install over an existing user config, Linux/Node 22 execution, every
  bridge/review combination, storage or power failures, long unattended runs on large repositories.

## Design notes

Policy lives in one JSON registry, and the routing logic is pure, so it is testable without account
access. Small CLI adapters isolate each vendor's interface, while project-local files carry
resumable state across harnesses. Managed links separate published code from global runtime
writes; private receipts and archive-first removal favour recoverability over aggressive cleanup.
Budget-aware routing and cross-vendor review are guardrails, not substitutes for human approval and
real tests.
