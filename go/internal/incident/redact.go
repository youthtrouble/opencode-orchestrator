package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// The patterns of redact() in lib/incident.mjs, applied in this order. `\w`, `\d` and `\b` are
// ASCII in both JS and RE2, but RE2's `\s` is not the JS set, so it is spelled out.
var redactions = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`/Users/[^/` + jsstr.WhiteSpaceClass + `]+`), "~"},
	{regexp.MustCompile(`/home/[^/` + jsstr.WhiteSpaceClass + `]+`), "~"},
	{regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`), "<email>"},
	{regexp.MustCompile(`\b(?:sk|rt|at|eyJ)[A-Za-z0-9._-]{20,}`), "<token>"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\bses_[A-Za-z0-9]+`), "<session>"},
}

var (
	digits     = regexp.MustCompile(`\d+`)
	whiteSpace = regexp.MustCompile(`[` + jsstr.WhiteSpaceClass + `]+`)
)

// maxRedacted is where redact() cuts its result, in UTF-16 code units.
const maxRedacted = 2000

// Redact strips what may identify the user from text: every occurrence of home (the home
// directory as HOME gives it), then any /Users/<name> or /home/<name> segment, becomes "~";
// emails, tokens, uuids and opencode session ids become placeholders. The result is cut to 2000
// UTF-16 code units (a split surrogate pair becomes U+FFFD: PE-1). Replacements are literal.
func Redact(home, text string) string {
	if home == "" {
		text = tildeAtEveryPosition(text)
	} else {
		text = strings.ReplaceAll(text, home, "~")
	}
	for _, r := range redactions {
		text = r.pattern.ReplaceAllLiteralString(text, r.replacement)
	}
	return jsstr.Slice(text, 0, maxRedacted)
}

// tildeAtEveryPosition is what the JS home replacement does for an empty home: the pattern
// is then empty and, without the u flag, matches before every UTF-16 code unit and at the end,
// so "a😀" becomes "~a~\ud83d~\ude00~". Go cannot hold the split halves; each becomes U+FFFD
// (PE-1: same length, same hash, same stdout).
func tildeAtEveryPosition(text string) string {
	var out strings.Builder
	for _, r := range text {
		if utf16.RuneLen(r) == 2 {
			out.WriteString("~\ufffd~\ufffd")
		} else {
			out.WriteByte('~')
			out.WriteRune(r)
		}
	}
	out.WriteByte('~')
	return out.String()
}

// Fingerprint groups incidents that are the same problem: the first 12 hex digits of the
// sha256 of "component|kind|message", the message redacted, lowercased as JS does, with digit
// runs as N, white space runs as one space, and cut to 200 UTF-16 code units. component is
// written as a JS template literal would (a bare --component gives true).
func Fingerprint(home string, component any, kind, message string) string {
	norm := jsstr.ToLower(Redact(home, message))
	norm = digits.ReplaceAllLiteralString(norm, "N")
	norm = whiteSpace.ReplaceAllLiteralString(norm, " ")
	sum := sha256.Sum256([]byte(jsjson.TemplateString(component) + "|" + kind + "|" + jsstr.Slice(norm, 0, 200)))
	return hex.EncodeToString(sum[:])[:12]
}
