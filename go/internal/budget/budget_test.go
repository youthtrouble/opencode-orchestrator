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
		want                     string
	}{
		{"policy off refuses", "", false, true, refused},
		{"policy on funds a recording caller", `{"openai_credits":"on"}`, false, true,
			`{"allowed":true,"level":"red","note":"openai budget is red; drawing ChatGPT credits (openai_credits=on).","credit_funded":true,"credits_before":"215.68"}`},
		{"a caller that records nothing is refused", `{"openai_credits":"on"}`, false, false, refused},
		{"frontier work never runs on credits", `{"openai_credits":"on"}`, true, true, refused},
		{"the floor is respected", `{"openai_credits":"on","openai_credits_floor":"300"}`, false, true, refused},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := configWithCache(t, claudeGreen, codexLimitReached)
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
