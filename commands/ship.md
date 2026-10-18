---
description: Take an idea end to end: plan, build slice by slice with Opus 5.5 / GPT-6.1 Sol, cross-vendor review, verify, record
agent: conductor
---
Ship this end to end: $ARGUMENTS

Follow the full loop from the global AGENTS.md. Plan first (frontier planner) unless a current plan in
`.orchestrator/plans/` already covers it. Then for each bounded brief in order: delegate to the coder
role, get a manifest, run the cross-vendor reviewer on it, have the author fix findings, close on a
new manifest, run the verifier on the real checks, record STATE.md. Keep going autonomously across
slices; stop only for a product ambiguity I must decide, a red budget on both vendors (then write the
remaining briefs and tell me when the window resets), or an action needing approval (push, deploy,
spend). Finish with: what shipped, who reviewed what, checks run, not done, next step.
