// Package flip is the conductor flip policy of lib/flip.mjs (pure): which front door should
// drive the project given both vendors' postures.
package flip

import (
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

// The two front doors: the OpenAI-native opencode conductor and the Anthropic-native Claude
// Code conductor.
const (
	DoorOpencode = "opencode"
	DoorClaude   = "claude"
)

// DoorVendor maps each door to the vendor whose budget it runs on.
var DoorVendor = map[string]string{DoorOpencode: routing.OpenAI, DoorClaude: routing.Anthropic}

// OtherDoor returns the opposite door; every name but "opencode" counts as the Claude side.
func OtherDoor(door string) string {
	if door == DoorOpencode {
		return DoorClaude
	}
	return DoorOpencode
}

// Actions of a Decision.
const (
	ActionFlip     = "flip"
	ActionHandback = "handback"
	ActionStay     = "stay"
	ActionPause    = "pause"
)

// Decision is the result of Decide.
type Decision struct {
	Action string
	// To is the door that should drive next; empty (JSON null) when pausing.
	To     string
	Reason string
}

// JSON returns {action, to, reason}.
func (d Decision) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("action", d.Action)
	if d.To == "" {
		out.Set("to", nil)
	} else {
		out.Set("to", d.To)
	}
	out.Set("reason", d.Reason)
	return out
}

// Input is what Decide needs. LastFlipAt is nil when the project never flipped (or its
// flip.json time does not parse, which the JS also treats as "not cooling").
type Input struct {
	Active     string
	Postures   routing.Postures
	Registry   *jsjson.Object
	Now        time.Time
	LastFlipAt *time.Time
}

// conductorConfig is registry.conductor with the JS defaults.
type conductorConfig struct {
	home                string
	greenGraceMinutes   float64
	flipCooldownMinutes float64
}

func conductorConfigOf(registry *jsjson.Object) conductorConfig {
	cfg := jsjson.Path(registry, "conductor")
	home, _ := jsjson.Path(cfg, "home").(string)
	if home == "" {
		home = DoorOpencode
	}
	return conductorConfig{
		home:                home,
		greenGraceMinutes:   jsjson.NumberOr(cfg, "green_grace_minutes", 10),
		flipCooldownMinutes: jsjson.NumberOr(cfg, "flip_cooldown_minutes", 10),
	}
}

// Decide applies the flip policy: a conductor whose own vendor is red flips to a usable other
// door (or pauses when there is none); an away conductor hands back once the home vendor is
// stably green and no flip happened within the cooldown; otherwise it stays.
func Decide(in Input) Decision {
	cfg := conductorConfigOf(in.Registry)
	other := OtherDoor(in.Active)
	own := in.Postures.ByVendor(DoorVendor[in.Active])
	them := in.Postures.ByVendor(DoorVendor[other])
	ownRed := own == nil || !own.Available || own.Overall == routing.Red
	themUsable := them != nil && them.Available && them.Overall != routing.Red
	cooling := in.LastFlipAt != nil && minutesBetween(*in.LastFlipAt, in.Now) < cfg.flipCooldownMinutes

	if ownRed {
		if themUsable {
			return Decision{ActionFlip, other, vendorText(in.Active) + " is red; " + vendorText(other) + " has headroom"}
		}
		return Decision{ActionPause, "", "both vendors red or unknown; wait for the nearest reset"}
	}
	if in.Active != cfg.home {
		homeVendor := DoorVendor[cfg.home]
		home := in.Postures.ByVendor(homeVendor)
		stable := home != nil && home.Available && home.Overall == routing.Green &&
			!windowFreshlyReset(home.Detail, homeVendor, in.Now, cfg.greenGraceMinutes)
		if stable && !cooling {
			return Decision{ActionHandback, cfg.home, "home door " + cfg.home + " is stably green again"}
		}
		if stable {
			return Decision{ActionStay, in.Active, "cooldown after a recent flip"}
		}
		return Decision{ActionStay, in.Active, "home door " + cfg.home + " not stably green yet"}
	}
	return Decision{ActionStay, in.Active, "home door with headroom"}
}

// vendorText is `${DOOR_VENDOR[door]}`: DOOR_VENDOR is a plain object, so a name it inherits
// from Object.prototype ("constructor", "__proto__", ...) prints that property's text, and any
// other name that is not a door prints "undefined".
func vendorText(door string) string {
	if vendor, known := DoorVendor[door]; known {
		return vendor
	}
	if inherited, ok := jsjson.InheritedText(door); ok {
		return inherited
	}
	return "undefined"
}

// windowFreshlyReset reports that the vendor's main window reset less than graceMin minutes
// ago, inferred from its resets_at and window length (default 300 minutes): such a window
// is not yet "stable".
func windowFreshlyReset(detail any, vendor string, now time.Time, graceMin float64) bool {
	windowKey := "session"
	if vendor == routing.OpenAI {
		windowKey = "five_hour"
	}
	window := jsjson.Path(detail, windowKey)
	resetsAt := jsjson.Path(window, "resets_at")
	if !jsjson.Truthy(resetsAt) {
		return false
	}
	lengthMin := 300.0
	if n, isNumber := jsjson.Path(window, "window_mins").(float64); isNumber && jsjson.Truthy(n) {
		lengthMin = n
	}
	text, _ := resetsAt.(string)
	resetMs, ok := jsjson.ParseDate(text)
	if !ok {
		return false // Date.parse is NaN, and NaN > x is false
	}
	untilReset := float64(resetMs-now.UnixMilli()) / 60000
	return untilReset > lengthMin-graceMin
}

// minutesBetween works in whole milliseconds, like the JS Date arithmetic.
func minutesBetween(from, to time.Time) float64 {
	return float64(to.UnixMilli()-from.UnixMilli()) / 60000
}
