package jsjson

import (
	"math"
	"time"
)

// dateLayouts are the forms of the ECMAScript Date Time String Format, in the order tried:
// date-time with Z or an offset (seconds and any fraction optional; Anthropic sends
// microseconds), date-only, year-month and year-only (all UTC, as in JS), and date-time
// without a zone (local time, as in JS).
//
// Known gaps, where V8's legacy fallback parser accepts input that ParseDate rejects: a
// lowercase "z", offsets without a colon ("+0100"), a space instead of "T"
// ("2026-10-18 10:00:00"), hour 24 ("T24:00:00Z"), days past the month's end ("2026-02-30",
// which V8 rolls over to March 2) and expanded years ("+002026-10-18"). The orchestrator's
// producers write toISOString or Anthropic ISO timestamps, which never use these forms.
var dateLayouts = []struct {
	layout   string
	location *time.Location
}{
	{time.RFC3339Nano, time.UTC},
	{"2006-01-02T15:04Z07:00", time.UTC},
	{"2006-01-02", time.UTC},
	{"2006-01", time.UTC},
	{"2006", time.UTC},
	{"2006-01-02T15:04:05.999999999", time.Local},
	{"2006-01-02T15:04", time.Local},
}

// ParseDate mirrors Date.parse for the formats above: milliseconds since the epoch, with
// sub-millisecond digits truncated as V8 does. ok is false where Date.parse returns NaN and
// for the known gaps listed above.
func ParseDate(s string) (ms int64, ok bool) {
	for _, candidate := range dateLayouts {
		if t, err := time.ParseInLocation(candidate.layout, s, candidate.location); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// Round is Math.round: halves round toward +Infinity (Math.round(-2.5) is -2), unlike
// math.Round, which rounds them away from zero. floor(x+0.5) differs from Math.round only
// where x+0.5 is inexact, e.g. 0.49999999999999994 gives 1 instead of 0; callers round
// minute counts, far from that precision.
func Round(x float64) float64 {
	return math.Floor(x + 0.5)
}
