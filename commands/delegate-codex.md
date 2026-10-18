---
description: Hand a bounded task to the Codex CLI on the ChatGPT subscription (GPT-6.1 Sol by default; its own sandboxed harness)
agent: conductor
---
Delegate this to Codex with `orch_delegate(harness="codex", role=<choose: coder|planner|reviewer|verifier>)`: $ARGUMENTS

Write a self-contained brief (goal, acceptance criteria, paths in scope, constraints, what to report
back). Use effort xhigh/max (or model gpt-6-astra) only for genuinely hard, long-running work and only
when the Codex 5h window is green. Return the result verbatim plus the thread id.
