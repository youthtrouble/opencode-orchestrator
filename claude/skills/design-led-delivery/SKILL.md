---
name: design-led-delivery
description: The owner's preferred way to build products with a UI - research, then a PM brief and a designer direction, then parallel builders on bounded slices, one shared real build, scored design reviews with frame-by-frame motion checks, and fast fix rounds onto a real device. Use when starting or redirecting any product/UI work, planning a release, or coordinating several builders; also defines the review gates, evidence rules and machine hygiene that go with it.
---
# Design-led delivery

Adopted by the owner on 2026-10-24 after Dawdling v2 went from "looks like a webpage" to a native app on
their phone overnight: "I like the new direction/way of working. Need to bake this style of work in."
The bar keeps rising: aim above the last review score every round.

## Roles
- **Product manager** (`product-manager` agent): the job to be done, moments that matter, one-page brief,
  must/should/could/won't, metrics with counter-metrics, ordered bounded build briefs, decision log.
- **Product designer** (`product-designer` agent / `product-design` skill): one ownable idea, direction,
  exact spec and code-ready tokens, motion/haptic table, every failure state; later the scored reviews.
- **Builders** (`coder-hard`/`coder`): one bounded slice each, owning distinct files.
- **Integrator** (a builder): merges all slices into one branch, resolves conflicts semantically, keeps
  checks green.
- **Independent reviewer** (other vendor / conductor): source review for truth and correctness.
- **Owner session** (you): routes findings, unblocks, runs the shared build, keeps the owner informed.

## The loop
1. **Research in parallel** (category leaders, platform capabilities, skills people vouch for, market and
   pricing). Write findings with cited sources into the project's plan folder.
2. **PM brief and designer direction** next; the designer reads the PM brief and research. Record owner
   decisions as numbered amendments the moment they're made.
3. **Fan out builders on bounded slices** with explicit ownership, acceptance criteria the designer can
   check, and a freeze time. One foundation slice owns all native/config/package changes.
4. **One shared real build.** A single integration branch and one simulator/device build that everyone
   reviews; builders don't each build natively. Capture scripts must wait for the real first frame (never
   the splash) and record motion.
5. **Scored design review** of real screens in light/dark, default and largest text, plus frame-by-frame
   recordings: score 0-100, category scores, hard fails, short ranked fixes with exact values and owner area.
6. **Fix rounds** (v2.1, v2.2, ...) routed straight to builders; rebuild onto the owner's phone after each
   batch; repeat the review. The pass bar is ≥90 with no hard fail and truth 5/5.
7. **Never idle.** When a slice finishes, start the next; when reviews land, route them within minutes.
   Pipeline research → plan → build → review so no role waits.

## Rules that travel with it
- Truth beats beauty: never invent data, never show stale as live; then craft everywhere else.
- Evidence or it didn't happen: real screenshots/recordings; say plainly what is unverified.
- Show the owner real progress (screens on their phone, a page with screenshots), not gates passed.
- Owner-only: money, secrets, production deploys, App Store, paid accounts. Ask once with the exact
  one-line command; everything else proceeds under standing approval.
- Machine hygiene: APFS-cloned node_modules (`cp -cR`) instead of per-builder installs; delete each
  worktree and build folder as soon as it's pushed; hourly cleanup; never kill processes you didn't start.
- Usage limits happen: resume every builder the moment the limit resets, re-plan the freeze honestly.
