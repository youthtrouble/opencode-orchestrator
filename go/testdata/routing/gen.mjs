#!/usr/bin/env node
// Regenerates the routing, flip and supervise goldens by running the JS policies
// (orchestrator/lib/{routing,flip,supervise}.mjs) on the inputs in cases.json with the real
// orchestrator/models.json, or the case's own "registry" where a routing case has one, and the credits setting of
// the case's "policy" (off without one). Writes, with no trailing newline:
//   <case>.resolved.golden  JSON.stringify(resolveRoles(...), null, 2)
//   <case>.prompt.golden    summarizeForPrompt(...)
//   flip.golden             JSON.stringify([decideFlip(...) per flip case], null, 2)
//   supervise.golden        JSON.stringify([{observation, event, reminder} per case], null, 2)
// The Go tests compare the port against these bytes. Run: node go/testdata/routing/gen.mjs
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const LIB = path.join(HERE, "..", "..", "..", "orchestrator", "lib");
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

const supervised = cases.supervise.map((c) => {
  const observation = observe(c.worker, c.now_sec, { alive: c.alive, lockPresent: c.lock_present });
  const event = chooseEvent(observation, c.state, c.acks, c.now_sec);
  return { observation, event, reminder: event ? reminderText(event.id, event.reason, observation, c.project_root) : null };
});
write("supervise.golden", JSON.stringify(supervised, null, 2));

console.log(`wrote goldens for ${cases.routing.length} routing, ${cases.flip.length} flip and ${cases.supervise.length} supervise cases with node ${process.version}`);
