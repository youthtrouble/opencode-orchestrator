# opencode-orchestrator

A global orchestrator for [opencode](https://opencode.ai) v2 that builds software the way a small
senior team would: one model plans, another writes the code, a model from the *other* vendor reviews
it, and a cheap model runs the real checks. It spends two subscriptions you already pay for, a
Claude plan (through Claude Code) and a ChatGPT plan (through Codex), and watches both usage
windows so work keeps moving when one of them runs low.

Current release: [v0.5.0](https://github.com/youthtrouble/opencode-orchestrator/releases/tag/v0.5.0)
(2026-10-19). MIT licensed.

This checkout targets **0.6.0 (unreleased)**: `orch` is the only runtime; the JS runtime is removed.

## Why it exists

Two subscriptions give you two families of strong models and two separate usage budgets. Used by
hand, you end up copying context between tools, reviewing your own model's work with the same
model, and stalling whenever one window hits its cap. This project turns that into a routine:

- **Right model for the job.** Frontier models (Claude Fable 5.1, GPT-6 Astra) plan. Capable models
  (Claude Opus 5.5, GPT-6.1 Sol) write code. Cheaper models (Sonnet 5.5, Sol at medium, GPT-6 Luna)
  review and verify. Effort scales with the size of the task, not a fixed setting.
- **Independent review.** Code written by one vendor is reviewed by the other, against a sha256
  manifest of exactly the files that were released. Nobody approves their own work.
- **A global UI taste bar.** [AGENTS.md](AGENTS.md#taste-bar-for-every-ui) and all standalone Claude
  agent prompts carry the same design requirements: product-specific character, purposeful motion
  and haptics, accessibility, real rendered evidence and independent review, not generic UI polish.
- **Budget awareness.** It reads the Claude session and Fable weekly windows and the available Codex
  5-hour and weekly windows. On weekly-only Codex plans, weekly usage alone steers routing.
  Each vendor is green, amber or red; red work moves to the other vendor, and
  coding is balanced toward whoever has more headroom.
- **It keeps going without you.** Two front doors, opencode and Claude Code, hand the project back
  and forth as budgets change. A per-project supervisor restarts a stopped conductor and resumes it
  after a reset. Verified slices are pushed for you; releases, deletions, spending and deploys wait
  for your answer.
- **Everything is on disk.** Plans, briefs, reports, reviews, manifests, questions and the exact
  next step live in `.orchestrator/` inside your project, so any session, either vendor or you can
  pick up cold.

## The catch

- **Two paid subscriptions and three CLIs.** You need a Claude plan that includes Claude Code, a
  ChatGPT plan that includes Codex, and `opencode`, `claude` and `codex` installed and logged in.
- **macOS or Linux only.** Releases are checked on macOS; Linux and Node 22 are supported in
  principle but not exercised.
- **Claude runs through a bridge.** opencode v2 has no native Claude subscription sign-in, so Claude
  work runs as `claude -p` (Claude Code print mode) sessions. They are separate processes with their
  own context, not opencode subagents.
- **Cross-vendor review needs both budgets.** When the reviewing vendor is red, review waits (other
  slices continue) or a same-vendor review is used and labelled as such.
- **Budgets are advisory.** Usage comes from each vendor's own feeds; when a reading is unavailable
  the bridges proceed rather than block.
- **Agent commands need `orch` on PATH**, including in the opencode service and headless Claude
  workers. The doctor's `orch on PATH` row warns when it is missing or resolves another binary.
  Per-project plugin options may not reach the plugin; use `ORCH_BIN` for a custom binary.
- **It is not a sandbox.** Models run tools and shell commands with the permissions you grant, and
  by default push verified slices to `main`.

## How do I...

**...use it on a project?** `cd` into the repo and run `opencode` (its default agent is the
conductor) or `claude --agent conductor`. Describe what you want. That is all interactive use needs:
the conductor plans, delegates, reviews and verifies, and asks you questions in the chat.

**...know where to type `orch` commands?** In a normal terminal, or after `!` in Claude Code. Not
in the opencode chat: there you just talk, and the conductor runs the same tools itself. `orch` is
for checking on and steering runs when no chat is open.

**...deal with the mailbox? Do I need to watch it?** Only when the conductor runs unattended in the
background (after `orch kickoff --headless`, after a flip to the other vendor, overnight). Then it
cannot ask you in a chat, so it uses the mailbox: a shared inbox file in your project,
`.orchestrator/mailbox/messages.jsonl`, where background agents leave questions and notes for you
and for each other, and where your answers go. Questions come in two kinds:

- **Blocking:** releases, deleting code, spending money, deploying. The conductor asks, keeps
  working on anything independent, and stops at a human gate only when nothing else is left. The
  one exception is this repository's Work Trains deploy: once you configure `remote-deploy.yml`,
  the conductor pushes and watches that run instead of asking.
- **Continuing with an assumption:** product questions. The conductor writes its assumption into
  `STATE.md` and the report, asks, and carries on. Answer when you can; it reads your reply on its
  next turn.

Every question carries the answer the conductor recommends and whether it blocks the conductor.
`orch watch` lists them under "QUESTIONS FOR YOU"; `orch mailbox inbox` prints each with its reply
command, marks blocking ones `(blocking)` and shows the recommended answer, so you can reply with
it or with your own. A question that asks you to *do* something (run a script, create a secret,
rebuild `orch`) is an action question: it lists the steps in its body and carries no recommended
answer, so nothing you accept can claim the work is done. Reply once you have done it; the conductor
checks the result before it relies on your reply.

**...leave it running while I'm away?** From a terminal in the repo, with a brief written to
`.orchestrator/briefs/<idea>.md`:

```sh
orch kickoff --brief .orchestrator/briefs/<idea>.md --headless --unattended
```

`--headless` runs the conductor in the background instead of opening a session in your terminal;
`--unattended` applies the away-mode policy (push verified slices, continue past product questions).
Releases, code deletion, spending and deploys still ask; force-pushes never happen. Change a
project's policy with `orch policy set <key>=auto|ask`, for example `release=auto` (see the
[policy reference](orchestrator/README.md#autonomy-policy-and-human-gates)).

**...leave mid-session?** Type `/away` in the conductor's chat (opencode or Claude Code), or just
say "I'm away" or "keep going without me" (it then asks you to confirm once). The conductor writes `STATE.md` and the remaining
briefs, runs the kickoff above (door picked by budget), confirms the background `conductor` lane is
running, tells you how to check in, and then stops, so only one conductor drives. `/away
<brief>` names the brief to continue with; `/away --dry-run` only shows the door and prompt
kickoff would use, changes nothing in the project, and keeps the session going.

**...check on it from my phone?** Set up [Work Trains](#remote-status-work-trains), an optional private
status page on your own Cloudflare account: it lists every project's state and lets you answer questions, pause and
resume from the phone.

**...check in, answer, or unblock it?**

```sh
orch watch --follow                                     # one live screen; Ctrl-C to leave
orch mailbox reply 1a2b3c4d "Use Postgres, not SQLite"  # the id, or its first 8 characters
orch flip gate --clear                                  # after a gate: clear it; the conductor restarts
```

Replying to the question a gate is waiting on clears it too. Details are in
[step 3](#3-while-it-runs).

| Inside opencode / Claude Code (chat) | In a terminal (`orch`) |
|---|---|
| Describe what you want; answer questions in the chat; `/away` when you leave | `orch kickoff --brief <file> --headless --unattended` to run in the background |
| `/usage`, `/plan <idea>`, `/ship <idea>`, `/brief` (opencode) | `orch watch --follow`, `orch usage --refresh` to check in |
| `/continue` (opencode) to resume from `STATE.md` | `orch mailbox inbox`, `orch mailbox reply <id> "..."` to answer |
| The conductor runs the same tools for you | `orch flip run --to <door>`, `orch worker stop --lane conductor` to steer |

## Daily commands

`orch` is the orchestrator's command-line tool, installed to `~/.local/bin` (put that directory on
your `PATH`). Run these from inside your project.

| You want to | Command |
|---|---|
| Start a run (picks the door by budget) | `orch kickoff --brief .orchestrator/briefs/<name>.md [--headless] [--unattended]` |
| See everything on one screen | `orch watch --follow` |
| See questions waiting for you | `orch mailbox inbox` |
| Answer one | `orch mailbox reply <id> "your answer"` |
| Budgets and routing right now (with OpenAI credits, when Codex reports them) | `orch usage --refresh` |
| What the conductor may do without asking | `orch policy show` |
| Let OpenAI work run on ChatGPT credits past the plan limit (off by default) | `orch policy set openai_credits=on [openai_credits_floor=20]` |
| Restart the conductor on a door | `orch flip run --to claude` (or `--to opencode`) |
| Pause the background conductor | `orch flip gate --reason "paused"` then `orch worker stop --lane conductor` |
| Supervisor | `orch supervisor status` / `start` / `stop` |
| Recent orchestrator defects | `orch incident report` |
| Health check (including `orch` on agents' PATH) | `orch doctor` |
| Work Trains sync status (if set up) | `orch remote status` |

Every orchestrator command runs through `orch`; there is no JS fallback. Plugin tools use the
same building blocks as `orch <cmd>`.

In the checkout, `go/` holds the runtime and tests; `orchestrator/` holds `models.json`,
`bin/setup.sh` and reference/history docs, not executable JS.

## How it works

```text
  you: idea or brief
        |
        v
  conductor  ---- opencode door (OpenAI-native) <-> Claude Code door (Anthropic-native)
        |         reads budgets, .orchestrator/STATE.md and the mailbox
        |
        +--> planner   Fable 5.1 / GPT-6 Astra      writes plans/ and bounded briefs/
        +--> coder     Opus 5.5 / GPT-6.1 Sol       one slice, checks, report, manifest
        +--> reviewer  the other vendor             reviews the manifest's bytes
        +--> verifier  Sonnet 5.5 / GPT-6 Luna      runs the real checks
        |
        v
  STATE.md updated, slice pushed, next brief
```

**The conductor** never writes product code. It plans with a frontier model, hands each bounded
brief to a coder, sends the result to the other vendor for review, has a verifier run the real
checks, and records the next step. All routing comes from one file,
[`orchestrator/models.json`](orchestrator/models.json): roles, tiers, thresholds, effort policy and
autonomy.

**Budgets.** A vendor is amber at 60% used and red at 85%. When a 5-hour window exists, it steers
routing; the vendor-wide weekly cap blocks only when red. Codex weekly-only plans show `5h n/a`
in usage and doctor; their weekly window alone steers routing and flip at every level (amber steers,
red blocks). Fable is used only while Claude's frontier posture is green.

On weekly-only Codex plans (such as `prolite`), unreadable usage remains unknown, not `n/a`.
Headroom and reset times use that driving window.

**Two front doors.** `opencode` runs the OpenAI-native conductor and reaches Claude through
`claude -p`. `claude --agent conductor` runs the Anthropic-native conductor and reaches OpenAI
through Codex. Only one drives a project at a time. At each brief boundary the conductor checks the
budgets: if its own vendor is red it flips to the other door; the away door hands back once home
(opencode by default) has been green for 10 minutes. If both are red it pauses, and the supervisor
relaunches it at the first reset.

**Background runs.** A headless conductor runs as the worker lane `conductor`. Each lane has a
two-hour limit by default; when one ends early or fails, the supervisor relaunches the conductor,
which resumes from `STATE.md`. A lane has one runner at a time: the runner holds a kernel lock on
`.orchestrator/workers/<lane>.lock` while it runs, and `orch worker stop` signals only that holder.
A lock file left by a runner that crashed is reclaimed by the next start; never delete one by
hand. Background processes are kept awake with `caffeinate` (macOS) or
`systemd-inhibit` (Linux); a closed laptop lid still sleeps unless you run in clamshell mode with
power and an external display.

## Use it on a project

### 1. Install once

Prerequisites: macOS or Linux, Bash 3.2+, Node 22+ (installer, uninstaller and tests only), Git,
opencode v2, Claude Code, Codex, and
optionally Go 1.25+ (to build `orch` locally; without Go the installer downloads it).

```sh
git clone https://github.com/youthtrouble/opencode-orchestrator ~/Developer/opencode-orchestrator
cd ~/Developer/opencode-orchestrator
claude auth login
codex login
opencode auth login openai --method chatgpt-token-sharing
./install.sh           # links the config into ~/.config/opencode and installs orch to ~/.local/bin
./install-claude.sh    # links the Claude Code agents and skills into ~/.claude
orch doctor            # expect OK rows; WARN means look closer, FAIL exits nonzero
```

If the shell cannot find `orch`, add `~/.local/bin` to your `PATH`, including the service and
headless workers' environments (or your custom `ORCH_BIN_DIR`). Keep the checkout where it is:
`~/.config/opencode` links back to it. `./install.sh --help` lists `--copy`, `--force`,
`--skip-binary` and `--no-reload` (install without the final `opencode reload` and doctor, for
scripted installs); `./uninstall.sh` restores your previous config. Details, updates and recovery
are in [orchestrator/README.md](orchestrator/README.md#install-update-uninstall-and-recovery).

### 2. Start a project

**Talk to it.** In your project directory run `opencode` (the default agent is the conductor) or
`claude --agent conductor`, and describe what you want. Useful commands inside opencode: `/usage`,
`/plan <idea>`, `/ship <idea>`, `/continue`. In this mode the conductor asks you questions in the
chat. When you leave, type `/away` (either door) and it carries on in the background.

**Or hand it a brief and let it run.** Write a short brief (outcome, acceptance criteria,
constraints, rough size) to `.orchestrator/briefs/<name>.md`, or ask the conductor for one with
`/brief`, then:

```sh
orch kickoff --brief .orchestrator/briefs/<name>.md                          # interactive, best door
orch kickoff --brief .orchestrator/briefs/<name>.md --headless --unattended  # background, you leave
```

Kickoff refreshes budgets, starts the supervisor, adds `.orchestrator/` to the repository's
`info/exclude` (in a linked worktree, the common dir's; in a submodule, its git dir's), picks the
door whose vendor has headroom (home first) and launches the conductor. Extra `--brief` files run
as side work when budget allows. `--door claude|opencode` forces a door; `--dry-run` shows the door
and prompt and changes nothing in the project (no policy, gate, supervisor or exclude change; like
every routing read, it still refreshes the shared usage cache). If both vendors are red it starts
the supervisor, records the pause with this brief, exits 75, and the supervisor starts the run from
this brief at the first reset. If the supervisor does not start or the
pause is not recorded, it exits 1 instead, since nothing would relaunch.

### 3. While it runs

**Answering questions.** A background conductor asks through the mailbox (see
[the mailbox](#how-do-i) above). Run these in the project directory:

```sh
orch watch --follow                    # live screen; "QUESTIONS FOR YOU" is listed at the top
orch mailbox inbox                     # the open questions: (blocking), the recommended answer, the reply command
orch mailbox reply 1a2b3c4d "Use Postgres, not SQLite"   # the id or its first 8 characters
```

Every question shows the answer the conductor recommends and whether it is blocked on you; to accept
the recommendation, reply with it as written. An action question shows steps instead: do them, then
reply.

**Clearing a gate.** A conductor stopped at a human gate stays stopped until you clear the gate.
Answer its question with `orch mailbox reply <id> "..."`, or do the thing it asked for and run
`orch flip gate --clear`. Either clears the gate, and the supervisor restarts that door's conductor
within a minute (it must be running: `orch supervisor status`); a flip queued before the gate is kept
and starts instead. `orch flip run --to <door>` and
`orch kickoff` also clear the gate and start a conductor at once.

Automatic supervisor restarts use `orch flip run --to <door> --unless-gated`: if a human gate is
set when the command checks it, it exits 1 with `{"flipped":false,"reason":"awaiting human"}`,
leaves flip state unchanged and launches no worker. An explicit `flip run` without that flag
and a real kickoff still intentionally clear a readable gate.

**Corrupt or unreadable flip.json.** `orch flip decide --active <door>` answers `gate` with
`flip state unreadable: <code>; fix or remove .orchestrator/flip.json` (exit 0), and the supervisor
logs the problem once and does not relaunch. `flip gate`, `gate --clear`, `pause` and `run`
refuse with exit 1 and leave the file unchanged. `mailbox reply` also exits 1 when it cannot
check the gate, but its reply note and acknowledgement may already have been recorded; inspect
the mailbox before sending the same answer again.

Repair the JSON or restore read access to `.orchestrator/flip.json`, or remove that data file.
Removing it also drops any gate it held: if that gate must remain, stop supervision before
removal and re-gate with `orch flip gate --active <door> --reason "..."` (and `--question <id>`
if needed) before restarting supervision.
Do not remove `flip.json.lock`. Repair state before kickoff: its preflight currently ignores
the gate-clear child's failure and can continue launching when a vendor is usable. The
both-red path exits 1 if it cannot record its pause; kickoff is not a repair command.

**Flip option values.** A trailing bare `--active`, `--from`, `--reason`, `--question`, `--resume`,
`--to` or `--brief` is a one-line usage error (exit 2, no writes or mailbox post). Supply the
value, for example `orch flip gate --reason "paused by me"`.

**Restarting or moving the conductor.** `orch flip run --to claude` (or `--to opencode`) writes a
continue brief and starts that door's conductor in the background. If a conductor is still running,
the launch is queued until it stops; stop it first with `orch worker stop --lane conductor` to move
now.

**Pausing.** Stopping the conductor alone is not enough: the supervisor relaunches a stopped
conductor. Mark the project as waiting for you first, then stop it:

```sh
orch flip gate --reason "paused by me"
orch worker stop --lane conductor
```

Resume with `orch flip gate --clear` (the supervisor restarts the conductor within a minute),
`orch flip run --to <door>` or `orch kickoff ...`. `orch supervisor stop` stops all background
supervision for the project (workers keep running). To take the wheel yourself, pause as above,
then open `opencode` and type `/continue`, or `claude --agent conductor` and ask it to continue from
`STATE.md`; leave the gate set while you drive, or the supervisor starts a background conductor
beside yours. At each brief boundary `orch flip decide` answers `gate` while the gate is set: a
headless conductor stops there by itself, while the interactive conductor you drive reports the
gate and keeps working with you.

### 4. Where things live in your project

| Path in `.orchestrator/` | What it is |
|---|---|
| `STATE.md` | Where things stand and the exact next action |
| `plans/`, `briefs/` | The plan and the bounded briefs each slice is built from |
| `reports/`, `reviews/` | What each coder did; what each reviewer found |
| `manifests/` | sha256 fingerprints of the files each slice released |
| `mailbox/` | Questions, notes and handoffs (`messages.jsonl`) |
| `runs/`, `workers/`, `logs/` | One record per model run, background lane state, logs |
| `flip.json` | Which door drives, pauses and human gates |
| `policy.json` | This project's autonomy overrides (`orch policy set release=auto`) |
| `supervisor.json` | Optional wake channels for the supervisor |
| `sessions/` | One heartbeat per conductor door (`orch session show`): what it is doing and when it last moved. Claude conductors write it through hooks, opencode conductors through the plugin |

`.orchestrator/` is private: kickoff keeps it out of Git through the repository's `info/exclude`.
Run records, lane and supervisor state, `flip.json`, generated briefs and new
logs are also owner-only on disk. Your budgets cache and
the incident log live in `~/.local/state/opencode/orchestrator/`.

## Go runtime

`orch` is the runtime: one Go binary with command parity against the pre-S12 JS at commit
`9e3a456` (pinned in `go/parity/js-ref`), checked in CI apart from the exceptions listed in
[go/README.md](go/README.md#crash-output-and-parity-exceptions) (among them owner-only state files,
bounded child output and process groups). `./install.sh` builds it (or downloads the release
asset and checks its sha256) into `~/.local/bin`; `--skip-binary` requires an existing usable
`orch`. Without a checkout, the last published release is still 0.5.0 (not this checkout):

```sh
go install github.com/youthtrouble/opencode-orchestrator/go/cmd/orch@v0.5.0
```

That puts `orch` in `$(go env GOBIN)` or `~/go/bin`; put it on `PATH` or point the plugin at it with
`ORCH_BIN`. To use 0.6.0 before it is released, build from this checkout (see [Updating](#updating)).

### Plugin runtime

The plugin runs every tool and hook through `orch`; `go` is the default and only runtime.
Remove `ORCH_RUNTIME=js` or a `runtime: "js"` option: both fail with a migration error.
Binary resolution is the `binary` option, `ORCH_BIN`, `~/.local/bin/orch`, then `PATH`; an explicit
bad binary fails loudly, with no fallback. Object-form project options did not reach the plugin
in live testing (probably upstream), so use `ORCH_BIN`. Details and accepted port deviations:
[go/README.md](go/README.md#plugin-runtime).

## Keeping it healthy

- `orch doctor` checks binaries, logins, usage feeds, routing, config, plugin, agents and tests.
- `orch incident report` groups orchestrator defects (misroutes, bridge crashes, flips, your
  corrections) recorded locally and redacted. Nothing is uploaded.
- `/audit-orchestrator` in opencode re-verifies model ids, limits and CLI flags against official
  sources, updates `models.json` and reruns the tests. `/retro` reviews recent runs.
- Update: see [Updating](#updating).
- `not an orchestrator project: <dir> has no .orchestrator/ directory`: you ran a command that
  reads or stops a project's state (`orch watch`, `orch mailbox inbox|reply`, `orch flip status`,
  `orch supervisor stop`, ...) outside the project. `cd` into it, or pass `--cwd <project dir>`
  (`--root` for `orch mailbox`). Likewise `no message with id <id> in <dir>/.orchestrator/mailbox`
  usually means the id belongs to another project.
- *A lane or OpenCode session opened in another project.* A supervisor started by an orch
  older than 0.6.0 passes its own `PWD` to children: restart it from the project directory
  (`orch supervisor stop` then `orch supervisor start`, there or with `--cwd <project dir>`).

## Updating

`git pull` updates the symlinked prompts and plugin, but `orch` is a binary built from the checkout and stays as it was until
you rebuild it. Do not rerun `install.sh` for this: it refuses when `orch` already exists. In the checkout:

```sh
git pull
(cd go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ~/.local/bin/orch.new ./cmd/orch)
mv ~/.local/bin/orch.new ~/.local/bin/orch
orch version
opencode reload
orch doctor
```

The build writes a new file beside the installed `orch` and `mv` swaps it in at once, so nothing ever runs a
half-written binary. Use your `ORCH_BIN_DIR` in place of `~/.local/bin` if you installed elsewhere. `orch version`
should print the checkout's `go/internal/version/VERSION` and the commit you pulled.

Contributor verification, including `node --test tests/*.test.mjs`, is documented in
[CONTRIBUTING.md](CONTRIBUTING.md); it is not an upgrade prerequisite. The runtime needs no Node;
`install.sh` and `uninstall.sh` still require it.

**Upgrading from 0.5.0:** rebuild before restarting the service: old binaries still print deleted
JS commands. Unset `ORCH_RUNTIME=js` (for the service, `opencode service unset env ORCH_RUNTIME`),
remove any `runtime: "js"` option, then `opencode service restart`. Replace old commands in your
project's `STATE.md`, briefs and scripts with `orch <cmd>`; the next flip rewrites its continue brief.
**From 0.4.x:** run `./uninstall.sh`, then `./install.sh` to record the binary in the receipt.

If Work Trains is set up, restart its LaunchAgent so `orch remote serve` runs the new binary (the running one goes on
running the old binary until it exits):

```sh
launchctl kickstart -k gui/$(id -u)/org.opencode-orchestrator.work-trains
```

When a `CHANGELOG.md` entry you pulled says **Rollout:** deploy the Worker first, deploy the Worker
([remote/README.md](remote/README.md#deploy-from-github-actions)) and let it finish before you rebuild `orch` and
restart the LaunchAgent: an older Worker refuses what a newer `orch` sends. A Worker-only rollout needs no rebuild.

## Remote status (Work Trains)

Work Trains is an optional, private status page for every orchestrator project on your Mac, built for your phone.
It shows each project's state (needs you, failed, paused, working, done, idle), its run, open questions, recent
events, CI and both budgets. The page can do four things and nothing else: **answer** a question (accept the
recommended reply or type one; it lands as `orch mailbox reply` would, with `"via": "remote"`; an action question
offers no Accept, shows its steps in full with a **Copy** button and has an **I've done it** button that sends the
typed answer "Owner reports done (unverified).", which the conductor verifies), **pause** a project (sets the human
gate), **resume** it (clears the gate), and **hide** a project from the page (or show it again; this
never reaches the Mac). An answer, pause or resume can be undone for 10 seconds before the Mac may pick it up. The page
cannot start runs, run commands, read files or change code. It is macOS-only and self-hosted: you deploy the Worker
to your own Cloudflare account.

Each vendor's budget chip shows the window that sets its colour (weekly at every level on weekly-only
Codex plans; otherwise the 5-hour one, unless the weekly one is red) and
when it resets, in your phone's time zone and 24-hour time: relative within a day, else the weekday:
`Anthropic 28% · resets in 2 h 10 min`, `OpenAI weekly 100% · resets Mon 08:04 · credits 195.62` (credits only when
Codex reports them). Tap a chip for both windows with clock and relative times, `5-hour 28% · resets 22:50 (in 2 h
10 min)`, and the credits in full. `· no reading` means the Mac has no reading of that vendor from the last 30 minutes.

```text
  Mac: orch remote serve (LaunchAgent)            phone: browser
        |  push snapshots, pull answers                 |  one-time PIN login
        v  HTTPS, service token + machine token         v
  Cloudflare Access (trains.example.com)  <-------------+
        |
        v
  Worker  <-->  D1 (latest snapshots; answers and actions waiting for the Mac)
```

The Mac only makes outbound requests; nothing on the internet reaches it. It pulls every 30 s by default and pushes a
project only when it changed, at most once a minute.

**What leaves your Mac.** Per project: an opaque key (a hash of the machine id and the path) and the directory name;
its state and a short reason; the door, run kind, slice, `now` line and progress from the latest heartbeat; the
newest 20 open questions to you (body and recommended answer); the last 10 mailbox messages as id, time, kind, sender
and subject (never bodies); with `ci` on, the latest GitHub Actions run of up to 5 workflows (repository
`owner/name`, workflow, status, link). Plus this Mac's random machine id, the cached budget postures and the `orch` version. Every free-text field
is scrubbed of credentials, absolute paths, your home directory, emails and tokens, then cut to the schema's limits;
an API route such as `/v1/installations/me` or `/api/v1/summary` is not a path and stays readable. The trade-off:
any absolute path whose first directory is literally `api` or `v` plus digits (`/api/...`, `/v2/...`) is kept too,
so a real top-level directory of that name on the Mac would be readable on the Worker. Accept on the
page sends only "accept" for that question: the Mac replies with the recommendation as your mailbox holds it, never
the scrubbed copy the page shows.
**Never sent:** paths, `STATE.md`, plans, briefs, prompts, file contents, environment, run records, transcripts or
secrets. Audit it any time with `orch remote push --dry-run`: it prints the exact request body and sends nothing.

**Cost.** One person fits Cloudflare's free tier with a wide margin (figures checked 2026-10-20):

| Product | Free allowance | Source (page last updated) |
|---|---|---|
| Workers | 100,000 requests/day; 10 ms CPU per invocation | <https://developers.cloudflare.com/workers/platform/pricing/> (2026-10-02) |
| D1 | 5 million rows read/day; 100,000 rows written/day; 5 GB. Over a limit, queries fail until the daily reset | <https://developers.cloudflare.com/d1/platform/pricing/> (2026-04-21) |
| Email | Sending to verified destination addresses is free on all plans | <https://developers.cloudflare.com/email-service/platform/pricing/> (2026-06-09) |
| Access (Zero Trust) | Free plan, $0, up to 50 users | <https://www.cloudflare.com/plans/sase-zero-trust/> |

Estimated use for one Mac with 5 active projects: about 4,000-5,000 requests/day (pulls 2,880, pushes about 1,000,
an hour of page use 240), under 5% of the allowance; even with every project changing every minute all day it stays
under the allowance up to 67 projects. D1 writes stay well under 5,000/day (the worst case fits up to 34 projects);
reads are under 0.1% for 10 projects. Notification emails are capped at 20 a day.

**Set it up.** You need a Cloudflare account with your domain on it, Node 26 (`npm ci` in `remote/worker` installs
the pinned `wrangler`), and an `orch` that has `orch remote`
(a release after v0.5.0, or built from `main`). One deployment serves one Mac: a second Mac on it can take the
first Mac's answers, reject them as an unknown project, and so lose them
([Before you start](remote/README.md#before-you-start)). Then, in order, following [remote/README.md](remote/README.md):

1. Create the Access application, one-time PIN login and a service token; store the token with
   `orch remote service-token set`.
2. Create the D1 database, deploy the Worker, run `orch remote token rotate`, and set the Worker secrets with
   one `wrangler secret bulk`.
3. `orch remote init --url https://trains.example.com` (projects are found under `~/Developer` unless you pass
   `--root`).
4. Install the LaunchAgent that `orch remote launchd` prints.
5. First sync: `orch remote status` shows a recent `last push`, and the page lists your projects.

Optionally, let GitHub Actions deploy the Worker after every push to main that passes the Remote workflow. Create a
`work-trains-production` environment with a Cloudflare token and your instance's ids, as described in
[Deploy from GitHub Actions](remote/README.md#deploy-from-github-actions). Without that environment, as in a fork,
the deploy workflow skips. Conductors then push and watch the deploy run instead of asking you to deploy. Worker
secrets, Access and DNS stay manual.

**Daily use.** Open the page (add it to your home screen; it has its own icon there). The page is light only, and
colour means status alone: blue-teal working, amber needs you (and a stale Mac), red failed, green done, grey paused
or idle. A recommended answer is one tap on **Accept**; **Type something else** opens a reply. On the Mac,
`orch remote status` shows the config, the Keychain items, the last push, pull and error, and the projects found.
The sync loop logs to `~/.local/state/opencode/orchestrator/remote/remote.log`.

**Rotate credentials.**

- Machine token: `orch remote token rotate`, then paste the printed hash at
  `wrangler secret put MACHINE_TOKEN_SHA256 -c wrangler.deploy.jsonc` (in `remote/worker`). Pushes get 403 until
  the secret holds the new hash.
- Access service token: it expires (after 1 year if you followed the guide). Before then, in **Zero Trust > Access
  controls > Service credentials > Service Tokens**, choose **Refresh** to extend it, or **Rotate secret** for a new
  secret (the old one stays valid for the grace period you pick); after a rotation run
  `orch remote service-token set` and paste the same Client ID and the new secret.

**Turn it off.**

- For now: `launchctl bootout gui/$(id -u)/org.opencode-orchestrator.work-trains`. Back on:
  `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/org.opencode-orchestrator.work-trains.plist`.
- For good: run that `bootout` and delete the plist; `orch remote token revoke` (deletes the three Keychain items);
  delete `~/.config/orch/remote.json` and `~/.local/state/opencode/orchestrator/remote/`. Then, in `remote/worker`:
  `wrangler delete -c wrangler.deploy.jsonc`; to keep the data,
  `wrangler d1 export work-trains --remote --output work-trains.sql -c wrangler.deploy.jsonc` (store the file
  outside the repository); `wrangler d1 delete work-trains -c wrangler.deploy.jsonc`. Finally remove the Access
  application and the `work-trains-mac` service token in Zero Trust.

**Troubleshooting.**

- *The page shows 403, or keeps returning to the Access login.* The email you sign in with must be in the Access
  policy and in the `OWNER_EMAIL` secret (a comma-separated list when you sign in with more than one address), and
  `ACCESS_AUD`, `ACCESS_TEAM_DOMAIN` and `PUBLIC_ORIGIN` must match the application exactly. A 503 means a Worker
  secret is missing. The page names the cause from the 403's `reason` code (a 403 without one reads "Not allowed"): "Signed in as an address that isn't an
  owner" (`OWNER_EMAIL`), "Access sign-in doesn't match ACCESS_AUD or ACCESS_TEAM_DOMAIN", "This address can't send
  answers" (a page open on an address other than `PUBLIC_ORIGIN`; the banner links to it). Only a lapsed sign-in
  says "Signed out. Reload to sign in".
- *Accept shows "orch on the Mac is too old to accept a recommendation".* The Worker is newer than `orch` on the Mac,
  which would have replied with the page's scrubbed copy; the Worker refused the accept instead. Update `orch`,
  restart the LaunchAgent, and answer that question on the Mac (`orch mailbox reply`).
- *The page says "Access token from another team domain" (`wrong_issuer`).* The Access application in front of the
  hostname belongs to another Zero Trust team than `ACCESS_TEAM_DOMAIN`; the message names the team the Worker
  expects. See [Moving to another hostname](remote/README.md#moving-to-another-hostname) in remote/README.md.
- *Moving the page to a new hostname* (a Custom Domain instead of `workers.dev`, say): a one-step cut-over of
  `PUBLIC_ORIGIN`, the deploy config's `routes` and `orch remote init --url`; the steps are in
  [remote/README.md](remote/README.md#moving-to-another-hostname).
- *`orch remote` reports `access_denied`.* Access refused the Mac before the Worker saw it: the service token expired
  or its secret was rotated (`orch remote service-token set`), or the application lacks the **Service Auth** policy
  for `work-trains-mac`. A 403 from the Worker itself (`HTTP 403 forbidden`) usually means `MACHINE_TOKEN_SHA256`
  does not hold the hash of the current machine token.
- *Keychain errors after a reboot.* The login keychain unlocks when you log in; until then cycles fail with a
  `keychain` error and retry with backoff. Log in; `orch remote status` shows each item as `yes`, `no` or `unknown`.
- *"Mac seen" is stale (over 3 minutes).* The Mac is asleep or offline, or `serve` is not running: check
  `launchctl print gui/$(id -u)/org.opencode-orchestrator.work-trains`, `orch remote status` and `remote.log`.
- *A free-tier limit was reached.* Requests or D1 queries fail until Cloudflare's daily reset, and `orch remote`
  records the errors meanwhile; the Cloudflare dashboard shows the usage. Fewer roots, `"ci": false` or a larger
  `poll_seconds` reduce it.

## Documentation map

| Document | Covers |
|---|---|
| [orchestrator/README.md](orchestrator/README.md) | Commands and flags, plugin tools, routing and budgets, flips, supervisor, mailbox, policy, state files, install internals and recovery, troubleshooting, security, limitations |
| [go/README.md](go/README.md) | The `orch` binary: commands, building, install, plugin runtime, parity harness, release checklist |
| [claude/README.md](claude/README.md) | The Claude Code front door: agents and skill |
| [go/README.md](go/README.md#orch-remote-go-only) | `orch remote`, the Mac side of Work Trains: `init`, `status`, `push --dry-run` (prints exactly what a push would send and sends nothing), `once` / `serve` (the sync loop) and `launchd` (prints the LaunchAgent plist that keeps `serve` running) |
| [remote/protocol/README.md](remote/protocol/README.md) | The Work Trains wire protocol: the versioned v1 JSON Schema, its validator and examples; test with `node --test 'remote/protocol/test/*.test.ts'` |
| [remote/README.md](remote/README.md) | Work Trains operator guide: Cloudflare Access steps, D1 and deploy, the Mac's config, Keychain items and state files, the tests |
| [remote/worker/](remote/worker/) | The Work Trains Cloudflare Worker, which you deploy to your own account (by hand or with `.github/workflows/remote-deploy.yml`): the Access auth gate, the D1 migrations, the machine API (push, pull, ack), the browser API (summary, answer, pause/resume, undo, hide), the email notifier and the deploy scripts; test with `npm ci && npm test && npm run typecheck` in that directory |
| [remote/e2e/](remote/e2e/) | The Work Trains end-to-end test: the Worker under `wrangler dev --local` on a temp D1, behind a local Access emulator, driven by the real `orch remote once` and a simulated phone (about 90 s; needs Go, Node 26 and `npm ci` in `remote/worker`, with `remote/worker/node_modules/.bin` on PATH for the wrangler that package pins); run `node --test 'remote/e2e/*.test.ts'` |
| [AGENTS.md](AGENTS.md) | The operating contract every conductor and worker follows |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Checks to run before a change |
| [CHANGELOG.md](CHANGELOG.md) | Release history |

Licensed under [MIT](LICENSE).
