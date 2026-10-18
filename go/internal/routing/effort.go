package routing

import "github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"

// EffortFor returns the reasoning effort for a role and task size from
// registry.effort_policy; an unknown size (or "", JS undefined) falls back to the policy's
// default size, then to "medium". ok is false when the policy has no table for the role.
func EffortFor(registry *jsjson.Object, role, size string) (effort string, ok bool) {
	policy := jsjson.Path(registry, "effort_policy")
	table := jsjson.Path(policy, role)
	if !jsjson.Truthy(table) {
		return "", false
	}
	if chosen := jsjson.Path(table, size); jsjson.Truthy(chosen) {
		return jsjson.TemplateString(chosen), true
	}
	defaultSize := jsjson.OrText(jsjson.Path(policy, "default_size"), "medium")
	if chosen := jsjson.Path(table, defaultSize); jsjson.Truthy(chosen) {
		return jsjson.TemplateString(chosen), true
	}
	return "medium", true
}
