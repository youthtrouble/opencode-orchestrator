---
description: Deep plan an idea with a frontier model (Fable 5.1 or GPT-6 Astra by budget) and write it to .orchestrator/plans/
agent: conductor
---
Plan this idea end to end: $ARGUMENTS

Steps: (1) restate outcome, acceptance criteria and constraints; (2) gather repo facts with `explore`
(stack, structure, conventions, tests, CI, existing related code); (3) `orch_delegate(role="planner")`
with the idea + facts + criteria, asking for a plan at `.orchestrator/plans/<slug>.md` with milestones,
bounded briefs (outcome, in-scope paths, checks, unknowns, size), risks, rollback and human questions;
(4) read the plan back critically and fix gaps with one follow-up to the same planner session (`resume`);
(5) write/update `.orchestrator/STATE.md`; (6) give me the plan summary and the questions only I can answer.
Do not start coding.
