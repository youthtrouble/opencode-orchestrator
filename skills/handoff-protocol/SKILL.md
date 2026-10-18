---
name: handoff-protocol
description: How state travels between sessions, harnesses and humans in an orchestrated project: .orchestrator/STATE.md cursor, briefs, reports, manifests, mailbox messages, resumable session/thread ids. Load when starting, pausing or resuming work, or when a usage limit is near.
---
# Handoff protocol

Everything a cold reader needs lives under `<project>/.orchestrator/`:

```
.orchestrator/
  STATE.md            continuation cursor (single source of truth; replace, don't append forever)
  plans/<slug>.md     frontier plans with bounded briefs
  briefs/<ID>-<slug>.md   one bounded unit of work for a worker
  reports/<task>.md   author report (outcome, files, checks, not done)
  reviews/<task>.md   reviewer findings + closure
  manifests/<task>.json   sha256 of released files (+ baseline)
  mailbox/            messages.jsonl / acks.jsonl (post, list, ack)
  runs/               one JSON record per delegated run (harness, model, tokens, cost, resume id)
  workers/            <lane>.json state + <lane>.lock for background workers
  logs/               worker and supervisor logs (0600)
  supervisor.json     wake channels for the supervisor
```

## STATE.md template (keep under 80 lines)
```
# STATE — <project> — <date>
Goal: <one line>
Budget: <claude session/fable %, codex 5h/weekly %, from orch_usage_status>
## Done this session
- <file/commit>: <what>
## Half-done
- <exact file/function and what remains>
## Next action (exact)
<one imperative sentence, with the command or delegate call>
## Resumable
- claude session <uuid>: knows <what>
- codex thread <uuid>: knows <what>
- worker lane <name>: <status>
## Checks
green: … | red: … | not run: …
## Open questions for the human
- …
```

## Before a stopping boundary (usage limit, end of session)
1. Finish the current atomic step or revert to a clean point; never leave a half-applied edit.
2. Update STATE.md; post a `handoff` mailbox message naming the next action.
3. If a worker is running, leave it; the supervisor will wake the coordinator.

## Resuming
Read STATE.md and the unacked mailbox, verify claims against `git status` and the tests, acknowledge
what you processed, then execute the exact next action. Prefer `resume` ids over re-explaining.
