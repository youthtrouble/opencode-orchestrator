---
description: Write the continuation cursor (.orchestrator/STATE.md) so any harness or human can resume cold
agent: conductor
---
Write or update `.orchestrator/STATE.md` now. $ARGUMENTS

Include: date; goal in one line; what changed this session (files, commits); what is half-done and
exactly where; the exact next action; open questions for the human; resumable ids (Claude session
ids, Codex thread ids, worker lanes) with what each one knows; budget snapshot (from
`orch_usage_status`); and which checks are green/red/not run. Keep it under 80 lines; replace stale
content instead of appending forever. Then post a `handoff` message to the mailbox.
