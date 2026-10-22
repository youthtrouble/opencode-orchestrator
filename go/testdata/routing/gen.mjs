#!/usr/bin/env node
// Regenerates the routing, flip and supervise goldens by running the JS policies
// ($ORCH_JS/lib/{routing,flip,supervise}.mjs) on the inputs in cases.json with the real
// $ORCH_JS/models.json, or the case's own "registry" where a routing case has one, and the credits setting of
// the case's "policy" (off without one). Writes, with no trailing newline:
//   <case>.resolved.golden  JSON.stringify(resolveRoles(...), null, 2)
//   <case>.prompt.golden    summarizeForPrompt(...)
//   flip.golden             JSON.stringify([decideFlip(...) per flip case], null, 2)
//   supervise.golden        JSON.stringify([{observation, event, reminder} per case], null, 2),
//                           each reminder with orch's command lines (PE-19, below)
// The Go tests compare the port against these bytes. Run: ORCH_JS=<tree> node go/testdata/routing/gen.mjs
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

if (!process.env.ORCH_JS) {
  console.error("ORCH_JS is unset; run ORCH_JS=<tree> node gen.mjs, where <tree> is an orchestrator/ tree (git archive $(cat go/parity/js-ref) orchestrator | tar -x -C <dir> gives one)");
  process.exit(2);
}
const HERE = path.dirname(fileURLToPath(import.meta.url));
const LIB = path.resolve(process.env.ORCH_JS, "lib");
const { creditsPolicyOf, resolveRoles, summarizeForPrompt } = await import(path.join(LIB, "routing.mjs"));
const { decideFlip } = await import(path.join(LIB, "flip.mjs"));
const { observe, chooseEvent, reminderText } = await import(path.join(LIB, "supervise.mjs"));

const registry = JSON.parse(fs.readFileSync(path.join(LIB, "..", "models.json"), "utf8"));
const cases = JSON.parse(fs.readFileSync(path.join(HERE, "cases.json"), "utf8"));
const now = Date.parse(cases.now);
const write = (name, text) => fs.writeFileSync(path.join(HERE, name), text);

const resolvedByCase = {};
for (const c of cases.routing) {
  const resolved = resolveRoles(c.usage, c.registry ?? registry, { now, channels: c.channels, credits: creditsPolicyOf(c.policy) });
  resolvedByCase[c.name] = resolved;
  write(`${c.name}.resolved.golden`, JSON.stringify(resolved, null, 2));
  write(`${c.name}.prompt.golden`, summarizeForPrompt(resolved, c.usage));
}

const decisions = cases.flip.map((c) => decideFlip({
  active: c.active,
  postures: resolvedByCase[c.routing].postures,
  registry,
  nowMs: now,
  lastFlipAtMs: c.last_flip_at ? Date.parse(c.last_flip_at) : null,
}));
write("flip.golden", JSON.stringify(decisions, null, 2));

// PE-19 (go/README.md): orch's reminders name `orch <cmd>` where the JS names its installed
// script (mailbox and supervisor), so the golden holds orch's form, as the parity harness maps it.
const asOrchCommands = (text) =>
  text.replace(/node ~\/\.config\/opencode\/orchestrator\/bin\/([a-z-]+)\.mjs/g, "orch $1");

const supervised = cases.supervise.map((c) => {
  const observation = observe(c.worker, c.now_sec, { alive: c.alive, lockPresent: c.lock_present });
  const event = chooseEvent(observation, c.state, c.acks, c.now_sec);
  const reminder = event ? asOrchCommands(reminderText(event.id, event.reason, observation, c.project_root)) : null;
  return { observation, event, reminder };
});
write("supervise.golden", JSON.stringify(supervised, null, 2));

console.log(`wrote goldens for ${cases.routing.length} routing, ${cases.flip.length} flip and ${cases.supervise.length} supervise cases with node ${process.version}`);
