# opencode-orchestrator

Two-vendor, budget-aware engineering orchestration for **opencode v2**. A conductor turns an idea
into bounded briefs, assigns models to roles, asks for independent review, runs verification, and
records enough state to resume later. Usage windows steer work away from a constrained vendor.

- **Claude subscription via Claude Code:** Anthropic subscription work goes through the Claude Code
  CLI bridge, not native subscription OAuth inside opencode. Native Anthropic API-key access is a
  separate option, not this subscription setup.
- **ChatGPT subscription via Codex / opencode:** OpenAI work can run natively through opencode's
  ChatGPT sign-in, or through the Codex CLI bridge.

This is an inspectable collection of prompts, configuration, a plugin and dependency-free Node
scripts, not a guarantee of unattended delivery. It has shipped itself once end to end (plan,
build, cross-vendor review, verification, publish); see [verification and limitations](#verification-and-limitations).

There are two front doors over the same project state:

- `opencode` (default agent `conductor`): OpenAI-native conductor; Anthropic roles run through the Claude Code bridge.
- `claude --agent conductor`: Anthropic-native conductor; OpenAI roles run through the Codex bridge.

When one vendor's window is exhausted, continue from the other door with `/continue`.

## Requirements

- macOS or Linux, Bash 3.2+, Node 22+, Git, opencode v2, Claude Code and Codex on `PATH`.
- Access to the configured models through your own subscriptions; authenticate each CLI separately.
- Config and installer state on the same filesystem, so backup/restore can use atomic renames.
- The current runtime expects `~/.config/opencode`. Nondefault `XDG_CONFIG_HOME` is rejected by the
  installer; installing elsewhere would not retarget the runtime's hardcoded home-relative paths.

Release checks used macOS, Node v26.5.1, opencode v2.0.32, Claude Code 2.1.287 and codex-cli 0.162.1.
Linux and Node 22 are supported assumptions, not independently exercised release environments.
No npm install is required to run the orchestrator.

## Install

Intended repository: <https://github.com/youthtrouble/opencode-orchestrator>.
Inspect the checkout before running:

```sh
git clone https://github.com/youthtrouble/opencode-orchestrator opencode-orchestrator
cd opencode-orchestrator
claude auth login
codex login
opencode auth login openai --method chatgpt-token-sharing
./install.sh
```

Installation defaults to **managed-entry symlinks**, not a symlink of the whole config directory.
The installer creates a real `~/.config/opencode` directory and links `AGENTS.md`, `opencode.jsonc`,
`agents`, `commands`, `skills`, `plugins`, `orchestrator`, `install.sh` and `uninstall.sh` to the
checkout. Global runtime files such as `service.json` therefore stay outside the checkout. Keep the
checkout at a stable path. Documentation and root integration tests stay in the checkout.

```sh
./install.sh --copy   # Copy managed entries instead of linking them
./install.sh --force  # Preserve an existing config before installing
```

Any existing target—including a regular file or broken symlink—is refused without `--force`.
Force does **not** merge configs or delete the old one: it moves the old target into private backup
storage. Stop affected opencode sessions/background service, supervisors and workers before using
`--force`, so they cannot keep writing into the moved config. Custom configuration, agents/plugins,
runtime files such as `service.json`, and package/dependency files including `node_modules` move
with the old target; they are not merged into the new installation and need restoration or explicit
manual reconciliation. Copy mode excludes history, dependencies, logs and runtime state, and refuses symlinks
inside managed source entries rather than following unknown data.

Installer state lives in `$XDG_STATE_HOME/opencode-orchestrator`, defaulting to
`~/.local/state/opencode-orchestrator`. The state directory is private (0700), receipts are 0600,
and state inside the checkout/config is rejected. There can be only one active installation receipt.

After deployment, the installer runs `opencode reload`, then the doctor at the **installed**
`~/.config/opencode/orchestrator/bin/doctor.mjs` path. Missing prerequisites are checked before
mutations. A reload or doctor failure returns nonzero and preserves the installed target, receipt
and backup for recovery; it is not reported as a successful installation.

### Uninstall and recover

```sh
./uninstall.sh
# Or, while installed:
bash ~/.config/opencode/uninstall.sh
```

Uninstall verifies the receipt, target identity, managed contents/link destinations and backup
identity. It restores the previous config when one existed and archives the removed installation
and receipt under a unique private `uninstalled-*` directory. It never recursively deletes an
installation. Known runtime additions are preserved in that archive; backups are never placed in
tracked source. Repeating uninstall with no active receipt is a no-op.

Changed/unrecognized targets, missing/replaced backups and invalid receipts are refused. Preserve
and reconcile local changes before retrying; do not delete recovery state to bypass a refusal.
Symlink-source Git updates are expected; copy-mode local content/mode changes are detected.
Uninstall needs only Bash and Node and does not run service commands. Reload opencode separately
when ready to use the restored config.

For an "Installed config changed" refusal:

1. Stop affected processes. Make a private backup of the current config and installer state outside
   the checkout; preserve the opaque original backup and do not edit the receipt/fingerprint.
2. Keep the installed root directory in place so its identity remains valid. Inspect the reported
   changes against the version originally installed, not an updated checkout.
3. Move only identified local additions (for example, newly created package/dependency files) into
   private holding storage. Restore only known managed entries/link destinations/permissions to
   their installed state, preserving changed content first, then retry uninstall.
4. If the installed state cannot be established, or target/backup identity is unrecognized, stop
   for manual recovery. Do not blindly delete unfamiliar files or bypass ownership checks.

A failed install normally remains recoverable with `./uninstall.sh`. An interrupted operation can
leave `operation.lock`; confirm no installer is running before manually reconciling a stale lock.
Low-level storage failures before receipt persistence may require manual recovery. Do not assume
power-loss durability or delete an opaque backup because a command failed.

### Claude Code side (Anthropic-native conductor)

```sh
./install-claude.sh          # symlinks claude/agents/*.md and claude/skills/orchestrator into ~/.claude
claude --agent conductor     # in any repo
./install-claude.sh --uninstall
```

The Claude Code conductor uses the same `.orchestrator/` state, mailbox, manifests and bridges.
Its subagents: `architect` / `architect-hard` (Fable 5.1 high / max), `coder` / `coder-hard`
(Opus 5.5 medium / xhigh), `verifier` and `researcher` (Sonnet 5.5 low), and a same-vendor
`reviewer` used only when the OpenAI window is red. Cross-vendor review goes through the Codex
bridge or `codex review`.

### Update

The Git checkout is the source of truth; `~/.config/opencode` only holds links to it plus runtime
files. Edit in the checkout, commit, push. For a symlink installation, pull updates in the
**checkout**, review them, run both test suites, then reload and check health:

```sh
git pull
node --test orchestrator/tests/*.test.mjs
node --test tests/*.test.mjs
opencode reload
node ~/.config/opencode/orchestrator/bin/doctor.mjs
```

Copy installations do not follow Git updates: uninstall first, update the checkout, then reinstall
with `--copy` (and `--force` if the restored previous config occupies the target). Preserve local
changes before uninstalling. The older nested `orchestrator/bin/setup.sh` is a legacy auth/setup
helper, not the reversible installer documented here.

## One-command runs

```sh
node ~/.config/opencode/orchestrator/bin/kickoff.mjs --brief .orchestrator/briefs/<brief>.md [--brief <side-brief>.md] [--headless]
node ~/.config/opencode/orchestrator/bin/watch.mjs --follow
```

`kickoff` refreshes budgets, starts the project supervisor, excludes `.orchestrator/` from git, picks
the door whose vendor has headroom (home first), and launches the conductor with the right prompt,
interactive by default or as a headless `conductor` lane with `--headless`. If both vendors are red
it records a pause and the supervisor starts the run by itself at the first reset. `watch` is one
refreshing screen: budgets, routing, supervisor, flip state, lanes, mailbox, incidents, next action.

## First run and the conductor loop

Run `opencode` from the project you want to work on, not necessarily this checkout. The installed
default agent is `conductor`. Start with `/usage`, then `/plan <idea>` or `/ship <idea>`.

1. **Orient:** read project `AGENTS.md`, `.orchestrator/STATE.md`, mailbox and current budgets.
2. **Plan:** a planner writes bounded briefs with acceptance criteria and explicit scope.
3. **Build:** a coder implements one slice, runs checks, writes a report and hashes release files.
4. **Review:** the other vendor reviews that fingerprint; the author fixes findings and releases
   a new manifest. Nobody approves their own work. If the other vendor is unavailable, disclose
   the unmet cross-vendor gate rather than treating same-vendor review as equivalent.
5. **Verify:** run the real checks; unrun checks stay "not run", unmet criteria stay "partial".
6. **Record:** update the continuation cursor and mailbox so another session can resume.

Pushes, deployments and spending still need deliberate approval. Prompts and permission rules are
not a security sandbox for every external CLI or tool.

## Roles and routing

`orchestrator/models.json` is the strict-JSON source of truth: `vendors` define labels, windows and
tiers; `roles.*.prefer` defines fallback order; `thresholds` defines budget posture. `$comment`
describes the schema. These are the prepared registry's configured defaults, not a promise that
every model ID is available on every account:

| Role               | Preferred model                                            | Main fallback                                                           |
| ------------------ | ---------------------------------------------------------- | ----------------------------------------------------------------------- |
| Planner            | Claude Fable 5.1, max                                      | GPT-6 Astra, xhigh; then strong tiers                                   |
| Coder              | Claude Opus 5.5, high                                      | GPT-6.1 Sol, high                                                       |
| Reviewer           | Other vendor's mid tier: Sonnet 5.5 or GPT-6.1 Sol, medium | Other vendor's cheap tier; then same-vendor mid tier, explicitly marked |
| Verifier           | Claude Sonnet 5.5                                          | GPT-6 Luna; then Claude Haiku 5.5                                       |
| Researcher         | Claude Sonnet 5.5                                          | GPT-6 Luna                                                              |
| Explorer / utility | Claude Haiku 5.5                                           | GPT-6 Luna                                                              |

Native OpenAI subagents include `architect-oai`, `coder-oai`, `reviewer-oai`, `verifier-oai` and
`researcher-oai`. When routing selects a native agent, `delegate` returns which subagent to call
rather than launching the CLI bridge. Claude subscription work uses the Claude Code bridge.

Budget defaults are **amber at 60% used**, **red at 85% used**. Without an explicit rate-limit lock,
a Claude session or Codex five-hour
window resetting within **20 minutes** is treated one level greener; this grace does not apply to
weekly windows. Claude overall posture combines session and weekly usage; its frontier posture
also considers the model-scoped Fable window. Codex combines five-hour and weekly usage.

Red roles route to another usable vendor or pause. Cross-vendor review may be unavailable even
when coding can continue. The CLI bridges refuse known-red budgets with exit 75 unless explicitly
forced. **This is advisory, not a hard quota:** automatic routing avoids unavailable feeds, but
bridge budget checks can proceed when usage is unknown. Check `/usage` before expensive work;
do not use force as the routine response to a red window.

To change policy, edit `thresholds`, tier fields or role preference lists in `models.json`, keep
valid JSON, run the unit tests, and reload opencode. The plugin syncs native model choices from
the registry; audit static agent/config fallbacks too when changing model IDs. No personal
subscription tier/catalog observations are needed in public configuration.

## Commands and tools

| Command                               | Purpose                                                      |
| ------------------------------------- | ------------------------------------------------------------ |
| `/usage`                              | Refresh usage and explain routing                            |
| `/plan`, `/brief`                     | Plan work and write bounded worker briefs                    |
| `/ship`                               | Follow the build/review/verify loop                          |
| `/review`                             | Request independent review and verification                  |
| `/delegate-claude`, `/delegate-codex` | Request a bounded task on a specific CLI harness             |
| `/continue`, `/handoff`               | Resume or write continuation state                           |
| `/retro`                              | Inspect recent run/review outcomes                           |
| `/audit-orchestrator`                 | Recheck model catalogs, CLI compatibility and policy sources |

The plugin registers the `orch` namespace (often displayed as `orch_*`):

| Tool                                 | Purpose                                                                      |
| ------------------------------------ | ---------------------------------------------------------------------------- |
| `usage_status`                       | Cached/fresh usage and role routing                                          |
| `delegate`                           | Route a bounded brief or direct the conductor to a native subagent           |
| `codex_review`                       | Invoke Codex's review harness; CLI compatibility must be verified separately |
| `mailbox_post`, `mailbox_list`       | Write/read project messages                                                  |
| `manifest_create`, `manifest_verify` | SHA-256 release fingerprints and verification                                |
| `worker_start`, `worker_status`      | Detached worker launch and lane state                                        |
| `supervisor`                         | Start/status/stop/ack supervision                                            |

The same building blocks are available as `node ~/.config/opencode/orchestrator/bin/<script>.mjs`.
Relevant scripts include `usage`, `claude-exec`, `codex-exec`, `worker`, `supervisor`, `mailbox`,
`manifest` and `doctor`; read their source headers for arguments. The presence of a tool does not
mean its external CLI path has passed end-to-end verification.

## Answering the conductor and running unattended

A headless conductor cannot ask you interactively. Its questions land in the mailbox; answer them in one line:

```sh
node ~/.config/opencode/orchestrator/bin/mailbox.mjs inbox                 # open questions + the exact reply command
node ~/.config/opencode/orchestrator/bin/mailbox.mjs reply <id> "answer"   # posts your answer and acknowledges
```

`watch.mjs` shows open questions at the top. The autonomy policy decides what never needs a question:
by default verified slices are pushed to `main` automatically; releases, code deletion, spend and deploys
ask but do not block other slices. `policy.mjs show|set|unattended` edits it per project
(`.orchestrator/policy.json`); `kickoff --unattended` applies the away-mode defaults.

## Flipping between the two doors

One conductor drives a project at a time. `models.json` -> `conductor.home` names the default door
(opencode). Rules:

| Situation at a brief boundary | Active conductor |
|---|---|
| its own vendor is red | flips to the other door (forced; also triggered automatically when its model dies on a usage-limit error) |
| it is the away door and home is stably green (green for `green_grace_minutes` after the reset) | hands back home |
| it is the away door, home still red | keeps going |
| both red | writes the remaining briefs, runs `flip.mjs pause`, stops; the supervisor (if running) relaunches automatically: the home door if it comes back first, otherwise the other door |

A headless conductor that reaches a human gate (push, deploy, spend, product question) records it with `flip.mjs gate`; the supervisor leaves it alone until a human clears it (`flip.mjs gate --clear`, `kickoff`, or a flip). `flip.mjs run` posts a `handoff`, records `.orchestrator/flip.json`, and starts the other door's
conductor headless as worker lane `conductor` (lane lock = single driver). A headless conductor
cannot ask you questions; they land in the mailbox as `question` messages. Flips never happen
mid-brief: a slice finishes where it started, and hand-back is delayed by at most one brief.

## Background workers and supervisor

Write a bounded project brief first. From the conductor, `worker_start` accepts `brief`, `harness`
(`claude` or `codex`), optional `lane`, `model`, `effort`, `resume` and `breadth`. Workers maintain
lane locks/state/logs and are instructed to publish a report and mailbox release. Check actual
state/messages rather than assuming every successful run published its report.

Start supervision with `supervisor` action `start`; inspect `status`, stop it with `stop`, and
acknowledge a reminder with action `ack` plus its event ID. Stopping the supervisor does not stop
workers. The shell worker script also has `stop --lane <lane>`.

Optional wake channels are configured in the **project's** `.orchestrator/supervisor.json`:

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

Choose only channels you need and replace placeholders. `command` channels can also invoke an
explicit `argv` array; treat that file as executable configuration. The supervisor attempts a
mailbox notification before external delivery; wake delivery is best-effort, not exactly-once.
Acknowledgement means receipt, not acceptance/completion. Deferred-worker redispatch defaults on
when `auto_redispatch_deferred` is omitted; the example disables it to avoid surprise work after
a reset. Enable it deliberately only after verifying the relevant bridge.

## Layout and state

```text
checkout/
  AGENTS.md, opencode.jsonc     operating contract and native fallback config
  agents/, commands/, skills/  role prompts and conductor workflow
  plugins/orchestrator.ts      tools, budget injection, model synchronization
  orchestrator/
    models.json                routing/configuration source of truth
    bin/, lib/                 CLI adapters, usage, routing and coordination
    tests/                     fast runtime unit tests (doctor runs these)
  tests/install.test.mjs        separate installer integration matrix
  install.sh, uninstall.sh     reversible config management
```

Each work project's private `.orchestrator/` holds `STATE.md`, `plans/`, `briefs/`, `reports/`,
`reviews/`, `manifests/`, `mailbox/`, `runs/`, `workers/`, `logs/` and supervisor state/config.
Keep it out of that project's Git history too. This checkout's `.gitignore` excludes
`.orchestrator/`, `service.json`, log files and `node_modules/`.

Runtime usage cache is separately stored at `~/.local/state/opencode/orchestrator/usage.json`;
the runtime does not retarget that path through `XDG_STATE_HOME`. Installer receipts/backups use
the different `opencode-orchestrator` state directory described above.

## Incident log and keep-awake

Orchestrator defects (tool/contract mismatches, misroutes, budget surprises, bridge crashes, flips,
and corrections the human gives about the workflow) are appended, redacted, to
`~/.local/state/opencode/orchestrator/incidents.jsonl` by the bridges, worker, supervisor, plugin and
the `orch_incident` tool. `incident.mjs report` groups them by fingerprint; the audit skill turns
repeats into fixes. Nothing is uploaded: `incident.mjs share --out file.json` writes a sanitized
bundle you can attach to an issue by hand if you choose.

Detached workers and the supervisor are wrapped in `caffeinate -i -s` on macOS (`systemd-inhibit`
on Linux) so long runs survive idle sleep; the assertion ends with the process. A laptop lid still
sleeps the machine unless it is in clamshell mode. Disable with `runtime.keep_awake: false` in
`orchestrator/models.json`.

## Audit and troubleshoot

Run `/audit-orchestrator` when models or CLI versions change. It loads `skills/orchestrator-audit`,
checks official sources/live catalogs, updates registry/config, and calls for tests and smoke runs.
Some smoke runs spend model quota: respect budget and approval rules. The retained audit skill has
legacy examples that assume the global config is a Git repo; with managed links, do Git operations
in the checkout instead, and review any suggested service/commit commands before executing them.

Start diagnostics with:

```sh
node ~/.config/opencode/orchestrator/bin/doctor.mjs
node ~/.config/opencode/orchestrator/bin/usage.mjs --refresh
```

The doctor checks binaries, login/feed availability, routing, config/plugin/agents, fast unit tests
and recent plugin errors. Expect `OK` rows, including `OK unit tests — pass 14`. `WARN` can indicate
unclear login or recent plugin errors; `FAIL` produces a nonzero exit. An all-OK doctor does not
test an actual delegated model turn, installation reversal or long unattended operation.

| Symptom                     | Check                                                                                                                                                                |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Missing binary/login        | Install the CLI, then use its own login flow; do not paste tokens into configuration                                                                                 |
| Claude usage unavailable    | Check Claude Code login and keychain/credential access; do not share credential contents                                                                             |
| Codex usage unavailable     | Check Codex login and `app-server` compatibility                                                                                                                     |
| Routing paused              | Inspect `/usage` and reset times; both vendors may be constrained                                                                                                    |
| Plugin/agents missing       | Check managed links, `opencode plugin list`, `opencode debug agents`, then reload                                                                                    |
| Installer refusal/failure   | Preserve private receipt/backup, reconcile changes, then use the recovery instructions above                                                                         |
| Doctor unit-test row fails  | Run `node --test orchestrator/tests/*.test.mjs` in the checkout for details; installer tests belong in root `tests/`, outside the doctor's 20-second unit-test limit |
| Codex bridge argument error | See known issue below; an available usage feed is not proof that exec/review flags work                                                                              |

## Security and data access

Read the scripts before granting access to an account or repository:

- Claude usage collection first checks legacy opencode Anthropic OAuth auth files. It then tries
  the macOS keychain entry named `Claude Code-credentials` and a Claude Code credentials-file
  fallback. It sends a bearer token to `https://api.anthropic.com/api/oauth/usage` and normalizes
  usage windows; it does not intentionally write that token into the usage cache.
- Codex usage collection starts local `codex app-server` over stdio and requests `account/read`
  and `account/rateLimits/read`. Codex owns authentication and its vendor network traffic; this
  repository does not implement a separate OpenAI HTTP usage client.
- Provider availability checks inspect local opencode credential metadata/legacy auth storage or
  CLI login status. Usage cache includes account-specific plan/tier/limit information: keep it private.
- Delegation sends the brief/prompt and selected repository context to the selected model through
  opencode or a vendor CLI. Those harnesses can run tools, MCP servers, shell commands and web
  requests. Audit/research also visits documentation/catalog hosts. **There is no blanket promise
  that all traffic goes only to Anthropic and OpenAI.**
- Project run records can contain prompt excerpts, final responses, commands, changed paths and
  resumable identifiers; logs/mailbox/reports/backups may contain private content. Ignoring files
  in Git is not access control or automatic redaction. Restrict local access and sanitize anything
  you share. Never commit credentials, runtime state or recovery backups.

Review permissions for every delegated harness. The config asks for approval on pushes, but
separate CLI/tool permissions still matter. Manifests identify released bytes; they do not prove
that a review occurred, that those bytes are safe or that a model will follow instructions.

## Verification and limitations

- **Verified:** 14 fast routing/supervisor unit tests; 29 separate temporary-HOME installer cases
  with stubbed external commands; strict registry JSON; syntax/privacy checks; initial packaging
  environment health checks, not a fresh verification of the current live environment. Installer
  integration tests do not touch real config or authenticate a model.
- **Observed initial packaging issue:** Codex exec delegation on codex-cli 0.162.1 failed argument
  parsing (exit 2) before a model turn. This is a historical observation from the initial packaging
  environment, not a newly reproduced bug on the latest live CLIs. The retained bridge is unchanged.
  Do not assume `/delegate-codex`, Codex-backed workers or the separate review invocation are
  validated because the doctor/usage feed passes. Native OpenAI subagents are a separate path.
- **Not verified:** a real install over an existing user config, Linux/Node 22 execution, all
  bridge/review combinations, arbitrary storage/power failures, or long unattended runs on large
  repositories. Independent cross-vendor release review and publication are separate gates.

Run both suites from the checkout; installer integration is intentionally outside the doctor's
fast unit-test set, not omitted from release verification:

```sh
node --test orchestrator/tests/*.test.mjs
node --test tests/*.test.mjs
```

## Design notes

Policy stays in one JSON registry; pure routing logic is testable without account access. Small
CLI adapters isolate vendor interfaces, while project-local files carry resumable state across
harnesses. Managed links separate published code from global runtime writes; private receipts and
archive-first removal favor recoverability over aggressive cleanup. Budget-aware routing and
cross-vendor review are deliberate guardrails, not substitutes for human approval and real tests.

Licensed under [MIT](LICENSE). See [CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).
