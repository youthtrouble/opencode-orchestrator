---
description: Retrospective on recent delegated runs: cost, failures, review findings, routing quality
agent: conductor
---
Run a retrospective over `.orchestrator/runs/`, `.orchestrator/reviews/` and the mailbox for $ARGUMENTS (default: the last 7 days).

!`ls -t .orchestrator/runs 2>/dev/null | head -40`

Report: runs per harness/model with success rate, tokens and estimated cost; review findings by
severity and how many were real; where routing was wrong (a cheap model struggled, a dear model did
trivial work); budget incidents (deferred/red); and three concrete changes to briefs, routing
thresholds (`~/.config/opencode/orchestrator/models.json`) or the loop.
