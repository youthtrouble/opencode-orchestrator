---
description: Resume from .orchestrator/STATE.md and the mailbox; pick up the exact next action
agent: conductor
---
Continue the project from its recorded state. $ARGUMENTS

!`cat .orchestrator/STATE.md 2>/dev/null || echo "(no STATE.md yet)"`

Unacknowledged mailbox messages:
!`node ~/.config/opencode/orchestrator/bin/mailbox.mjs list --unacked-by coordinator --limit 20 2>/dev/null || echo "(no mailbox)"`

Worker lanes:
!`node ~/.config/opencode/orchestrator/bin/worker.mjs status 2>/dev/null || echo "(no workers)"`

Verify the recorded facts against the actual tree (git status, tests) before acting. Acknowledge
mailbox events you have processed. Then execute the exact next action from STATE.md following the loop.
