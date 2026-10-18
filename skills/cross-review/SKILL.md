---
name: cross-review
description: The reciprocal, cross-vendor code review protocol: hashed source manifests, severity-graded findings with failure scenarios, author responses, reviewer closure on a new fingerprint, no self-approval. Load when reviewing or when preparing code for review.
---
# Cross-vendor review protocol

Code written by an Anthropic model is reviewed by an OpenAI model and vice versa; the conductor picks
the reviewer from the budget routing. A second same-vendor pass is optional, never a substitute.

## Author releases
1. Run the project's formatter, typecheck and tests; record exact commands + results.
2. `orch_manifest_create(task, author, paths)` for every changed/added file (and the tests).
3. Report at `.orchestrator/reports/<task>.md`: outcome, files, checks (with results), not done, open
   questions. Post `release` to the mailbox with refs to report + manifest.

## Reviewer
1. `orch_manifest_verify` before reading and again before writing the verdict; if it fails, stop and
   ask for a re-release.
2. Read every file in the manifest that changed (diff the manifest against the baseline if given).
3. Correctness lenses: inputs/validation, state ownership and lifecycle, concurrency/races, time and
   time zones, error paths and boundaries, security and privacy, deterministic ordering, resource
   leaks. Craft lenses: naming, cohesion, dead code, duplicated truths, abstraction proportionality,
   test quality (behaviour, not implementation), readability of the diff as a whole.
4. Run the tests and any cheap probe that would confirm a suspected bug. Say what you did not run.
5. Findings: `R1 high|medium|low — <file:line> — <failure scenario or maintenance cost> — <fix>`.
   No unexplained green verdicts; no style nits without a concrete cost.
6. Docs: if the change alters commands, flags, layout, install or behaviour and README/CHANGELOG were not updated, that is a finding (medium).
7. Verdict: pass / revise / partial (name the untestable requirement). Write to
   `.orchestrator/reviews/<task>.md`; post `review` to the mailbox.

## Closure
Author maps each finding to a change (+ test) or a reasoned disagreement, releases a new manifest
(v2). Reviewer rechecks only the fingerprint delta plus anything the fixes could have broken, and
records closure. High/medium correctness findings must be resolved or explicitly accepted with
evidence by the human.
