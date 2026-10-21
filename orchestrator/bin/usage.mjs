#!/usr/bin/env node
// Usage: usage.mjs [--refresh] [--json] [--brief]
import { openaiCredits } from "../lib/routing.mjs";
import { getRouting } from "../lib/usage.mjs";

const args = new Set(process.argv.slice(2));
const result = await getRouting({ refresh: args.has("--refresh") });
if (args.has("--json")) {
  console.log(JSON.stringify({ generated_at: result.usage.generated_at, stale: Boolean(result.usage.stale), channels: result.channels, usage: result.usage, postures: result.resolved.postures, roles: result.resolved.roles, reasons: result.resolved.reasons, paused: result.resolved.paused, openai_credits: openaiCredits(result.usage.codex), openai_credits_policy: result.creditsPolicy }, null, 2));
} else {
  console.log(result.prompt);
  if (result.usage.stale) console.log("(cached usage is stale; pass --refresh)");
}
