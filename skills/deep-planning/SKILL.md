---
name: deep-planning
description: The frontier planning method the planner role must follow: tireless, meticulous, evidence-based plans decomposed into bounded, independently reviewable briefs with explicit acceptance criteria, risks and human questions. Load when asked to plan or architect anything non-trivial.
---
# Deep planning

A plan is a contract the coder can execute without talking to you and the reviewer can verify.

1. **Restate** the outcome, the user's intent behind it, acceptance criteria (testable), constraints,
   and non-goals. If the request is ambiguous in a way that changes the design, list the question;
   otherwise choose and state the assumption.
2. **Read the code**, do not guess: entry points, data model, boundaries, conventions, test setup,
   CI, error handling style, existing similar features. Note facts with file paths.
3. **Verify external claims** (APIs, library behaviour, limits) against official docs or a live
   response; mark unverified items `TODO(verify)`.
4. **Options**: 2–3 designs with trade-offs (complexity, blast radius, performance, migration,
   testability); pick one; say why the others lost.
5. **Decompose** into bounded briefs, each: outcome, in-scope paths, out-of-scope, context pointers,
   checks to run, unknowns, size (S/M/L), breadth (focused|standard|broad). Order them so each slice
   is independently reviewable and the system stays working between slices. Thinnest vertical slice
   first.
6. **Adversarial pass**: invalid inputs, concurrency, partial failure, retries/idempotency, time zones
   and DST, large inputs, permissions, secrets, observability, rollback.
7. **Risks and mitigations**, **migration/rollout**, **how we will know it works** (metrics, manual
   checks).
8. **Questions only the human can answer**, batched.

Output to `.orchestrator/plans/<slug>.md`. Be exhaustive in substance and terse in prose; no filler,
no restating the obvious, no code except interfaces/signatures where they pin the design.
