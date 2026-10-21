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
	// CreditFunded marks an entry whose vendor's red window runs on ChatGPT credits.
	CreditFunded bool
	// Balanced marks an entry moved to the other vendor for its clearly larger 5-hour headroom.
	Balanced bool
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
	if r.CreditFunded {
		out.Set("credit_funded", true)
	}
	if r.Balanced {
		out.Set("balanced", true)
	}
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
// vendors whose posture is red (for frontier tiers: not green) unless credits fund the work,
// and balancing the roles in balancedRoles across vendors by 5-hour headroom.
// channels[vendor] is true when opencode has that vendor natively, which makes the role run
// "via opencode" instead of the vendor's CLI bridge. credits is the owner's credits setting;
// its zero value never draws credits.
func ResolveRoles(usage *jsjson.Object, registry *jsjson.Object, now time.Time, channels map[string]bool, credits CreditsPolicy) Resolved {
	postures := VendorPostures(usage, registry, now)
	postures.OpenAI.CreditFunded = creditFunded(&postures.OpenAI, credits)
	pick := func(role, coderVendor string) *RoleEntry {
		return pickFor(&postures, registry, role, coderVendor)
	}

	// Planner and coder first; the reviewer is chosen relative to the coder's vendor.
	planner := pick("planner", "")
	coder := pick("coder", "")
	coderVendor := Anthropic
	if coder != nil {
		coderVendor = coder.Vendor
	}
	reviewer := pick("reviewer", coderVendor)
	if reviewer != nil {
		crossVendor := reviewer.Vendor != coderVendor
		reviewer.CrossVendor = &crossVendor
	}
	roles := []Assignment{{"planner", planner}, {"coder", coder}, {"reviewer", reviewer}}
	for _, role := range []string{"verifier", "researcher", "explorer", "utility"} {
		roles = append(roles, Assignment{role, pick(role, "")})
	}

	paused := allPaused(roles)
	for _, a := range roles {
		if a.Entry != nil {
			a.Entry.Via = channelOf(a.Entry, channels)
		}
	}
	return Resolved{Postures: postures, Roles: roles, Reasons: reasons(&postures, coder, paused, credits), Paused: paused}
}

// channelOf is the execution channel: the native opencode provider or the vendor's CLI bridge.
// Credit-funded work always takes the bridge, which records the balance before and after the
// run.
func channelOf(entry *RoleEntry, channels map[string]bool) string {
	if channels[entry.Vendor] && !entry.CreditFunded {
		return "opencode"
	}
	return entry.CLI + "-cli"
}

// balancedRoles may move to the other vendor when it has clearly more 5-hour headroom.
var balancedRoles = map[string]bool{"coder": true, "verifier": true, "researcher": true, "explorer": true, "utility": true}

// pickFor returns the first usable preference of the role, unless the role is balanced and
// another usable vendor leads it in headroom by more than thresholds.balance_margin.
func pickFor(postures *Postures, registry *jsjson.Object, role, coderVendor string) *RoleEntry {
	candidates := usableCandidates(postures, registry, role, coderVendor)
	if len(candidates) == 0 {
		return nil
	}
	first := candidates[0]
	if balancedRoles[role] && len(candidates) > 1 {
		margin := thresholdsOf(registry).BalanceMargin
		alt := otherVendorCandidate(first, candidates)
		if alt != nil && headroom(postures, alt.Vendor)-headroom(postures, first.Vendor) > margin {
			alt.Balanced = true
			return alt
		}
	}
	return first
}

// usableCandidates returns the role's preferences whose vendor is usable, in registry order. A
// preference is "vendor.tier", where vendor "cross" means the vendor other than coderVendor and
// "same" means coderVendor; with no coderVendor (every role but the reviewer) "cross" is
// anthropic and "same" is unusable. A preference whose tier is missing from the registry is
// skipped, so the search continues with the next one.
func usableCandidates(postures *Postures, registry *jsjson.Object, role, coderVendor string) []*RoleEntry {
	prefs, _ := jsjson.Path(registry, "roles", role, "prefer").([]any)
	var candidates []*RoleEntry
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
		if !usable(postures, vendor, tier == "frontier") {
			continue
		}
		if entry := tierEntry(registry, vendor, tier); entry != nil {
			entry.CreditFunded = postures.ByVendor(vendor).CreditFunded
			candidates = append(candidates, entry)
		}
	}
	return candidates
}

// otherVendorCandidate is the first candidate from a vendor other than first's, preferring
// one of the same tier. Credits are never spent to balance headroom, so a credit-funded
// candidate is never the other one.
func otherVendorCandidate(first *RoleEntry, candidates []*RoleEntry) *RoleEntry {
	var anyTier *RoleEntry
	for _, c := range candidates {
		if c.Vendor == first.Vendor || c.CreditFunded {
			continue
		}
		if c.Tier == first.Tier {
			return c
		}
		if anyTier == nil {
			anyTier = c
		}
	}
	return anyTier
}

// headroom is the vendor's 5-hour headroom, -1 when unknown.
func headroom(postures *Postures, vendor string) float64 {
	p := postures.ByVendor(vendor)
	if p == nil || p.Headroom == nil {
		return -1
	}
	return *p.Headroom
}

// usable: a vendor serves a role unless unavailable or red, or red with its work
// credit-funded, except for frontier tiers. Frontier tiers draw on weekly
// caps (Fable has its own), so they are used only while the frontier posture is green; an
// amber one sends routine planning to the strong tier.
func usable(postures *Postures, vendor string, isFrontier bool) bool {
	p := postures.ByVendor(vendor)
	if p == nil || !p.Available {
		return false
	}
	if isFrontier {
		return p.Frontier == Green
	}
	return p.Overall != Red || p.CreditFunded
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

// ModelName is the JS `v.model.split("/")[1]` of a role entry, read from the registry tier
// because the entry's Model cannot tell a missing model from an empty one: "undefined" for a
// model without a slash, the JS TypeError for a model that is not a string.
func ModelName(registry *jsjson.Object, e *RoleEntry) (string, error) {
	tier := jsjson.Path(registry, "vendors", e.Vendor, "tiers", e.Tier)
	raw, present := jsjson.Lookup(tier, "opencode")
	switch model := raw.(type) {
	case string:
		_, after, found := strings.Cut(model, "/")
		if !found {
			return "undefined", nil
		}
		name, _, _ := strings.Cut(after, "/")
		return name, nil
	case nil:
		if present {
			return "", jsjson.CannotReadOfNull("split")
		}
		return "", &jsjson.TypeError{Message: "Cannot read properties of undefined (reading 'split')"}
	default:
		return "", &jsjson.TypeError{Message: "v.model.split is not a function"}
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
func reasons(postures *Postures, coder *RoleEntry, paused bool, credits CreditsPolicy) []string {
	a, o := &postures.Anthropic, &postures.OpenAI
	fable, _ := a.Window("fable")
	var out []string
	if a.Available && fable == Red {
		out = append(out, "Fable weekly cap is red: planning routed away from Fable until it resets.")
	}
	if a.Available && fable == Amber {
		out = append(out, "Fable weekly cap is amber: routine planning goes to Opus 5.5; only size=hard delegations use Fable.")
	}
	if a.Available && a.Overall == Red {
		out = append(out, "Claude session/weekly is red: Anthropic roles failed over to OpenAI.")
	}
	if coder != nil && coder.Balanced {
		out = append(out, "Coder balanced to "+coder.Vendor+": it has more 5-hour headroom (Claude "+usedText(a)+"% vs Codex "+usedText(o)+"% used).")
	}
	if weeklyAmber(a) || weeklyAmber(o) {
		out = append(out, "A weekly cap is amber: shown for planning, not steering; only a red weekly blocks a vendor.")
	}
	if o.Available && o.Overall == Red {
		if o.CreditFunded {
			out = append(out, "Codex window is red but "+credits.Text()+": OpenAI roles spend ChatGPT credits through the codex bridge ("+OpenAICredits(o.Detail).Text()+").")
		} else {
			out = append(out, "Codex window is red: OpenAI roles failed over to Anthropic.")
		}
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

// usedText is `${100 - (headroom ?? 0)}`.
func usedText(p *VendorPosture) string {
	headroom := 0.0
	if p.Headroom != nil {
		headroom = *p.Headroom
	}
	return jsjson.TemplateString(100 - headroom)
}

func weeklyAmber(p *VendorPosture) bool {
	weekly, _ := p.Window("weekly")
	return p.Available && weekly == Amber
}
