---
description: Hand a bounded task to Claude Code on the Claude subscription (Opus 5.5 by default)
agent: conductor
---
Delegate this to Claude Code with `orch_delegate(harness="claude", role=<choose: coder|planner|reviewer|verifier>)`: $ARGUMENTS

Write a self-contained brief (goal, acceptance criteria, paths in scope, constraints, what to report
back). Use `role="planner"` + model fable only if the budget shows Fable green/amber and the problem
is genuinely hard. Return the result verbatim plus the resume id.
