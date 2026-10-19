# Claude Code front door

`claude --agent conductor` is the Anthropic-native conductor. It mirrors the opencode conductor and
shares everything with it: the project's `.orchestrator/` state, the mailbox, manifests, the
autonomy policy and the scripts in `~/.config/opencode/orchestrator/bin`. Claude roles run as Claude
Code subagents; OpenAI roles (cross-vendor review, Sol/Astra work) go through the Codex bridge.

## Install

Install the main orchestrator first (`./install.sh` in the checkout root), then:

```sh
./install-claude.sh              # symlink claude/agents/*.md and claude/skills/orchestrator into ~/.claude
./install-claude.sh --uninstall  # remove only the links that point into this checkout
```

The target is `$CLAUDE_CONFIG_DIR`, else `~/.claude`. Links only; a real file already at a target
is skipped with a message, never replaced. Safe to re-run.

## Agents

| Agent | Model, effort | Used for |
|---|---|---|
| `conductor` | Opus 5.5, medium | Plans, delegates, tracks state; edits only `.orchestrator/**` and docs |
| `architect` | Fable 5.1, high | Medium and large planning |
| `architect-hard` | Fable 5.1, max | Hard problems: root causes, concurrency, long-lived architecture |
| `coder` | Opus 5.5, medium | Trivial to medium slices |
| `coder-hard` | Opus 5.5, xhigh | Large or hard slices |
| `verifier` | Sonnet 5.5, low | Runs the real checks and tests every claim in a report |
| `researcher` | Sonnet 5.5, low | Official docs and current facts, with citations |
| `reviewer` | Sonnet 5.5, medium | Same-vendor fallback review, only when the OpenAI window is red |

Normal review is cross-vendor: `codex-exec.mjs --sandbox read-only` with GPT-6.1 Sol, or
`codex review --uncommitted`. Exploration uses Claude Code's built-in `Explore` agent.

## Skill

`skills/orchestrator/SKILL.md` teaches any Claude Code session (not only the conductor) the shell
commands for budgets, delegation to Codex or a fresh Claude session, workers, the supervisor, the
mailbox and manifests. It loads when you mention budgets, limits, delegation to Codex,
cross-vendor review or the orchestrator.

The opencode-side skills (`deep-planning`, `clean-code`, `cross-review`, `handoff-protocol`,
`delegation`, `orchestrator-audit`) live in the root `skills/` directory and are installed by
`./install.sh`, not by this script.

## Headless use

`kickoff.mjs --door claude --headless` and `flip.mjs run --to claude` start this conductor as the
worker lane `conductor` through `claude-exec.mjs` with `--permission-mode auto`. A headless
conductor cannot ask you anything: questions land in the mailbox. See
[orchestrator/README.md](../orchestrator/README.md#flipping-between-the-two-doors) for the flip
rules.
