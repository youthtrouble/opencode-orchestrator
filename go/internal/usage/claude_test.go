package usage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Token values are placeholders; no real credential ever enters a test.
const (
	futureMs = 1893456000000 // 2030-01-01
	pastMs   = 1577836800000 // 2020-01-01
)

func TestFindClaudeTokenFollowsTheSourceOrder(t *testing.T) {
	const opencodeAuth = ".local/share/opencode/auth.json"
	const legacyAuth = ".opencode/auth.json"
	keychainJSON := func(token string, expiresAt int64) string {
		return fmt.Sprintf(`echo '{"claudeAiOauth":{"accessToken":%q,"expiresAt":%d,"subscriptionType":"team","rateLimitTier":"tier-x"}}'`, token, expiresAt)
	}
	cases := []struct {
		name     string
		files    map[string]string
		keychain string // body of the fake security; "" keeps the failing default
		want     *ClaudeToken
	}{
		{"nothing anywhere", nil, "", nil},
		{"opencode oauth login wins",
			map[string]string{opencodeAuth: fmt.Sprintf(`{"anthropic":{"type":"oauth","access":"tok-a","expires":%d}}`, futureMs)},
			keychainJSON("tok-k", futureMs),
			&ClaudeToken{Token: "tok-a", Source: "opencode-auth"}},
		{"expired opencode login falls to the legacy auth.json",
			map[string]string{
				opencodeAuth: fmt.Sprintf(`{"anthropic":{"type":"oauth","access":"tok-old","expires":%d}}`, pastMs),
				legacyAuth:   `{"anthropic":{"type":"oauth","access":"tok-legacy"}}`,
			}, "", &ClaudeToken{Token: "tok-legacy", Source: "opencode-auth"}},
		{"an api-key login and a null auth.json are skipped",
			map[string]string{opencodeAuth: `{"anthropic":{"type":"api","key":"k"}}`, legacyAuth: `null`},
			keychainJSON("tok-k", futureMs),
			&ClaudeToken{Token: "tok-k", Source: "claude-code-keychain", Plan: "team", Tier: "tier-x"}},
		{"expired keychain entry falls to the credentials file",
			map[string]string{".claude/.credentials.json": fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok-f","expiresAt":%d,"subscriptionType":"pro","rateLimitTier":"ignored"}}`, pastMs)},
			keychainJSON("tok-k", pastMs),
			&ClaudeToken{Token: "tok-f", Source: "claude-code-credentials-file", Plan: "pro"}},
		{"keychain entry without expiry is valid",
			nil, `echo '{"claudeAiOauth":{"accessToken":"tok-k"}}'`,
			&ClaudeToken{Token: "tok-k", Source: "claude-code-keychain"}},
		{"keychain output that is not JSON falls through",
			nil, `echo 'not json'`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			for rel, content := range c.files {
				s.writeFile(t, rel, content)
			}
			if c.keychain != "" {
				s.fake(t, "security", `[ "$*" = "find-generic-password -s Claude Code-credentials -w" ] || exit 9`+"\n"+c.keychain)
			}
			got := FindClaudeToken(s.home, fixedNow)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestFetchClaudeUsageRawSendsTheHeadersNodeSends(t *testing.T) {
	fixture, err := os.ReadFile(testdataDir + "/claude-oauth-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Authorization":   "Bearer tok-test",
		"Anthropic-Beta":  "oauth-2025-04-20",
		"Content-Type":    "application/json",
		"Accept":          "*/*",
		"Accept-Language": "*",
		"Sec-Fetch-Mode":  "cors",
		"User-Agent":      "node",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range want {
			if got := r.Header.Get(name); got != value {
				t.Errorf("header %s = %q, want %q", name, got, value)
			}
		}
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		w.Write(fixture)
	}))
	defer server.Close()

	raw, err := FetchClaudeUsageRaw(server.URL, "tok-test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := indented(t, raw); !strings.Contains(got, `"display_name": "Fable"`) {
		t.Errorf("body not parsed: %s", got)
	}
}

func TestFetchClaudeUsageRawErrorsReadLikeNode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/denied":
			w.WriteHeader(http.StatusUnauthorized)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		}
	}))
	defer server.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	cases := []struct {
		name, url, want string
	}{
		{"non-2xx", server.URL + "/denied", "usage endpoint 401"},
		{"timeout aborts", server.URL + "/slow", "This operation was aborted"},
		{"connection refused", closedURL, "fetch failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FetchClaudeUsageRaw(c.url, "tok-test", 100*time.Millisecond)
			if err == nil || err.Error() != c.want {
				t.Errorf("error %v, want %q", err, c.want)
			}
		})
	}
}

// The goldens are normalizeClaudeUsage's output for the same inputs (testdata/usage/gen.mjs).
func TestNormalizeClaudeUsageMatchesJS(t *testing.T) {
	cases := []struct {
		fixture string
		meta    *ClaudeToken
	}{
		{"claude-oauth-usage", &ClaudeToken{Source: "claude-code-keychain", Plan: "team", Tier: "default_claude_max_5x"}},
		{"claude-legacy", &ClaudeToken{Source: "claude-code-credentials-file", Plan: "pro"}},
		{"claude-sparse", &ClaudeToken{Source: "opencode-auth"}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			got := indented(t, NormalizeClaudeUsage(readFixture(t, c.fixture+".json"), c.meta))
			if want := readGolden(t, c.fixture+".golden"); got != want {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestReadClaudeUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"limits":[{"kind":"session","percent":12,"resets_at":"2026-10-18T23:00:00+00:00","severity":"normal"}]}`))
		case "/null":
			w.Write([]byte(`null`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	cases := []struct {
		name, path string
		withToken  bool
		want       string
	}{
		{"no token", "/ok", false, `{"ok":false,"error":"no Claude OAuth token found (run ` + "`opencode auth login`" + ` for Anthropic, or log in to Claude Code)"}`},
		{"fetched", "/ok", true, `{"ok":true,"source":"opencode-auth","plan":null,"tier":null,"session":{"pct":12,"resets_at":"2026-10-18T23:00:00+00:00","severity":"normal"},"weekly":{"pct":null,"resets_at":null,"severity":null},"weekly_by_model":{},"extra_usage_enabled":false,"locked_reason":null}`},
		{"endpoint failure", "/fail", true, `{"ok":false,"source":"opencode-auth","error":"usage endpoint 500"}`},
		{"null body", "/null", true, `{"ok":false,"source":"opencode-auth","error":"Cannot read properties of null (reading 'limits')"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			if c.withToken {
				s.writeFile(t, ".local/share/opencode/auth.json", `{"anthropic":{"type":"oauth","access":"tok-test"}}`)
			}
			if got := compact(t, ReadClaudeUsage(s.config(server.URL+c.path))); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}
