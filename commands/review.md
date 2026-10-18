---
description: Cross-vendor review of the current changes (Codex review for Anthropic-written code, Sonnet/Opus for OpenAI-written code)
agent: conductor
---
Review the current working-tree changes: $ARGUMENTS

Changed files:
!`git status --short 2>/dev/null | head -50`

Diff stat:
!`git diff --stat 2>/dev/null | tail -20`

Decide who wrote this code (check `.orchestrator/STATE.md` / recent run records; if unknown, assume
me/Anthropic) and run the OTHER vendor as reviewer: `orch_codex_review` (uncommitted) or
`orch_delegate(role="reviewer", harness="claude")`. Also run `orch_delegate(role="verifier")` to
execute the tests and typecheck. Return a merged findings list with severity, file:line, failure
scenario and concrete fix, then a verdict (pass / revise / partial). Do not edit code.
