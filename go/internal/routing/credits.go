package routing

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// ChatGPT credits (lib/routing.mjs): Codex draws them by itself once the plan limits are
// reached (ordinary usage refused). Banked resets (rateLimitResetCredits) are promotional
// resets of the usage window, not money; nothing here redeems them. A balance is a decimal
// string in the app-server schema.
var decimal = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// DecimalOrNull is the JS decimalOrNull: a finite number, or a decimal string read as one
// (policy values are strings); ok is false for anything else.
func DecimalOrNull(v any) (n float64, ok bool) {
	switch v := v.(type) {
	case float64:
		return v, !math.IsInf(v, 0) && !math.IsNaN(v)
	case string:
		if !decimal.MatchString(v) {
			return 0, false
		}
		n, err := strconv.ParseFloat(v, 64)
		return n, err == nil
	}
	return 0, false
}

// Credits is the OpenAI credits fact of a Codex usage entry. HasCredits, Unlimited and
// Balance are nil (JSON null) when Codex sent no credits object; Balance is also nil when the
// balance is not a decimal string.
type Credits struct {
	Balance          *string
	HasCredits       *bool
	Unlimited        *bool
	PlanLimitReached bool
	BankedResets     float64
}

// OpenAICredits is openaiCredits: the fact, or nil when the entry is not ok or Codex reported
// neither credits nor banked resets.
func OpenAICredits(codex any) *Credits {
	if !jsjson.Truthy(jsjson.Path(codex, "ok")) {
		return nil
	}
	credits, _ := jsjson.Path(codex, "credits").(*jsjson.Object)
	banked := 0.0
	if n, isNumber := jsjson.Path(codex, "reset_credits_available").(float64); isNumber && isInteger(n) && n > 0 {
		banked = n
	}
	if credits == nil && banked == 0 {
		return nil
	}
	fact := &Credits{
		PlanLimitReached: jsjson.Path(codex, "ordinary_usage_allowed") == false || jsjson.Truthy(jsjson.Path(codex, "rate_limit_reached")),
		BankedResets:     banked,
	}
	if credits != nil {
		if balance, isString := jsjson.Path(credits, "balance").(string); isString && decimal.MatchString(balance) {
			fact.Balance = &balance
		}
		hasCredits, unlimited := jsjson.Path(credits, "hasCredits") == true, jsjson.Path(credits, "unlimited") == true
		fact.HasCredits, fact.Unlimited = &hasCredits, &unlimited
	}
	return fact
}

// isInteger is Number.isInteger.
func isInteger(n float64) bool { return !math.IsInf(n, 0) && math.Trunc(n) == n }

// JSON returns the fact as the JS object, keys in the JS order.
func (c *Credits) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("balance", nullable(c.Balance))
	out.Set("has_credits", nullable(c.HasCredits))
	out.Set("unlimited", nullable(c.Unlimited))
	out.Set("plan_limit_reached", c.PlanLimitReached)
	out.Set("banked_resets", c.BankedResets)
	return out
}

func nullable[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// Text is creditsText: "credits 215.68 left; plan limit reached; 3 banked resets (window
// resets, not money)".
func (c *Credits) Text() string {
	var parts []string
	if c.HasCredits != nil {
		switch {
		case *c.Unlimited:
			parts = append(parts, "credits unlimited")
		case !*c.HasCredits:
			parts = append(parts, "no credits")
		case c.Balance == nil:
			parts = append(parts, "credits available")
		default:
			parts = append(parts, "credits "+*c.Balance+" left")
		}
	}
	if c.PlanLimitReached {
		parts = append(parts, "plan limit reached")
	}
	if c.BankedResets > 0 {
		plural := "s"
		if c.BankedResets == 1 {
			plural = ""
		}
		parts = append(parts, jsjson.TemplateString(c.BankedResets)+" banked reset"+plural+" (window resets, not money)")
	}
	return strings.Join(parts, "; ")
}

// spendable reports unlimited credits or a balance above floor.
func (c *Credits) spendable(floor float64) bool {
	if c.Unlimited != nil && *c.Unlimited {
		return true
	}
	if c.HasCredits == nil || !*c.HasCredits || c.Balance == nil {
		return false
	}
	balance, ok := DecimalOrNull(*c.Balance)
	return ok && balance > floor
}

// CreditsPolicy is the owner's credits setting (policy openai_credits, openai_credits_floor).
// The zero value is off.
type CreditsPolicy struct {
	On    bool
	Floor float64
}

// CreditsPolicyOf is creditsPolicyOf over the effective policy: anything but "on" is off, as
// credits are spend and are never drawn without the explicit setting; a floor that is not a
// number is 0.
func CreditsPolicyOf(policy any) CreditsPolicy {
	floor, ok := DecimalOrNull(jsjson.Path(policy, "openai_credits_floor"))
	if !ok {
		floor = 0
	}
	return CreditsPolicy{On: jsjson.Path(policy, "openai_credits") == "on", Floor: floor}
}

func (p CreditsPolicy) mode() string {
	if p.On {
		return "on"
	}
	return "off"
}

// JSON returns {mode, floor}.
func (p CreditsPolicy) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("mode", p.mode())
	out.Set("floor", p.Floor)
	return out
}

// Text is creditsPolicyText: "openai_credits=on floor 20", the floor only when non-zero.
func (p CreditsPolicy) Text() string {
	text := "openai_credits=" + p.mode()
	if p.Floor != 0 {
		text += " floor " + jsjson.TemplateString(p.Floor)
	}
	return text
}

// creditFunded reports whether red OpenAI work may run on credits: the setting is on, Codex
// refuses ordinary usage (the plan limit is reached), and credits are unlimited or their
// balance is above the floor.
func creditFunded(posture *VendorPosture, policy CreditsPolicy) bool {
	if !policy.On || !posture.Available || posture.Overall != Red || jsjson.Path(posture.Detail, "ordinary_usage_allowed") != false {
		return false
	}
	fact := OpenAICredits(posture.Detail)
	return fact != nil && fact.spendable(policy.Floor)
}
