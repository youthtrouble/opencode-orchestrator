package snapshot

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
)

// The schema's maxLength for each pushed string, in UTF-16 code units as JSON Schema counts.
const (
	maxNameUnits        = 80
	maxReasonUnits      = 200
	maxSliceUnits       = 40
	maxNowUnits         = 200
	maxFromUnits        = 60
	maxKindUnits        = 20
	maxEventUnits       = 140 // an event subject
	maxSubjectUnits     = 200 // a question subject
	maxBodyUnits        = 2000
	maxRecommendedUnits = 1000
)

// sendableID is the one message field sent without redaction, so only the form orch's writers
// generate goes verbatim: a lowercase uuid (randomUUID in JS, mailbox.NewUUID in Go). A
// hand-edited id (a path, an email, a credential) is not sent at all.
var sendableID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString

// redactCutUnits is where incident.Redact cuts its result. A redacted text that long may have
// been cut there, so it is marked as cut even when it fits its cap.
const redactCutUnits = 2000

const ellipsis = "…"

// cleaner prepares free text for the wire: it strips what may identify the owner or open
// their accounts (credentials, filesystem paths, the home directory, emails, uuids, session
// ids) and cuts it to a schema cap.
type cleaner struct{ home string }

// text redacts s and cuts it to maxUnits. incident.Redact runs last, so its own cut is the
// one redactCutUnits detects.
func (c cleaner) text(s string, maxUnits int) string {
	redacted := incident.Redact(c.home, scrubOutbound(s))
	if length := utf16Length(redacted); length > maxUnits || length >= redactCutUnits {
		return shorten(redacted, maxUnits)
	}
	return redacted
}

// optional is text for a nullable field: nil when s is empty or cleans to nothing.
func (c cleaner) optional(s string, maxUnits int) *string {
	cleaned := c.text(s, maxUnits)
	if cleaned == "" {
		return nil
	}
	return &cleaned
}

// shorten cuts s to fewer than maxUnits UTF-16 code units and ends it with "…", within the
// cap. It cuts between characters, never inside a surrogate pair.
func shorten(s string, maxUnits int) string {
	budget := maxUnits - utf16Length(ellipsis)
	var kept strings.Builder
	for _, r := range s {
		units := utf16.RuneLen(r)
		if units > budget {
			break
		}
		budget -= units
		kept.WriteRune(r)
	}
	return kept.String() + ellipsis
}

func utf16Length(s string) int {
	units := 0
	for _, r := range s {
		units += utf16.RuneLen(r)
	}
	return units
}

// iso formats t as the wire's UTC timestamp with milliseconds.
func iso(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// isoOrNull is iso for a nullable field: nil for the zero time.
func isoOrNull(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	formatted := iso(t)
	return &formatted
}
