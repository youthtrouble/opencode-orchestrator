package budget

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

var now = time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)

// configWithCache is a usage config whose cache is fresh and holds the given feed entries,
// so the gate never collects. PATH holds nothing, so no real tool can be reached.
func configWithCache(t *testing.T, claude, codex string) usage.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	cfg := usage.Config{
		Home:         home,
		UsageFile:    filepath.Join(home, "usage.json"),
		RegistryFile: "../../../orchestrator/models.json",
		CodexBin:     "codex",
		CodexTimeout: time.Minute,
		Now:          func() time.Time { return now },
	}
	doc := `{"generated_at":"2026-10-18T19:59:00.000Z","claude":` + claude + `,"codex":` + codex + `}`
	if err := os.WriteFile(cfg.UsageFile, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

const (
	unknownFeed = `{"ok":false,"error":"x"}`
	claudeGreen = `{"ok":true,"session":{"pct":10,"resets_at":"2026-10-18T23:00:00Z"},"weekly":{"pct":null},"weekly_by_model":{"fable":{"pct":20,"resets_at":"2026-10-25T00:00:00Z"}}}`
	// Session red; Fable resets are the fallback when the session window has none.
	claudeSessionRed        = `{"ok":true,"session":{"pct":97,"resets_at":"2026-10-18T23:00:00Z"},"weekly":{"pct":null},"weekly_by_model":{"fable":{"pct":20,"resets_at":"2026-10-25T00:00:00Z"}}}`
	claudeSessionRedNoReset = `{"ok":true,"session":{"pct":97,"resets_at":null},"weekly":{"pct":null},"weekly_by_model":{"fable":{"pct":20,"resets_at":"2026-10-25T00:00:00Z"}}}`
	claudeRedNoResets       = `{"ok":true,"session":{"pct":97},"weekly":{"pct":null},"weekly_by_model":{}}`
	claudeFableRed          = `{"ok":true,"session":{"pct":10,"resets_at":"2026-10-18T23:00:00Z"},"weekly":{"pct":null},"weekly_by_model":{"fable":{"pct":99,"resets_at":"2026-10-25T00:00:00Z"}}}`
	codexRed                = `{"ok":true,"five_hour":{"pct":100,"resets_at":null},"weekly":{"pct":10,"resets_at":"2026-10-24T00:00:00.000Z"}}`
)

func TestGate(t *testing.T) {
	cases := []struct {
		name            string
		claude, codex   string
		vendor          string
		force, frontier bool
		want            string
	}{
		{"unknown usage proceeds with a note", unknownFeed, unknownFeed, "openai", false, false,
			`{"allowed":true,"note":"openai usage unknown; proceeding"}`},
		{"green is allowed with its level", claudeGreen, unknownFeed, "anthropic", false, false,
			`{"allowed":true,"level":"green"}`},
		{"red is refused until the session resets", claudeSessionRed, unknownFeed, "anthropic", false, false,
			`{"allowed":false,"level":"red","resets_at":"2026-10-18T23:00:00Z","note":"anthropic budget is red; refusing to start (pass --force to override). Resets 2026-10-18T23:00:00Z."}`},
		{"a null session reset falls back to Fable's", claudeSessionRedNoReset, unknownFeed, "anthropic", false, false,
			`{"allowed":false,"level":"red","resets_at":"2026-10-25T00:00:00Z","note":"anthropic budget is red; refusing to start (pass --force to override). Resets 2026-10-25T00:00:00Z."}`},
		{"no reset known omits resets_at and prints undefined", claudeRedNoResets, unknownFeed, "anthropic", false, false,
			`{"allowed":false,"level":"red","note":"anthropic budget is red; refusing to start (pass --force to override). Resets undefined."}`},
		{"force overrides red", claudeSessionRed, unknownFeed, "anthropic", true, false,
			`{"allowed":true,"level":"red"}`},
		{"Fable red only matters for frontier work", claudeFableRed, unknownFeed, "anthropic", false, false,
			`{"allowed":true,"level":"green"}`},
		{"frontier work checks the frontier level", claudeFableRed, unknownFeed, "anthropic", false, true,
			`{"allowed":false,"level":"red","resets_at":"2026-10-18T23:00:00Z","note":"anthropic budget is red; refusing to start (pass --force to override). Resets 2026-10-18T23:00:00Z."}`},
		{"openai falls back from the 5-hour to the weekly reset", unknownFeed, codexRed, "openai", false, false,
			`{"allowed":false,"level":"red","resets_at":"2026-10-24T00:00:00.000Z","note":"openai budget is red; refusing to start (pass --force to override). Resets 2026-10-24T00:00:00.000Z."}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := configWithCache(t, c.claude, c.codex)
			d, err := Gate(cfg, c.vendor, c.force, c.frontier, false)
			if err != nil {
				t.Fatal(err)
			}
			got, err := jsjson.Marshal(d.JSON())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
			if d.Routing == nil {
				t.Error("decision without its routing")
			}
		})
	}
}

// codexLimitReached is past its weekly plan limit with 215.68 ChatGPT credits left.
const codexLimitReached = `{"ok":true,"five_hour":{"pct":88,"resets_at":"2026-10-18T22:00:00.000Z"},"weekly":{"pct":100,"resets_at":"2026-10-24T00:00:00.000Z"},` +
	`"rate_limit_reached":"rate_limit_reached","ordinary_usage_allowed":false,"reset_credits_available":3,"credits":{"hasCredits":true,"unlimited":false,"balance":"215.68"}}`

func TestGateDrawsCreditsOnlyWithThePolicyAndARecordingCaller(t *testing.T) {
	const refused = `{"allowed":false,"level":"red","resets_at":"2026-10-18T22:00:00.000Z","note":"openai budget is red; refusing to start (pass --force to override). Resets 2026-10-18T22:00:00.000Z."}`
	cases := []struct {
		name                     string
		policy                   string // the project's policy.json; "" for none
		frontier, mayDrawCredits bool
		codex                    func(*testing.T) string // the codex a fresh reading comes from
		want                     string
	}{
		{"policy off refuses", "", false, true, codexAnswering("215.68"), refused},
		{"policy on funds a recording caller", `{"openai_credits":"on"}`, false, true, codexAnswering("215.68"),
			`{"allowed":true,"level":"red","note":"openai budget is red; drawing ChatGPT credits (openai_credits=on).","credit_funded":true,"credits_before":"215.68"}`},
		{"a caller that records nothing is refused", `{"openai_credits":"on"}`, false, false, codexAnswering("215.68"), refused},
		{"frontier work never runs on credits", `{"openai_credits":"on"}`, true, true, codexAnswering("215.68"), refused},
		{"the floor is respected", `{"openai_credits":"on","openai_credits_floor":"300"}`, false, true, codexAnswering("215.68"), refused},
		{"the floor is checked against the fresh balance", `{"openai_credits":"on","openai_credits_floor":"20"}`, false, true, codexAnswering("10"), refused},
		{"a failed fresh reading refuses the draw", `{"openai_credits":"on"}`, false, true, codexFailing,
			`{"allowed":false,"level":"red","note":"openai budget is red and the ChatGPT credits balance could not be re-read (` + codexFailure + `); refusing to draw credits (pass --force to override)."}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := configWithCache(t, claudeGreen, codexLimitReached)
			cfg.CodexBin = c.codex(t)
			cfg.ProjectRoot = t.TempDir()
			if c.policy != "" {
				if err := os.MkdirAll(filepath.Join(cfg.ProjectRoot, ".orchestrator"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.ProjectRoot, ".orchestrator", "policy.json"), []byte(c.policy), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := Gate(cfg, "openai", false, c.frontier, c.mayDrawCredits)
			if err != nil {
				t.Fatal(err)
			}
			got, err := jsjson.Marshal(d.JSON())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestFreshCreditsBalance(t *testing.T) {
	cfg := configWithCache(t, claudeGreen, codexLimitReached)
	cfg.CodexBin = codexAnswering("190.5")(t)
	if got := FreshCreditsBalance(cfg); got != "190.5" {
		t.Errorf("fresh balance %v, want 190.5", got)
	}
	// A failed refresh keeps the last good reading (read_error set): its balance is stale.
	cfg = configWithCache(t, claudeGreen, codexLimitReached)
	cfg.CodexBin = codexFailing(t)
	if got := FreshCreditsBalance(cfg); got != nil {
		t.Errorf("balance after a failed refresh %v, want nil", got)
	}
}

// codexAnswering is a fake `codex app-server` reporting the weekly plan limit reached with
// balance ChatGPT credits left. It answers initialize, account/read and rateLimits/read by
// line (the second line is the initialized notification); PATH is empty, so it uses no tool.
func codexAnswering(balance string) func(*testing.T) string {
	return func(t *testing.T) string {
		limits := `{"ordinaryUsageAllowed":false,"rateLimits":{"primary":{"usedPercent":88,"windowDurationMins":300,"resetsAt":1792360800},` +
			`"secondary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1792800000},"rateLimitReachedType":"rate_limit_reached","planType":"plus",` +
			`"credits":{"hasCredits":true,"unlimited":false,"balance":"` + balance + `"}},"rateLimitResetCredits":{"availableCount":3}}`
		return fakeCodex(t, `n=0
while IFS= read -r line; do
  n=$((n+1))
  case $n in
    1) echo '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
    3) echo '{"jsonrpc":"2.0","id":2,"result":{"account":{"planType":"plus"}}}' ;;
    4) echo '{"jsonrpc":"2.0","id":3,"result":`+limits+`}' ;;
  esac
done`)
	}
}

// codexFailure is the error of codexFailing, whose app-server refuses initialize (an exit
// without an answer would only fail after the app-server timeout).
const codexFailure = "not logged in"

func codexFailing(t *testing.T) string {
	return fakeCodex(t, `read -r line; echo '{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"`+codexFailure+`"}}'`)
}

func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestGateProceedsWhenRoutingFails(t *testing.T) {
	cfg := configWithCache(t, unknownFeed, unknownFeed)
	cfg.RegistryFile = filepath.Join(cfg.Home, "missing.json")
	d, err := Gate(cfg, "anthropic", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || d.Routing != nil || d.Level != "" || d.Note == "" {
		t.Errorf("decision %+v", d)
	}
}

// In the JS a codex spawn failure crashes the process before budgetGate can catch it.
func TestGateReturnsACodexSpawnFailure(t *testing.T) {
	cfg := configWithCache(t, unknownFeed, unknownFeed)
	if err := os.Remove(cfg.UsageFile); err != nil {
		t.Fatal(err)
	}
	cfg.ClaudeUsageURL = "http://127.0.0.1:1/unreachable"
	var spawnErr *usage.SpawnError
	if _, err := Gate(cfg, "anthropic", false, false, false); !errors.As(err, &spawnErr) {
		t.Errorf("error %v, want a SpawnError", err)
	}
}
