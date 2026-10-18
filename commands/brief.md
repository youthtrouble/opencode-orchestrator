---
description: Write a bounded brief file for a worker (.orchestrator/briefs/<id>.md) from a short description
agent: conductor
---
Write a bounded brief for: $ARGUMENTS

Save it as `.orchestrator/briefs/<ID>-<slug>.md` (next free ID). A brief has: Outcome (one paragraph);
Acceptance criteria (testable bullets); In-scope paths and explicitly out-of-scope paths; Context the
worker needs (facts, decisions, links to plan sections, relevant code pointers); Checks to run (exact
commands); Unknowns and how to handle them; Report destination and required report sections; Size
estimate and breadth (focused|standard|broad). Then tell me the `orch_worker_start` call to launch it.
