package routing

import (
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// RoleEntry is the model chosen for a role: one vendor tier from the registry plus the
// execution channel.
type RoleEntry struct {
	Vendor string
	Tier   string
	// Model, CLIModel and CLI are the tier's opencode and cli ids and the vendor's CLI; the
	// registry declares them as strings (a missing one reads as "", where JS would omit it).
	Model    string
	CLIModel string
	CLI      string
	// Variant, Effort and Context are copied verbatim from the tier; nil is JSON null.
	Variant any
	Effort  any
	Context any
	Note    string
	// CrossVendor is set for the reviewer only: whether it differs from the coder's vendor.
	CrossVendor *bool
	// Via is the channel: "opencode" for a native provider, else "<cli>-cli".
	Via string
}

// JSON returns the role entry as the JS object, keys in the JS order.
func (r *RoleEntry) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("vendor", r.Vendor)
	out.Set("tier", r.Tier)
	out.Set("model", r.Model)
	out.Set("cli_model", r.CLIModel)
	out.Set("cli", r.CLI)
	out.Set("variant", r.Variant)
	out.Set("effort", r.Effort)
	out.Set("context", r.Context)
	out.Set("note", r.Note)
	if r.CrossVendor != nil {
		out.Set("cross_vendor", *r.CrossVendor)
	}
	out.Set("via", r.Via)
	return out
}

// Assignment pairs a role with its entry; a nil Entry means the role is paused.
type Assignment struct {
	Role  string
	Entry *RoleEntry
}

// Resolved is the result of ResolveRoles.
type Resolved struct {
	Postures Postures
	Roles    []Assignment // planner, coder, reviewer, verifier, researcher, explorer, utility
	Reasons  []string
	Paused   bool
}

// Role returns the entry of the named role, nil when it is paused or not a role.
func (r *Resolved) Role(name string) *RoleEntry {
	for _, a := range r.Roles {
		if a.Role == name {
			return a.Entry
		}
	}
	return nil
}

// JSON returns {postures, roles, reasons, paused} as the JS writes it.
func (r *Resolved) JSON() *jsjson.Object {
	roles := jsjson.NewObject()
	for _, a := range r.Roles {
		if a.Entry == nil {
			roles.Set(a.Role, nil)
		} else {
			roles.Set(a.Role, a.Entry.JSON())
		}
	}
	reasons := make([]any, len(r.Reasons))
	for i, reason := range r.Reasons {
		reasons[i] = reason
	}
	out := jsjson.NewObject()
	out.Set("postures", r.Postures.JSON())
	out.Set("roles", roles)
	out.Set("reasons", reasons)
	out.Set("paused", r.Paused)
	return out
}

// ResolveRoles picks a model for every role from the registry's preference lists, skipping
// vendors whose posture is red. channels[vendor] is true when opencode has that vendor
// natively, which makes the role run "via opencode" instead of the vendor's CLI bridge.
func ResolveRoles(usage *jsjson.Object, registry *jsjson.Object, now time.Time, channels map[string]bool) Resolved {
	postures := VendorPostures(usage, registry, now)

	// Planner and coder first; the reviewer is chosen relative to the coder's vendor.
	planner := pick(&postures, registry, "planner", "")
	coder := pick(&postures, registry, "coder", "")
	coderVendor := Anthropic
	if coder != nil {
		coderVendor = coder.Vendor
	}
	reviewer := pick(&postures, registry, "reviewer", coderVendor)
	if reviewer != nil {
		crossVendor := reviewer.Vendor != coderVendor
		reviewer.CrossVendor = &crossVendor
	}
	roles := []Assignment{{"planner", planner}, {"coder", coder}, {"reviewer", reviewer}}
	for _, role := range []string{"verifier", "researcher", "explorer", "utility"} {
		roles = append(roles, Assignment{role, pick(&postures, registry, role, "")})
	}

	paused := allPaused(roles)
	for _, a := range roles {
		if a.Entry != nil {
			a.Entry.Via = channelOf(a.Entry, channels)
		}
	}
	return Resolved{Postures: postures, Roles: roles, Reasons: reasons(&postures, paused), Paused: paused}
}

// channelOf is the execution channel: the native opencode provider or the vendor's CLI bridge.
func channelOf(entry *RoleEntry, channels map[string]bool) string {
	if channels[entry.Vendor] {
		return "opencode"
	}
	return entry.CLI + "-cli"
}

// pick returns the first usable preference of the role. A preference is "vendor.tier", where
// vendor "cross" means the vendor other than coderVendor and "same" means coderVendor; with
// no coderVendor (every role but the reviewer) "cross" is anthropic and "same" is unusable.
// The first usable vendor wins even when the registry lacks its tier (the role is then
// paused): mirrored from the JS.
func pick(postures *Postures, registry *jsjson.Object, role string, coderVendor string) *RoleEntry {
	prefs, _ := jsjson.Path(registry, "roles", role, "prefer").([]any)
	for _, item := range prefs {
		pref, _ := item.(string)
		parts := strings.Split(pref, ".")
		vendor, tier := parts[0], ""
		if len(parts) > 1 {
			tier = parts[1]
		}
		switch vendor {
		case "cross":
			if coderVendor == Anthropic {
				vendor = OpenAI
			} else {
				vendor = Anthropic
			}
		case "same":
			vendor = coderVendor
		}
		if usable(postures, vendor, tier == "frontier") {
			return tierEntry(registry, vendor, tier)
		}
	}
	return nil
}

// usable: a vendor serves a role unless unavailable or red (frontier tiers also count the
// Fable window).
func usable(postures *Postures, vendor string, isFrontier bool) bool {
	p := postures.ByVendor(vendor)
	if p == nil || !p.Available {
		return false
	}
	level := p.Overall
	if isFrontier {
		level = p.Frontier
	}
	return level != Red
}

func tierEntry(registry *jsjson.Object, vendor, tier string) *RoleEntry {
	v := jsjson.Path(registry, "vendors", vendor)
	t := jsjson.Path(v, "tiers", tier)
	if !jsjson.Truthy(t) {
		return nil
	}
	return &RoleEntry{
		Vendor:   vendor,
		Tier:     tier,
		Model:    stringAt(t, "opencode"),
		CLIModel: stringAt(t, "cli"),
		CLI:      stringAt(v, "cli"),
		Variant:  jsjson.Path(t, "variant"),
		Effort:   jsjson.Path(t, "effort"),
		Context:  jsjson.Path(t, "context"),
		Note:     stringAt(t, "note"),
	}
}

func stringAt(v any, key string) string {
	s, _ := jsjson.Path(v, key).(string)
	return s
}

func allPaused(roles []Assignment) bool {
	for _, a := range roles {
		if a.Entry != nil {
			return false
		}
	}
	return true
}

// reasons explains the interesting routing decisions, in the JS order.
func reasons(postures *Postures, paused bool) []string {
	a, o := &postures.Anthropic, &postures.OpenAI
	fable, _ := a.Window("fable")
	var out []string
	if a.Available && fable == Red {
		out = append(out, "Fable weekly cap is red: planning routed away from Fable until it resets.")
	}
	if a.Available && fable == Amber {
		out = append(out, "Fable weekly cap is amber: use Fable only for genuinely hard planning; Opus 5.5 for routine plans.")
	}
	if a.Available && a.Overall == Red {
		out = append(out, "Claude session/weekly is red: Anthropic roles failed over to OpenAI.")
	}
	if o.Available && o.Overall == Red {
		out = append(out, "Codex window is red: OpenAI roles failed over to Anthropic.")
	}
	if !a.Available {
		out = append(out, "Claude usage unknown (no OAuth token found): treating Anthropic as unavailable for automatic routing.")
	}
	if !o.Available {
		out = append(out, "Codex usage unknown (app-server unreachable or not logged in): treating OpenAI as unavailable for automatic routing.")
	}
	if paused {
		out = append(out, "Both vendors are red or unavailable: pause heavy work; wait for the nearest reset.")
	}
	return out
}
