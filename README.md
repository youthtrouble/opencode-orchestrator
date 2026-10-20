# opencode-orchestrator

A global orchestrator for [opencode](https://opencode.ai) v2 that builds software the way a small
senior team would: one model plans, another writes the code, a model from the *other* vendor reviews
it, and a cheap model runs the real checks. It spends two subscriptions you already pay for, a
Claude plan (through Claude Code) and a ChatGPT plan (through Codex), and watches both usage
windows so work keeps moving when one of them runs low.

Current release: [v0.5.0](https://github.com/youthtrouble/opencode-orchestrator/releases/tag/v0.5.0)
(2026-10-19). MIT licensed.

## Why it exists

Two subscriptions give you two families of strong models and two separate usage budgets. Used by
hand, you end up copying context between tools, reviewing your own model's work with the same
model, and stalling whenever one window hits its cap. This project turns that into a routine:

- **Right model for the job.** Frontier models (Claude Fable 5.1, GPT-6 Astra) plan. Capable models
  (Claude Opus 5.5, GPT-6.1 Sol) write code. Cheaper models (Sonnet 5.5, Sol at medium, GPT-6 Luna)
  review and verify. Effort scales with the size of the task, not a fixed setting.
- **Independent review.** Code written by one vendor is reviewed by the other, against a sha256
  manifest of exactly the files that were released. Nobody approves their own work.
- **Budget awareness.** It reads the Claude session and Fable weekly windows and the Codex 5-hour
  and weekly windows. Each vendor is green, amber or red; red work moves to the other vendor, and
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
- **The Go runtime is opt-in until 0.6.0**, and per-project plugin options do not reach the plugin
  yet: switch runtimes with the `ORCH_RUNTIME` environment variable (see [Go runtime](#go-runtime)).
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
  working on anything independent, and stops at a human gate only when nothing else is left.
- **Continuing with an assumption:** product questions. The conductor writes its assumption into
  `STATE.md` and the report, asks, and carries on. Answer when you can; it reads your reply on its
  next turn.

`orch watch` lists them under "QUESTIONS FOR YOU"; `orch mailbox inbox` prints each with its reply
command.

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
| Budgets and routing right now | `orch usage --refresh` |
| What the conductor may do without asking | `orch policy show` |
| Restart the conductor on a door | `orch flip run --to claude` (or `--to opencode`) |
| Pause the background conductor | `orch flip gate --reason "paused"` then `orch worker stop --lane conductor` |
| Supervisor | `orch supervisor status` / `start` / `stop` |
| Recent orchestrator defects | `orch incident report` |
| Health check | `orch doctor` |

Without `orch`, every command also exists as `node ~/.config/opencode/orchestrator/bin/<name>.mjs`.

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

**Budgets.** A vendor is amber at 60% used and red at 85%. The 5-hour window decides routing; a
weekly cap only matters once it is red. Fable is used only while Claude's frontier posture is green.

**Two front doors.** `opencode` runs the OpenAI-native conductor and reaches Claude through
`claude -p`. `claude --agent conductor` runs the Anthropic-native conductor and reaches OpenAI
through Codex. Only one drives a project at a time. At each brief boundary the conductor checks the
budgets: if its own vendor is red it flips to the other door; the away door hands back once home
(opencode by default) has been green for 10 minutes. If both are red it pauses, and the supervisor
relaunches it at the first reset.

**Background runs.** A headless conductor runs as the worker lane `conductor`. Each lane has a
two-hour limit by default; when one ends early or fails, the supervisor relaunches the conductor,
which resumes from `STATE.md`. Background processes are kept awake with `caffeinate` (macOS) or
`systemd-inhibit` (Linux); a closed laptop lid still sleeps unless you run in clamshell mode with
power and an external display.

## Use it on a project

### 1. Install once

Prerequisites: macOS or Linux, Bash 3.2+, Node 22+, Git, opencode v2, Claude Code, Codex, and
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

If the shell cannot find `orch`, add `~/.local/bin` to your `PATH`. Keep the checkout where it is:
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
orch mailbox inbox                     # the open questions, each with its reply command
orch mailbox reply 1a2b3c4d "Use Postgres, not SQLite"   # the id or its first 8 characters
```

**Clearing a gate.** A conductor stopped at a human gate stays stopped until you clear the gate.
Answer its question with `orch mailbox reply <id> "..."`, or do the thing it asked for and run
`orch flip gate --clear`. Either clears the gate, and the supervisor restarts that door's conductor
within a minute (it must be running: `orch supervisor status`); a flip queued before the gate is kept
and starts instead. `orch flip run --to <door>` and
`orch kickoff` also clear the gate and start a conductor at once.

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
| `sessions/` | One heartbeat per conductor door (`orch session show`): what it is doing and when it last moved |

`.orchestrator/` is private: kickoff keeps it out of Git through the repository's `info/exclude`.
With the Go runtime, run records, lane and supervisor state, `flip.json`, generated briefs and new
logs are also owner-only on disk (the JS scripts use your umask's default modes). Your budgets cache and
the incident log live in `~/.local/state/opencode/orchestrator/`.

### 5. Switch to the Go runtime (optional)

The opencode plugin uses the JS scripts by default. To run every tool and hook through `orch`
instead, set `ORCH_RUNTIME` for the opencode service and restart it:

```sh
opencode service set env ORCH_RUNTIME go
opencode service restart
# undo:
opencode service unset env ORCH_RUNTIME
opencode service restart
```

The variable must be in the environment of the opencode process that loads the plugin.
`orch_usage_status` reports which runtime answered. Both runtimes share the same state, so you can
switch at any time.

## Go runtime

`orch` is one static Go binary that replaces every `orchestrator/bin/*.mjs` script with byte-for-byte
parity, checked in CI by a parity harness, apart from the exceptions listed in
[go/README.md](go/README.md#crash-output-and-parity-exceptions) (among them owner-only state files,
bounded child output and process groups). `./install.sh` builds it (or downloads the release
asset and checks its sha256) into `~/.local/bin`; `--skip-binary` opts out. Without a checkout:

```sh
go install github.com/youthtrouble/opencode-orchestrator/go/cmd/orch@v0.5.0
```

That puts `orch` in `$(go env GOBIN)` or `~/go/bin`; put it on `PATH` or point the plugin at it with
`ORCH_BIN`. The JS runtime stays the default until 0.6.0, when it is planned to be removed.

### Plugin runtime

The plugin's runtime is `js` (default) or `go`, chosen by `ORCH_RUNTIME` as shown in
[step 5](#5-switch-to-the-go-runtime-optional). The plugin also accepts `runtime` and `binary`
options, but object-form options in a project `opencode.jsonc` did not reach the plugin in live
testing (probably an upstream opencode issue), so use `ORCH_RUNTIME` and `ORCH_BIN`. In go mode a
missing binary fails loudly; it never falls back to JS. Binary resolution, failure policy and the
accepted go-mode deviations are in [go/README.md](go/README.md#plugin-runtime).

## Keeping it healthy

- `orch doctor` checks binaries, logins, usage feeds, routing, config, plugin, agents and tests.
- `orch incident report` groups orchestrator defects (misroutes, bridge crashes, flips, your
  corrections) recorded locally and redacted. Nothing is uploaded.
- `/audit-orchestrator` in opencode re-verifies model ids, limits and CLI flags against official
  sources, updates `models.json` and reruns the tests. `/retro` reviews recent runs.
- Update: `git pull` in the checkout, run the tests, `opencode reload`, `orch doctor`.
- `not an orchestrator project: <dir> has no .orchestrator/ directory`: you ran a command that
  reads or stops a project's state (`orch watch`, `orch mailbox inbox|reply`, `orch flip status`,
  `orch supervisor stop`, ...) outside the project. `cd` into it, or pass `--cwd <project dir>`
  (`--root` for `orch mailbox`). Likewise `no message with id <id> in <dir>/.orchestrator/mailbox`
  usually means the id belongs to another project.

## Documentation map

| Document | Covers |
|---|---|
| [orchestrator/README.md](orchestrator/README.md) | Scripts and flags, plugin tools, routing and budgets, flips, supervisor, mailbox, policy, state files, install internals and recovery, troubleshooting, security, limitations |
| [go/README.md](go/README.md) | The `orch` binary: commands, building, install, plugin runtime, parity harness, release checklist |
| [claude/README.md](claude/README.md) | The Claude Code front door: agents and skill |
| [remote/protocol/README.md](remote/protocol/README.md) | The Work Trains wire protocol: the versioned v1 JSON Schema, its validator and examples; test with `node --test 'remote/protocol/test/*.test.ts'` |
| [AGENTS.md](AGENTS.md) | The operating contract every conductor and worker follows |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Checks to run before a change |
| [CHANGELOG.md](CHANGELOG.md) | Release history |

Licensed under [MIT](LICENSE).

