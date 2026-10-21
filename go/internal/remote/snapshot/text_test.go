package snapshot

import (
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
)

func TestCleanerRedactsThenCutsWithinTheCap(t *testing.T) {
	clean := cleaner{home: "/srv/owner-home"}
	cases := []struct {
		in       string
		maxUnits int
		want     string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven chars", 10, "eleven ch…"},
		{"see /srv/owner-home/a.md", 40, "see <path>"},
		{"see " + fakeToken("ghp_", 36) + " now", 40, "see <token> now"},
		{"mail owner@example.com", 40, "mail <email>"},
		{"key sk-" + strings.Repeat("A", 24), 40, "key <token>"},
		// 😀 is two UTF-16 units: the cut never splits it.
		{"ab😀cd", 4, "ab…"},
		{"ab😀cd", 5, "ab😀…"},
		{"", 10, ""},
	}
	for _, c := range cases {
		if got := clean.text(c.in, c.maxUnits); got != c.want {
			t.Errorf("text(%q, %d) = %q, want %q", c.in, c.maxUnits, got, c.want)
		}
	}
}

// fakeToken is prefix followed by n filler characters: a credential shape built at run time,
// so no token-shaped literal sits in the repository for secret scanners to report.
func fakeToken(prefix string, n int) string {
	return prefix + strings.Repeat("X", n)
}

func TestScrubOutboundRemovesCredentials(t *testing.T) {
	cases := map[string]string{
		"github classic":    "use " + fakeToken("ghp_", 36) + " here",
		"github oauth":      "use " + fakeToken("gho_", 36) + " here",
		"github user":       "use " + fakeToken("ghu_", 36) + " here",
		"github server":     "use " + fakeToken("ghs_", 36) + " here",
		"github refresh":    "use " + fakeToken("ghr_", 36) + " here",
		"github fine":       "use " + fakeToken("github_"+"pat_", 82) + " here",
		"aws access key":    "use " + fakeToken("AK"+"IA", 16) + " here",
		"aws session key":   "use " + fakeToken("AS"+"IA", 16) + " here",
		"slack":             "use " + fakeToken("xo"+"xb-", 24) + " here",
		"google":            "use " + fakeToken("AI"+"za", 35) + " here",
		"stripe live":       "use " + fakeToken("sk_"+"live_", 24) + " here",
		"stripe restricted": "use " + fakeToken("rk_"+"live_", 24) + " here",
		"npm":               "use " + fakeToken("np"+"m_", 36) + " here",
		"jwt":               "use " + fakeToken("ey"+"J", 12) + "." + fakeToken("ey"+"J", 12) + "." + fakeToken("", 20) + " here",
	}
	for name, text := range cases {
		if got := scrubOutbound(text); got != "use <token> here" {
			t.Errorf("%s: scrubOutbound = %q", name, got)
		}
	}
}

// A provider prefix glued to a word (an underscore is a word character, so \b never fired).
func TestScrubOutboundRemovesProviderTokensGluedToAWord(t *testing.T) {
	for _, prefix := range []string{"backup_", "my-", "x"} {
		for _, token := range []string{fakeToken("ghp_", 36), fakeToken("github_"+"pat_", 40), fakeToken("AK"+"IA", 16), fakeToken("np"+"m_", 24)} {
			if got := scrubOutbound("use " + prefix + token + " here"); got != "use "+prefix+"<token> here" {
				t.Errorf("scrubOutbound(%q) = %q", prefix+token, got)
			}
		}
	}
}

// fakeSecret is a credential-shaped run without a known prefix, mixing letters and digits.
func fakeSecret(n int) string {
	return strings.Repeat("a1B2", n/4)
}

func TestScrubOutboundRemovesHighEntropyRunsButNotWordsOrIds(t *testing.T) {
	cases := []struct{ in, want string }{
		{"key " + fakeSecret(32) + " set", "key <token> set"},
		{"key backup_" + fakeSecret(24) + ".", "key <token>."},
		{"full sha " + strings.Repeat("0a", 20), "full sha <token>"},
		{"short sha 0a1b2c3d4e5f and 0a1b2c3", "short sha 0a1b2c3d4e5f and 0a1b2c3"},
		{"id 0a1b2c3d-0a1b-4c2d-8e3f-0a1b2c3d4e5f", "id 0a1b2c3d-0a1b-4c2d-8e3f-0a1b2c3d4e5f"},
		{"ses_" + fakeSecret(28), "ses_" + fakeSecret(28)},
		{"TestBudgetRunsNoProgramWithAFreshCache and wt-12-snapshot-outbound-scrubbing-v2", "TestBudgetRunsNoProgramWithAFreshCache and wt-12-snapshot-outbound-scrubbing-v2"},
		{"snake_case_name_with_digits_2026_10_21", "snake_case_name_with_digits_2026_10_21"},
	}
	for _, c := range cases {
		if got := scrubOutbound(c.in); got != c.want {
			t.Errorf("scrubOutbound(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestScrubOutboundRemovesCredentialValuesAndBlocks(t *testing.T) {
	cases := []struct{ in, want string }{
		{"curl -H 'Authorization: Bearer " + fakeToken("", 24) + "'", "curl -H 'Authorization: Bearer <token>'"},
		{"Bearer authentication is fine", "Bearer authentication is fine"},
		{"password=fake-pass-1 and db_password: fake2", "password=<redacted> and db_password: <redacted>"},
		{`{"api_key": "fake value", "authToken":'x'}`, `{"api_key": <redacted>, "authToken":<redacted>}`},
		{"SECRET=abc; next", "SECRET=<redacted>; next"},
		{"tokens: 5 used", "tokens: 5 used"},
		{"author: someone", "author: someone"},
		{"clone https://bot:fakepass@git.example.com/org/repo", "clone https://<credentials>@git.example.com/org/repo"},
		{"key:\n-----BEGIN RSA PRIVATE KEY-----\nFAKE\n-----END RSA PRIVATE KEY-----\nafter", "key:\n<private key>\nafter"},
		{"cut -----BEGIN PRIVATE KEY-----\nFAKE", "cut <private key>"},
	}
	for _, c := range cases {
		if got := scrubOutbound(c.in); got != c.want {
			t.Errorf("scrubOutbound(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestScrubOutboundRemovesCompletePathsButNotURLsOrFractions(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Review /Volumes/PrivateClient/contracts/draft.pdf now", "Review <path>"},
		{"in /private/var/folders/x and /srv/data/", "in <path>"},
		{"(/tmp/run/log), /opt/tool/bin; /var/db/x", "(<path>), <path>; <path>"},
		{"edit ~/notes/plan.md", "edit <path>"},
		{"open file:///Users/someone/a.txt", "open <path>"},
		{`dir="/srv/app/x"`, `dir="<path>"`},
		{"see https://trains.example.com/a/b and http://host:8080/c/d", "see https://trains.example.com/a/b and http://host:8080/c/d"},
		{"from /srv/a/b see https://trains.example.com/a/b, 1/2 done", "from <path> https://trains.example.com/a/b, 1/2 done"},
		{"from /srv/a/b and HTTP://host/c/d", "from <path> HTTP://host/c/d"},
		{"1/2 done on 2026/10/21, in go/internal/remote and ./a/b", "1/2 done on 2026/10/21, in go/internal/remote and ./a/b"},
		{"the root / and and/or w/o", "the root / and and/or w/o"},
	}
	for _, c := range cases {
		if got := scrubOutbound(c.in); got != c.want {
			t.Errorf("scrubOutbound(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// D11: no part of a path is sent, whatever its shape.
func TestScrubOutboundRemovesWholePathsWithSpacesSingleComponentsAndWindowsForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{`see "/Volumes/Private Client/contracts/draft.pdf" now`, `see "<path>" now`},
		{"see '/Volumes/Private Client/a b.pdf' now", "see '<path>' now"},
		{"see `~/Private Client/notes.md` now", "see `<path>` now"},
		{`see "C:\Users\Private Client\a.txt"`, `see "<path>"`},
		{"see /Volumes/Private Client/contracts/draft.pdf now", "see <path>"},
		{`see /Volumes/Private\ Client/draft.pdf, now`, "see <path>, now"},
		{"load /private-client.env; /tmp then stop", "load <path>; <path>"},
		{`open C:\Users\PrivateClient\a.txt, now`, "open <path>, now"},
		{`open C:\Users\Private Client\a.txt now`, "open <path>"},
		{"open D:/Clients/PrivateClient/a.txt now", "open <path>"},
		{`mount \\fileserver\PrivateClient\x now`, "mount <path>"},
		{"it's /srv/a, isn't it", "it's <path>, isn't it"},
	}
	for _, c := range cases {
		if got := scrubOutbound(c.in); got != c.want {
			t.Errorf("scrubOutbound(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An unquoted final component may hold a space, so the rest of the clause goes with the path,
// whatever its case or prefix.
func TestScrubOutboundRemovesTheClauseAfterAnUnquotedPath(t *testing.T) {
	for _, path := range []string{"/Volumes/Private Client", "/Volumes/Private client", "/srv/private customer", "~/Private Client", `C:\Users\private client`} {
		cases := []struct{ in, want string }{
			{"see " + path, "see <path>"},
			{"see " + path + "\nnext", "see <path>\nnext"},
			{"see " + path + ", next", "see <path>, next"},
			{"see " + path + "; next", "see <path>; next"},
			{`see "x ` + path + `" next`, `see "x <path>" next`},
			{"see 'x " + path + "' next", "see 'x <path>' next"},
			{"see `x " + path + "` next", "see `x <path>` next"},
			{"(see " + path + ") next", "(see <path>) next"},
			{"[see " + path + "] next", "[see <path>] next"},
			{"{see " + path + "} next", "{see <path>} next"},
			{"see " + path + " at https://trains.example.com/a/b now", "see <path> https://trains.example.com/a/b now"},
		}
		for _, c := range cases {
			if got := scrubOutbound(c.in); got != c.want {
				t.Errorf("scrubOutbound(%q) = %q, want %q", c.in, got, c.want)
			}
		}
	}
}

func TestCleanerMarksTextRedactCutAtItsOwnCap(t *testing.T) {
	long := strings.Repeat("y", 3000)
	if got := incident.Redact("/h", long); utf16Length(got) != redactCutUnits {
		t.Fatalf("incident.Redact cut at %d units, not redactCutUnits (%d)", utf16Length(got), redactCutUnits)
	}
	got := cleaner{home: "/h"}.text(long, maxBodyUnits)
	if utf16Length(got) != maxBodyUnits || !strings.HasSuffix(got, ellipsis) {
		t.Errorf("a body cut by Redact is %d units, ends %q; want %d ending in …", utf16Length(got), got[len(got)-8:], maxBodyUnits)
	}
}

func TestOptionalIsNullForEmptyText(t *testing.T) {
	clean := cleaner{home: "/h"}
	if got := clean.optional("", 10); got != nil {
		t.Errorf("optional(\"\") = %q, want nil", *got)
	}
	if got := clean.optional("x", 10); got == nil || *got != "x" {
		t.Errorf("optional(\"x\") = %v", got)
	}
}
