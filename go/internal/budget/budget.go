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
	// CreditFunded allows red OpenAI work that the owner's openai_credits=on funds from ChatGPT
	// credits; CreditsBefore is the balance then (JSON null when unlimited).
	CreditFunded  bool
	CreditsBefore any
	// Routing is the routing the decision was made on; nil when usage was unavailable.
	Routing *usage.Routing
}

// JSON returns {allowed, level, resets_at, note, credit_funded, credits_before} with unset
// fields omitted: the gate object without its routing.
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
	if d.CreditFunded {
		out.Set("credit_funded", true)
		out.Set("credits_before", d.CreditsBefore)
	}
	return out
}

// Gate mirrors budgetGate: allowed unless the vendor's overall level (frontier level with
// frontier set) is red and force is unset. Unknown usage, or routing that cannot be computed,
// lets the work proceed with a note. A codex spawn failure is returned as an error: in the
// JS it crashes the process before the gate can catch it.
//
// mayDrawCredits says the caller records credit-funded runs (the balance before and after),
// so a red, non-frontier OpenAI window whose work routing funds from ChatGPT credits is
// allowed instead of refused, once a fresh reading confirms it (see confirmOnFreshReading).
func Gate(cfg usage.Config, vendor string, force, frontier, mayDrawCredits bool) (Decision, error) {
	r, err := usage.GetRouting(cfg, false)
	var spawnErr *usage.SpawnError
	if errors.As(err, &spawnErr) {
		return Decision{}, err
	}
	if err != nil {
		return Decision{Allowed: true, Note: "usage unavailable (" + err.Error() + "); proceeding"}, nil
	}
	request := gateRequest{vendor, force, frontier, mayDrawCredits}
	if decision := request.decide(r); !decision.CreditFunded {
		return decision, nil
	}
	return request.confirmOnFreshReading(cfg)
}

// gateRequest is what a caller asks of the gate, decided on one routing or another.
type gateRequest struct {
	vendor                          string
	force, frontier, mayDrawCredits bool
}

func (g gateRequest) decide(r *usage.Routing) Decision {
	p := r.Resolved.Postures.ByVendor(g.vendor)
	if p == nil || !p.Available {
		return Decision{Allowed: true, Note: g.vendor + " usage unknown; proceeding", Routing: r}
	}
	level := p.Overall
	if g.frontier {
		level = p.Frontier
	}
	if level == routing.Red && !g.force && g.mayDrawCredits && !g.frontier && p.CreditFunded {
		return Decision{
			Allowed:       true,
			Level:         level,
			Note:          g.vendor + " budget is red; drawing ChatGPT credits (" + r.CreditsPolicy.Text() + ").",
			CreditFunded:  true,
			CreditsBefore: CreditsBalance(p.Detail),
			Routing:       r,
		}
	}
	if level == routing.Red && !g.force {
		resets, present := resetsAt(g.vendor, p.Detail)
		return Decision{
			Allowed:     false,
			Level:       level,
			ResetsAt:    resets,
			HasResetsAt: present,
			Note:        g.vendor + " budget is red; refusing to start (pass --force to override). Resets " + jsjson.TemplateStringOf(resets, present) + ".",
			Routing:     r,
		}
	}
	return Decision{Allowed: true, Level: level, Routing: r}
}

// confirmOnFreshReading mirrors confirmOnFreshReading in orchestrator/lib/budget.mjs: the
// cached reading behind routing can be minutes old, so a credit draw (OpenAI only) is decided
// again on a fresh one, which checks the floor against the balance actually left. With no
// fresh Codex reading the draw is refused (fail closed), as credits are money.
func (g gateRequest) confirmOnFreshReading(cfg usage.Config) (Decision, error) {
	r, err := usage.GetRouting(cfg, true)
	var spawnErr *usage.SpawnError
	if errors.As(err, &spawnErr) {
		return Decision{}, err
	}
	if err != nil {
		return g.refuseUnconfirmedDraw(err.Error(), nil), nil
	}
	codex := jsjson.Path(r.Usage, "codex")
	readError, present := jsjson.Lookup(codex, "read_error")
	if !jsjson.Truthy(jsjson.Path(codex, "ok")) || jsjson.Truthy(readError) {
		if readError == nil { // codex?.read_error ?? codex?.error
			readError, present = jsjson.Lookup(codex, "error")
		}
		return g.refuseUnconfirmedDraw(jsjson.TemplateStringOf(readError, present), r), nil
	}
	return g.decide(r), nil
}

func (g gateRequest) refuseUnconfirmedDraw(why string, r *usage.Routing) Decision {
	return Decision{
		Allowed: false,
		Level:   routing.Red,
		Note:    g.vendor + " budget is red and the ChatGPT credits balance could not be re-read (" + why + "); refusing to draw credits (pass --force to override).",
		Routing: r,
	}
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

// CreditsBalance is the ChatGPT credits balance of a Codex usage entry, nil (JSON null) when
// there is none to show (unlimited credits, or nothing reported).
func CreditsBalance(codex any) any {
	if credits := routing.OpenAICredits(codex); credits != nil && credits.Balance != nil {
		return *credits.Balance
	}
	return nil
}

// FreshCreditsBalance is freshCreditsBalance: the balance from a fresh usage reading, for the
// run record of a credit-funded run; nil when it cannot be read (a failed refresh keeps the
// last good reading, marked read_error, whose balance is stale).
func FreshCreditsBalance(cfg usage.Config) any {
	current, err := usage.Get(cfg, true)
	if err != nil {
		return nil
	}
	codex := jsjson.Path(current, "codex")
	if jsjson.Truthy(jsjson.Path(codex, "read_error")) {
		return nil
	}
	return CreditsBalance(codex)
}
