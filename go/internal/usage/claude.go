package usage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// ClaudeUsageURL is the OAuth usage endpoint Claude Code itself reads (lib/claude-usage.mjs).
const ClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	claudeFetchTimeout = 15 * time.Second
	// keychainTimeout is a deliberate deviation (plan 3.10 item 2): the JS waits forever on a
	// locked keychain that prompts.
	keychainTimeout = 30 * time.Second
)

const noClaudeTokenError = "no Claude OAuth token found (run `opencode auth login` for Anthropic, or log in to Claude Code)"

// ClaudeToken is a Claude OAuth access token and where it came from. Plan and Tier are the
// raw values the source recorded (nil when it recorded none), copied into the usage entry.
type ClaudeToken struct {
	Token  string
	Source string
	Plan   any
	Tier   any
}

// FindClaudeToken mirrors findClaudeToken: opencode's own Anthropic OAuth login first (two
// auth.json locations), then Claude Code's keychain entry, then its credentials file.
func FindClaudeToken(home string, now time.Time) *ClaudeToken {
	if found := opencodeAuthToken(home, now); found != nil {
		return found
	}
	if found := keychainToken(now); found != nil {
		return found
	}
	return credentialsFileToken(home)
}

func opencodeAuthToken(home string, now time.Time) *ClaudeToken {
	candidates := []string{
		filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		filepath.Join(home, ".opencode", "auth.json"),
	}
	for _, file := range candidates {
		data, err := readJSONFile(file)
		if err != nil || data == nil {
			continue // JS: unreadable, unparsable or null (data.anthropic throws) -> next file
		}
		entry := jsjson.Path(data, "anthropic")
		access := jsjson.Path(entry, "access")
		if !jsjson.Truthy(entry) || jsjson.Path(entry, "type") != "oauth" || !jsjson.Truthy(access) {
			continue
		}
		expires, isNumber := jsjson.Path(entry, "expires").(float64)
		if isNumber && expires < float64(now.UnixMilli()) {
			continue
		}
		return &ClaudeToken{Token: jsjson.TemplateString(access), Source: "opencode-auth"}
	}
	return nil
}

// keychainToken reads Claude Code's macOS keychain entry through the security tool; on other
// systems (or when not logged in) the tool is missing or fails and there is no token.
func keychainToken(now time.Time) *ClaudeToken {
	raw, err := runQuiet("security", []string{"find-generic-password", "-s", "Claude Code-credentials", "-w"}, keychainTimeout)
	if err != nil {
		return nil
	}
	data, err := jsjson.Unmarshal([]byte(raw))
	if err != nil || data == nil {
		return nil
	}
	oauth := jsjson.Path(data, "claudeAiOauth")
	access := jsjson.Path(oauth, "accessToken")
	if !jsjson.Truthy(access) || !unexpired(jsjson.Path(oauth, "expiresAt"), now) {
		return nil
	}
	return &ClaudeToken{
		Token:  jsjson.TemplateString(access),
		Source: "claude-code-keychain",
		Plan:   jsjson.Path(oauth, "subscriptionType"),
		Tier:   jsjson.Path(oauth, "rateLimitTier"),
	}
}

// unexpired is `!expiresAt || expiresAt > Date.now()`. A non-numeric truthy expiresAt compares
// as NaN in the JS (false); the coercion of numeric strings is not modelled.
func unexpired(expiresAt any, now time.Time) bool {
	if !jsjson.Truthy(expiresAt) {
		return true
	}
	ms, isNumber := expiresAt.(float64)
	return isNumber && ms > float64(now.UnixMilli())
}

// credentialsFileToken is Claude Code's file-based store; its expiry is not checked and it
// records no tier (mirrored from the JS).
func credentialsFileToken(home string) *ClaudeToken {
	data, err := readJSONFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || data == nil {
		return nil
	}
	oauth := jsjson.Path(data, "claudeAiOauth")
	access := jsjson.Path(oauth, "accessToken")
	if !jsjson.Truthy(access) {
		return nil
	}
	return &ClaudeToken{
		Token:  jsjson.TemplateString(access),
		Source: "claude-code-credentials-file",
		Plan:   jsjson.Path(oauth, "subscriptionType"),
	}
}

func readJSONFile(file string) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return jsjson.Unmarshal(data)
}

// Error texts of Node's fetch, so a cache written by either runtime reads the same.
const (
	fetchAbortedError = "This operation was aborted"
	fetchFailedError  = "fetch failed"
)

// FetchClaudeUsageRaw mirrors fetchClaudeUsageRaw: GET the usage endpoint with the token,
// abort after timeout, fail with "usage endpoint <status>" on a non-2xx answer, and return
// the parsed JSON body. The request carries the headers Node's fetch adds (User-Agent
// "node" and friends) so the endpoint sees the same client whichever runtime asks.
func FetchClaudeUsageRaw(url, token string, timeout time.Duration) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "*")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("User-Agent", "node")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fetchError(ctx)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("usage endpoint %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fetchError(ctx)
	}
	return jsjson.Unmarshal(body)
}

// fetchError maps a transport failure to the message Node's fetch gives; Node keeps the
// cause out of the message too.
func fetchError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New(fetchAbortedError)
	}
	return errors.New(fetchFailedError)
}

// NormalizeClaudeUsage mirrors normalizeClaudeUsage: the few numbers routing needs, from the
// limits array when present, else from the legacy five_hour/seven_day windows. raw must not
// be JSON null (the JS throws reading raw.limits; see ReadClaudeUsage).
func NormalizeClaudeUsage(raw any, meta *ClaudeToken) *jsjson.Object {
	limits, _ := jsjson.Path(raw, "limits").([]any)
	session := findLimit(limits, func(l any) bool { return jsjson.Path(l, "kind") == "session" })
	weeklyAll := findLimit(limits, func(l any) bool {
		return jsjson.Path(l, "group") == "weekly" && !jsjson.Truthy(jsjson.Path(l, "scope"))
	})
	five := jsjson.Path(raw, "five_hour")
	seven := jsjson.Path(raw, "seven_day")

	out := jsjson.NewObject()
	out.Set("ok", true)
	out.Set("source", orNull(meta.Source))
	out.Set("plan", orNull(meta.Plan))
	out.Set("tier", orNull(meta.Tier))
	out.Set("session", window(session, five))
	out.Set("weekly", window(weeklyAll, seven))
	out.Set("weekly_by_model", weeklyByModel(limits))
	out.Set("extra_usage_enabled", jsjson.Truthy(jsjson.Path(raw, "extra_usage", "is_enabled")))
	out.Set("locked_reason", orNull(jsjson.Path(five, "locked_reason")))
	return out
}

func findLimit(limits []any, matches func(any) bool) any {
	for _, l := range limits {
		if matches(l) {
			return l
		}
	}
	return nil
}

// window is {pct, resets_at, severity} of a limit, falling back to the legacy window's
// utilization (rounded) and resets_at.
func window(limit, legacy any) *jsjson.Object {
	var legacyPct any
	if utilization, isNumber := jsjson.Path(legacy, "utilization").(float64); isNumber {
		legacyPct = jsjson.Round(utilization)
	}
	out := jsjson.NewObject()
	out.Set("pct", nullish(jsjson.Path(limit, "percent"), legacyPct))
	out.Set("resets_at", nullish(jsjson.Path(limit, "resets_at"), jsjson.Path(legacy, "resets_at")))
	out.Set("severity", jsjson.Path(limit, "severity"))
	return out
}

// weeklyByModel keys the model-scoped weekly limits by lowercased model name. Fields the
// limit lacks are omitted, as JSON.stringify drops undefined.
func weeklyByModel(limits []any) *jsjson.Object {
	byModel := jsjson.NewObject()
	for _, l := range limits {
		model := jsjson.Path(l, "scope", "model")
		if jsjson.Path(l, "group") != "weekly" || !jsjson.Truthy(model) {
			continue
		}
		name := jsjson.OrText(jsjson.Path(model, "display_name"), jsjson.OrText(jsjson.Path(model, "id"), "model"))
		entry := jsjson.NewObject()
		for _, field := range [][2]string{{"pct", "percent"}, {"resets_at", "resets_at"}, {"severity", "severity"}, {"active", "is_active"}} {
			if value, present := jsjson.Lookup(l, field[1]); present {
				entry.Set(field[0], value)
			}
		}
		byModel.Set(strings.ToLower(name), entry)
	}
	return byModel
}

// ReadClaudeUsage mirrors readClaudeUsage: {ok:false, error} without a token, the normalized
// usage on success, {ok:false, source, error} when the fetch or normalization fails.
func ReadClaudeUsage(cfg Config) *jsjson.Object {
	found := FindClaudeToken(cfg.Home, cfg.Now())
	if found == nil {
		return failure(noClaudeTokenError)
	}
	raw, err := FetchClaudeUsageRaw(cfg.ClaudeUsageURL, found.Token, cfg.ClaudeTimeout)
	if err == nil && raw == nil {
		err = errors.New("Cannot read properties of null (reading 'limits')")
	}
	if err != nil {
		out := jsjson.NewObject()
		out.Set("ok", false)
		out.Set("source", found.Source)
		out.Set("error", err.Error())
		return out
	}
	return NormalizeClaudeUsage(raw, found)
}

// failure is the {ok:false, error} entry of a feed that could not be read.
func failure(message string) *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("ok", false)
	out.Set("error", message)
	return out
}

// orNull is `v || null`.
func orNull(v any) any {
	if jsjson.Truthy(v) {
		return v
	}
	return nil
}

// nullish is `a ?? b` (absent and null alike).
func nullish(a, b any) any {
	if a != nil {
		return a
	}
	return b
}
