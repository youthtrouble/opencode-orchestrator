---
name: clean-code
description: The craft bar for every coder role (Opus 5.5, GPT-6.1 Sol): clean, maintainable, beautiful code; conventions, tests, error handling, naming, and the report format. Load before implementing any brief.
---
# Craft bar

- Match the project's conventions (formatter, lint, structure, naming) before your own preferences.
- Names explain intent; functions do one thing; files have one reason to change.
- Explicit error boundaries: typed errors, no silent catches, no swallowed promises; fail loudly at
  the edge, keep the core pure.
- No speculative abstraction, no feature flags "for later", no dead code, no duplicated sources of
  truth, no non-null assertions where the type can express the invariant.
- Tests protect behaviour and failure boundaries; table-driven where it helps; deterministic; no
  network in unit tests.
- Comments explain constraints and trade-offs, not syntax. Public contracts documented at the
  definition.
- Small, scoped diffs: one brief, one slice. Leave the campsite cleaner only where you touched.
- Run formatter, typecheck/build, tests; paste real results. Report: outcome; files changed; checks
  run; NOT done; surprises/brief errors. Never claim verification you did not perform.
