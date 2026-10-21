---
name: orchestrator
description: Use the global two-vendor orchestrator from Claude Code: check live Claude + Codex budgets, delegate bounded work to Codex (GPT-6.1 Sol / Astra) or to a fresh Claude Code session, run background workers with the supervisor, and use the project mailbox/manifests. Use when the user mentions budgets, limits, delegating to Codex, cross-vendor review, or the orchestrator.
---
# Orchestrator from Claude Code

All tooling lives in `~/.config/opencode/orchestrator` (README there). Shell commands:

- Budget + routing: `node ~/.config/opencode/orchestrator/bin/usage.mjs --refresh` (`--json` for machine use).
  Session %, Fable weekly % (Claude), 5h/weekly % (Codex); red = don't start work on that vendor.
- Delegate to Codex: `node ~/.config/opencode/orchestrator/bin/codex-exec.mjs --model gpt-6.1-sol --effort high --sandbox workspace-write --cwd <repo> --prompt-file brief.md`
  (returns JSON with `thread_id` to `--resume`, usage, files changed; exit 75 = budget red).
- Codex native review of the diff: `codex review --uncommitted` (or `--base main`).
- Delegate to a separate Claude session: `node ~/.config/opencode/orchestrator/bin/claude-exec.mjs --model claude-opus-5-5 --effort high --permission-mode acceptEdits --cwd <repo> --prompt-file brief.md`.
- Background worker + supervisor: `worker.mjs start --brief .orchestrator/briefs/X.md --harness codex --detach --breadth standard`, `supervisor.mjs start`, `supervisor.mjs status|ack --event ID`.
- Answer a headless conductor: `mailbox.mjs inbox`, then `mailbox.mjs reply <id> "answer"`. Autonomy: `policy.mjs show|unattended`.
- Mailbox: `mailbox.mjs post --from claude --to coordinator --kind release --subject "..." --ref path`, `mailbox.mjs list --unacked-by claude`.
  A question carries the reply you recommend and whether it blocks you:
  `mailbox.mjs post --from claude --to human --kind question --subject "..." --body "..." --recommended "..." --blocking true|false`.
  Recommended answers are for decisions only; to ask the human to DO something, post `--ask action` with the steps in
  `--body` and no `--recommended`, and verify the state any reply claims before relying on it.
- Session heartbeat (start of a task, every brief boundary): `session.mjs beat --door claude --now "<one line>" --slice <id> --progress <done>/<total>` (`--state done` when the plan is finished); `session.mjs show` reads it.
- Manifests: `manifest.mjs create --task T --author claude --out .orchestrator/manifests/T.json <paths>`; `manifest.mjs verify file.json`.

Conventions (same as the opencode side): frontier models plan, Opus 5.5 / Sol write, the other
vendor reviews on a hashed manifest, the verifier runs the real checks, STATE.md records the exact
next action. Briefs are self-contained; a delegate cannot see this conversation.
