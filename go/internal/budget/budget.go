// Package budget is the pre-flight budget gate shared by the CLI bridges and the plugin
// (lib/budget.mjs): refuse to start work on a vendor whose usage window is red.
package budget

import (
	"errors"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// Decision is budgetGate's result. Level, ResetsAt and Note are each only sometimes set in
// the JS; an unset one is omitted from the JSON (JS undefined).
type Decision struct {
	Allowed bool
	Level   string // "" when unset
	// ResetsAt is the reset time quoted in a refusal; HasResetsAt is false when the JS value
	// is undefined (JSON null is a present nil).
	ResetsAt    any
	HasResetsAt bool
	Note        string // "" when unset
	// Routing is the routing the decision was made on; nil when usage was unavailable.
	Routing *usage.Routing
}

// JSON returns {allowed, level, resets_at, note} with unset fields omitted: the gate object
// without its routing.
func (d *Decision) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("allowed", d.Allowed)
	if d.Level != "" {
		out.Set("level", d.Level)
	}
	if d.HasResetsAt {
		out.Set("resets_at", d.ResetsAt)
	}
	if d.Note != "" {
		out.Set("note", d.Note)
	}
	return out
}

// Gate mirrors budgetGate: allowed unless the vendor's overall level (frontier level with
// frontier set) is red and force is unset. Unknown usage, or routing that cannot be computed,
// lets the work proceed with a note. A codex spawn failure is returned as an error: in the
// JS it crashes the process before the gate can catch it.
func Gate(cfg usage.Config, vendor string, force, frontier bool) (Decision, error) {
	r, err := usage.GetRouting(cfg, false)
	var spawnErr *usage.SpawnError
	if errors.As(err, &spawnErr) {
		return Decision{}, err
	}
	if err != nil {
		return Decision{Allowed: true, Note: "usage unavailable (" + err.Error() + "); proceeding"}, nil
	}
	p := r.Resolved.Postures.ByVendor(vendor)
	if p == nil || !p.Available {
		return Decision{Allowed: true, Note: vendor + " usage unknown; proceeding", Routing: r}, nil
	}
	level := p.Overall
	if frontier {
		level = p.Frontier
	}
	if level == routing.Red && !force {
		resets, present := resetsAt(vendor, p.Detail)
		return Decision{
			Allowed:     false,
			Level:       level,
			ResetsAt:    resets,
			HasResetsAt: present,
			Note:        vendor + " budget is red; refusing to start (pass --force to override). Resets " + jsjson.TemplateStringOf(resets, present) + ".",
			Routing:     r,
		}, nil
	}
	return Decision{Allowed: true, Level: level, Routing: r}, nil
}

// resetsAt is the window whose reset ends the refusal: the session window, else Fable's
// weekly window, for Anthropic; the 5-hour window, else the weekly one, for OpenAI. It is
// `a || b`, so a falsy a yields b exactly, absent included.
func resetsAt(vendor string, detail any) (any, bool) {
	first, second := []string{"five_hour", "resets_at"}, []string{"weekly", "resets_at"}
	if vendor == routing.Anthropic {
		first, second = []string{"session", "resets_at"}, []string{"weekly_by_model", "fable", "resets_at"}
	}
	if a, present := jsjson.Lookup(detail, first...); jsjson.Truthy(a) {
		return a, present
	}
	return jsjson.Lookup(detail, second...)
}
