// Package routing is the budget-aware role -> model routing of lib/routing.mjs: pure
// functions over the normalized usage document and the registry (models.json), no I/O.
//
// Both inputs are read as JSON values with JS semantics (truthiness, ?? and template
// coercion), because they are passed through verbatim (a posture's detail is the usage entry
// itself) and the outputs must match the JS byte for byte.
package routing

import (
	"math"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// Posture levels of a vendor or a usage window.
const (
	Green   = "green"
	Unknown = "unknown"
	Amber   = "amber"
	Red     = "red"
)

// Thresholds are registry.thresholds: percent-used levels and the "resets soon" horizon. A
// threshold missing from the registry is NaN, so every comparison with it is false, as with
// undefined in JS.
type Thresholds struct {
	Amber       float64
	Red         float64
	SoonMinutes float64
}

func thresholdsOf(registry *jsjson.Object) Thresholds {
	th := jsjson.Path(registry, "thresholds")
	nan := math.NaN()
	return Thresholds{
		Amber:       jsjson.NumberOr(th, "amber", nan),
		Red:         jsjson.NumberOr(th, "red", nan),
		SoonMinutes: jsjson.NumberOr(th, "soon_minutes", nan),
	}
}

// PostureFromPct maps a window's used percentage (its raw JSON value) to a level. Posture is
// computed from the raw percentage only: an imminent reset never makes a window look greener;
// that rule once launched a conductor into a window with 2% left (incident 2026-10-18).
func PostureFromPct(pct any, th Thresholds, locked bool) string {
	if locked {
		return Red
	}
	if pct == nil {
		return Unknown
	}
	used, isNumber := pct.(float64)
	if !isNumber {
		used = math.NaN()
	}
	switch {
	case used >= th.Red:
		return Red
	case used >= th.Amber:
		return Amber
	default:
		return Green
	}
}

var rank = map[string]int{Green: 0, Unknown: 1, Amber: 2, Red: 3}

// Worst returns the most severe level, starting from green; names outside the four levels
// never win, as their JS rank is undefined.
func Worst(levels ...string) string {
	worst := Green
	for _, level := range levels {
		if r, known := rank[level]; known && r > rank[worst] {
			worst = level
		}
	}
	return worst
}

// Window is one usage window's level, kept in the JS key order of posture.windows.
type Window struct {
	Name  string
	Level string
}

// VendorPosture is one vendor's entry of the postures object.
type VendorPosture struct {
	Available bool
	Overall   string
	Frontier  string
	Windows   []Window
	// ResetsSoon reports that the main window resets within thresholds.soon_minutes. It is
	// only written for available vendors.
	ResetsSoon bool
	// Detail is the vendor's usage entry, verbatim (JSON null when it was absent).
	Detail any
}

// Window returns the level of the named window; ok is false when the posture has no such
// window (JS undefined).
func (p *VendorPosture) Window(name string) (level string, ok bool) {
	for _, w := range p.Windows {
		if w.Name == name {
			return w.Level, true
		}
	}
	return "", false
}

// JSON returns the posture as the JS object, keys in the JS order.
func (p *VendorPosture) JSON() *jsjson.Object {
	windows := jsjson.NewObject()
	for _, w := range p.Windows {
		windows.Set(w.Name, w.Level)
	}
	out := jsjson.NewObject()
	out.Set("available", p.Available)
	out.Set("overall", p.Overall)
	out.Set("frontier", p.Frontier)
	out.Set("windows", windows)
	if p.Available {
		out.Set("resets_soon", p.ResetsSoon)
	}
	out.Set("detail", p.Detail)
	return out
}

// Postures holds the per-vendor postures (JS keys "anthropic" and "openai").
type Postures struct {
	Anthropic VendorPosture
	OpenAI    VendorPosture
}

// Vendor names as used in the registry, postures and role entries.
const (
	Anthropic = "anthropic"
	OpenAI    = "openai"
)

// ByVendor returns the posture of the named vendor, or nil for any other name.
func (p *Postures) ByVendor(vendor string) *VendorPosture {
	switch vendor {
	case Anthropic:
		return &p.Anthropic
	case OpenAI:
		return &p.OpenAI
	}
	return nil
}

// JSON returns the postures object.
func (p *Postures) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set(Anthropic, p.Anthropic.JSON())
	out.Set(OpenAI, p.OpenAI.JSON())
	return out
}

// VendorPostures builds the per-vendor postures from the normalized usage document.
func VendorPostures(usage *jsjson.Object, registry *jsjson.Object, now time.Time) Postures {
	th := thresholdsOf(registry)
	return Postures{
		Anthropic: anthropicPosture(jsjson.Path(usage, "claude"), th, now),
		OpenAI:    openAIPosture(jsjson.Path(usage, "codex"), th, now),
	}
}

func anthropicPosture(claude any, th Thresholds, now time.Time) VendorPosture {
	if !jsjson.Truthy(jsjson.Path(claude, "ok")) {
		return unavailablePosture(claude)
	}
	session := PostureFromPct(jsjson.Path(claude, "session", "pct"), th, jsjson.Truthy(jsjson.Path(claude, "locked_reason")))
	weekly := PostureFromPct(jsjson.Path(claude, "weekly", "pct"), th, false)
	fable := Unknown
	if window := jsjson.Path(claude, "weekly_by_model", "fable"); jsjson.Truthy(window) {
		fable = PostureFromPct(jsjson.Path(window, "pct"), th, false)
	}
	return VendorPosture{
		Available:  true,
		Overall:    Worst(session, knownOrGreen(weekly)),
		Frontier:   Worst(session, knownOrGreen(weekly), knownOrGreen(fable)),
		Windows:    []Window{{"session", session}, {"weekly", weekly}, {"fable", fable}},
		ResetsSoon: resetsSoon(jsjson.Path(claude, "session", "resets_at"), th, now),
		Detail:     claude,
	}
}

// openAIPosture, unlike the Anthropic one, lets an unknown weekly window make the vendor
// "unknown" overall (mirrored from the JS).
func openAIPosture(codex any, th Thresholds, now time.Time) VendorPosture {
	if !jsjson.Truthy(jsjson.Path(codex, "ok")) {
		return unavailablePosture(codex)
	}
	fiveHour := PostureFromPct(jsjson.Path(codex, "five_hour", "pct"), th, jsjson.Truthy(jsjson.Path(codex, "rate_limit_reached")))
	weekly := PostureFromPct(jsjson.Path(codex, "weekly", "pct"), th, false)
	return VendorPosture{
		Available:  true,
		Overall:    Worst(fiveHour, weekly),
		Frontier:   Worst(fiveHour, weekly),
		Windows:    []Window{{"five_hour", fiveHour}, {"weekly", weekly}},
		ResetsSoon: resetsSoon(jsjson.Path(codex, "five_hour", "resets_at"), th, now),
		Detail:     codex,
	}
}

func unavailablePosture(entry any) VendorPosture {
	if !jsjson.Truthy(entry) {
		entry = nil
	}
	return VendorPosture{Available: false, Overall: Unknown, Frontier: Unknown, Detail: entry}
}

// knownOrGreen treats an unknown secondary window as no constraint.
func knownOrGreen(level string) string {
	if level == Unknown {
		return Green
	}
	return level
}

func resetsSoon(resetsAt any, th Thresholds, now time.Time) bool {
	minutes, known := minutesUntil(resetsAt, now)
	if !known {
		minutes = 1e9
	}
	return minutes <= th.SoonMinutes
}

// minutesUntil is the rounded number of minutes from now to resetsAt, which is an ISO string
// or epoch seconds; known is false when it is falsy or unparsable.
func minutesUntil(resetsAt any, now time.Time) (minutes float64, known bool) {
	if !jsjson.Truthy(resetsAt) {
		return 0, false
	}
	var atMs float64
	switch v := resetsAt.(type) {
	case float64:
		atMs = v * 1000
	case string:
		ms, ok := jsjson.ParseDate(v)
		if !ok {
			return 0, false
		}
		atMs = float64(ms)
	default:
		return 0, false
	}
	return jsjson.Round((atMs - float64(now.UnixMilli())) / 60000), true
}
