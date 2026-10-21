// The effective autonomy policy of a project: the registry's autonomy block (without its
// $comment), overridden key by key by <project>/.orchestrator/policy.json.
import fs from "node:fs";
import path from "node:path";

export const policyFile = (projectRoot) => path.join(projectRoot, ".orchestrator", "policy.json");

// The override file as parsed, or {} when it cannot be read or parsed.
export function readPolicyOverrides(projectRoot) {
  try { return JSON.parse(fs.readFileSync(policyFile(projectRoot), "utf8")); } catch { return {}; }
}

export function effectivePolicy(projectRoot, registry) {
  const base = { ...(registry.autonomy || {}) };
  delete base.$comment;
  return { ...base, ...readPolicyOverrides(projectRoot) };
}
