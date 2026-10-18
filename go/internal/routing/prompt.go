package routing

import (
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// SummarizeForPrompt is the budget and routing summary injected into agent prompts: one line
// per vendor, then one line per role, then the reasons.
func SummarizeForPrompt(resolved Resolved, usage *jsjson.Object) string {
	lines := []string{
		claudeLine(jsjson.Path(usage, "claude"), &resolved.Postures.Anthropic),
		codexLine(jsjson.Path(usage, "codex"), &resolved.Postures.OpenAI),
		"Routing now:",
	}
	for _, a := range resolved.Roles {
		lines = append(lines, roleLine(a))
	}
	for _, reason := range resolved.Reasons {
		lines = append(lines, "  note: "+reason)
	}
	return strings.Join(lines, "\n")
}

func claudeLine(claude any, posture *VendorPosture) string {
	if !jsjson.Truthy(jsjson.Path(claude, "ok")) {
		return "Claude usage: unavailable (" + jsjson.OrText(jsjson.Path(claude, "error"), "no data") + ")."
	}
	line := "Claude (" + jsjson.OrText(jsjson.Path(claude, "plan"), "subscription") + "): " +
		"session " + nullishText(jsjson.Path(claude, "session", "pct"), "?") + "% [" + jsjson.TemplateStringOf(posture.Window("session")) + "] " +
		"resets " + jsjson.OrText(jsjson.Path(claude, "session", "resets_at"), "?") + "; " +
		"weekly " + nullishText(jsjson.Path(claude, "weekly", "pct"), "n/a") + "%"
	if fable := jsjson.Path(claude, "weekly_by_model", "fable"); jsjson.Truthy(fable) {
		line += "; Fable weekly " + jsjson.TemplateStringOf(jsjson.Lookup(fable, "pct")) + "% [" + jsjson.TemplateStringOf(posture.Window("fable")) + "] " +
			"resets " + jsjson.TemplateStringOf(jsjson.Lookup(fable, "resets_at"))
	}
	return line + "."
}

func codexLine(codex any, posture *VendorPosture) string {
	if !jsjson.Truthy(jsjson.Path(codex, "ok")) {
		return "Codex usage: unavailable (" + jsjson.OrText(jsjson.Path(codex, "error"), "no data") + ")."
	}
	return "Codex (" + jsjson.OrText(jsjson.Path(codex, "plan"), "subscription") + "): " +
		"5h " + jsjson.TemplateStringOf(jsjson.Lookup(codex, "five_hour", "pct")) + "% [" + jsjson.TemplateStringOf(posture.Window("five_hour")) + "] " +
		"resets " + jsjson.TemplateStringOf(jsjson.Lookup(codex, "five_hour", "resets_at")) + "; " +
		"weekly " + jsjson.TemplateStringOf(jsjson.Lookup(codex, "weekly", "pct")) + "% [" + jsjson.TemplateStringOf(posture.Window("weekly")) + "] " +
		"resets " + jsjson.TemplateStringOf(jsjson.Lookup(codex, "weekly", "resets_at")) + "."
}

func roleLine(a Assignment) string {
	r := a.Entry
	if r == nil {
		return "  " + a.Role + ": PAUSED"
	}
	line := "  " + a.Role + ": " + r.Model
	if jsjson.Truthy(r.Variant) {
		line += "#" + jsjson.TemplateString(r.Variant)
	}
	if jsjson.Truthy(r.Effort) {
		line += " (effort " + jsjson.TemplateString(r.Effort) + ")"
	}
	line += " via " + r.Via
	if r.CrossVendor != nil && !*r.CrossVendor {
		line += " [same-vendor review: cross vendor unavailable]"
	}
	return line
}

// nullishText is `${v ?? fallback}`.
func nullishText(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	return jsjson.TemplateString(v)
}
