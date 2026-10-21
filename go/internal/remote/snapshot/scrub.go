package snapshot

import (
	"regexp"
	"strings"
)

// What Work Trains sends leaves the Mac, so its free text is scrubbed harder than
// incident.Redact does (plan D11). incident.Redact and ScrubTokens have a byte-identical JS
// twin under the parity harness and cannot grow; these rules run first, on the raw text.

// A substitution is a pattern and its replacement template ($1 keeps the first group).
type substitution struct {
	pattern  *regexp.Regexp
	template string
}

// credentialSubstitutions, in order. A private key block may span lines and is dropped to the
// end of the text when its END line was cut off. Provider tokens are matched by their
// published prefixes wherever they start, even glued to a word (backup_ghp_…): the prefixes
// are distinctive enough that redacting a rare false match is the cheaper mistake. A JWT (or
// JWE) header is base64url JSON, so it starts "eyJ".
var credentialSubstitutions = []substitution{
	{regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----(?s:.*?)(?:-----END[A-Z ]*PRIVATE KEY-----|\z)`), "<private key>"},
	{regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`), "${1}<credentials>@"},
	{regexp.MustCompile(`(?:` +
		`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|` + // GitHub
		`(?:AKIA|ASIA)[A-Z0-9]{16}|` + // AWS access key ids
		`xox[abprs]-[A-Za-z0-9-]{10,}|` + // Slack
		`AIza[A-Za-z0-9_-]{30,}|` + // Google API keys
		`(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{10,}|` + // Stripe
		`npm_[A-Za-z0-9]{20,}|` + // npm
		`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*` + // JWT
		`)`), "<token>"},
	// At least 16 characters, so prose such as "Bearer authentication" survives.
	{regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/-]{16,}=*`), "${1}<token>"},
	// password=..., "token": "...", API_KEY: ... (the name may carry a prefix, as in
	// db_password or authToken). Only the value goes.
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)["']?\s*[:=]\s*)` +
		`(?:"[^"]*"|'[^']*'|[^\s"',;]+)`), "${1}<redacted>"},
}

// Path rules. D11 says paths are never sent, so they err towards redacting prose: a quoted path
// goes whole, spaces and all; an unquoted one goes with the rest of its clause, because a final
// component may hold a space and no case or prefix rule tells it from prose (/Volumes/Private
// client). What survives is a lone "/", words joined by a slash (and/or, w/o, 1/2, 2026/10/21),
// relative paths (go/internal, ./a/b), URLs and the text after the clause ends.

// quotedPaths match a whole quoted span ("…", '…' or `…`) that opens like a path: Unix (/, ~/,
// file://), a Windows drive (C:\, C:/) or a UNC share (\\server).
var quotedPaths = []*regexp.Regexp{quotedPath(`"`), quotedPath(`'`), quotedPath("\x60")}

func quotedPath(quote string) *regexp.Regexp {
	return regexp.MustCompile(quote + `(?:/|~/|file://|[A-Za-z]:[\\/]|\\\\)[^\n` + quote + `]*` + quote)
}

const (
	// segmentChar is a character of one path component: not whitespace, a separator or the
	// punctuation that usually closes around a path in prose. In a Unix component a
	// backslash-escaped character (an escaped space) belongs to the component too.
	segmentChar = "[^\\s/\\\\\"'\x60<>()\\[\\]{},;|]"
	unixSegment = "(?:" + segmentChar + "|\\\\.)+"
)

// unquotedPath is an absolute path where one can start: at the start of the text or after a
// character that cannot end a host, a word or a relative path, so URLs (https://host/a/b),
// fractions (1/2) and relative paths stay. It is Unix (/x, ~/x, file:///x), a Windows drive
// (C:\x) or a UNC share (\\server\share).
var unquotedPath = regexp.MustCompile(`(^|[^A-Za-z0-9/\\._~-])(?:` +
	`(?:file://)?/` + unixSegment + `(?:/` + unixSegment + `)*/?|` +
	`~/(?:` + unixSegment + `)?(?:/` + unixSegment + `)*/?|` +
	`[A-Za-z]:[\\/](?:` + segmentChar + `+[\\/]?)*|` +
	`\\\\` + segmentChar + `+(?:\\` + segmentChar + `*)*` +
	`)`)

// The clause swallowed after an unquoted path ends at a newline, a comma, a semicolon, a quote,
// a closing bracket or the end of the text, or where a URL starts, so a URL after a path is kept.
const pathClauseDelimiters = "\n,;\"'\x60)]}"

var urlStart = regexp.MustCompile(`(?i)\bhttps?://`)

// opaqueRun is a maximal run of token characters long enough to hold a secret.
var opaqueRun = regexp.MustCompile(`[A-Za-z0-9_-]{24,}`)

// scrubOutbound removes credentials and complete filesystem paths from text bound for the
// Worker.
func scrubOutbound(text string) string {
	for _, s := range credentialSubstitutions {
		text = s.pattern.ReplaceAllString(text, s.template)
	}
	for _, quoted := range quotedPaths {
		text = quoted.ReplaceAllStringFunc(text, keepQuotesAroundPath)
	}
	text = redactUnquotedPaths(text)
	return opaqueRun.ReplaceAllStringFunc(text, redactIfHighEntropy)
}

// redactUnquotedPaths replaces each unquoted path, with the rest of its clause, by <path>. A
// path that starts inside an earlier clause has gone with it.
func redactUnquotedPaths(text string) string {
	var out strings.Builder
	cursor := 0
	for _, match := range unquotedPath.FindAllStringSubmatchIndex(text, -1) {
		pathStart := match[3] // the end of the boundary character before the path
		if pathStart < cursor {
			continue
		}
		out.WriteString(text[cursor:pathStart])
		out.WriteString("<path>")
		cursor = pathClauseEnd(text, match[1])
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// pathClauseEnd is where the clause running on from a path that ends at from stops; the
// spaces before a delimiter or a URL are kept.
func pathClauseEnd(text string, from int) int {
	clause := text[from:]
	if i := strings.IndexAny(clause, pathClauseDelimiters); i >= 0 {
		clause = clause[:i]
	}
	if url := urlStart.FindStringIndex(clause); url != nil {
		clause = clause[:url[0]]
	}
	return from + len(strings.TrimRight(clause, " \t"))
}

// keepQuotesAroundPath replaces a quoted path with <path> inside the same quotes.
func keepQuotesAroundPath(quoted string) string {
	return quoted[:1] + "<path>" + quoted[len(quoted)-1:]
}

// redactIfHighEntropy is the catch-all for credentials without a known prefix: a run with a
// piece (between - and _) of 24 or more characters mixing letters and digits becomes <token>.
// Words, slugs, snake_case names and uuids (pieces of at most 12) stay; so do short commit
// SHAs, while a full 40-hex SHA is redacted, as it cannot be told from a hex key. Opencode
// session ids (ses_…) are left for incident.Redact, which names them <session>.
func redactIfHighEntropy(run string) string {
	if strings.HasPrefix(run, "ses_") {
		return run
	}
	for _, piece := range strings.FieldsFunc(run, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(piece) >= 24 && strings.ContainsAny(piece, digitChars) && strings.IndexFunc(piece, isASCIILetter) >= 0 {
			return "<token>"
		}
	}
	return run
}

const digitChars = "0123456789"

func isASCIILetter(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }
