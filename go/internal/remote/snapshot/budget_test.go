package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeUsage(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestBudgetMapsTheCachedPostures(t *testing.T) {
	file := writeUsage(t, `{"generated_at":"2026-10-20T11:31:00.000Z",
		"claude":{"ok":true,"session":{"pct":90,"resets_at":"2026-10-20T13:00:00Z"},"weekly":{"pct":null}},
		"codex":{"ok":true,"five_hour":{"pct":10,"resets_at":"bogus"},"weekly":{"pct":140}}}`) // a weekly cap past 100 is red, but no percentage the schema accepts
	budget, err := Budget(testUsageConfig(t, file))
	if err != nil || budget == nil {
		t.Fatalf("Budget = %v, %v", budget, err)
	}
	got, _ := json.Marshal(budget)
	want := `{"anthropic":{"level":"red","five_hour_pct":90,"weekly_pct":null,"resets_at":"2026-10-20T13:00:00.000Z","window":"five_hour"},` +
		`"openai":{"level":"red","five_hour_pct":10,"weekly_pct":null,"resets_at":null,"window":"weekly"}}`
	if string(got) != want {
		t.Errorf("Budget = %s\nwant %s", got, want)
	}
}

func TestBudgetCarriesTheOpenAICreditsFact(t *testing.T) {
	file := writeUsage(t, `{"generated_at":"2026-10-20T11:31:00.000Z",
		"claude":{"ok":true,"session":{"pct":10},"weekly":{"pct":null}},
		"codex":{"ok":true,"five_hour":{"pct":88},"weekly":{"pct":100},"rate_limit_reached":"rate_limit_reached","ordinary_usage_allowed":false,
			"reset_credits_available":3,"credits":{"hasCredits":true,"unlimited":false,"balance":"215.68"}}}`)
	budget, err := Budget(testUsageConfig(t, file))
	if err != nil || budget == nil {
		t.Fatalf("Budget = %v, %v", budget, err)
	}
	got, _ := json.Marshal(budget)
	want := `{"anthropic":{"level":"green","five_hour_pct":10,"weekly_pct":null,"resets_at":null,"window":"five_hour"},` +
		`"openai":{"level":"red","five_hour_pct":88,"weekly_pct":100,"resets_at":null,"window":"weekly",` +
		`"credits":{"balance":"215.68","has_credits":true,"unlimited":false,"plan_limit_reached":true,"banked_resets":3}}}`
	if string(got) != want {
		t.Errorf("Budget = %s\nwant %s", got, want)
	}
}

// The window that drives a vendor's level is the weekly one only once that is red (when both
// are red, the weekly reset is the one that unblocks); an amber weekly window does not count.
func TestBudgetNamesTheDrivingWindowAndBothResets(t *testing.T) {
	file := writeUsage(t, `{"generated_at":"2026-10-20T11:31:00.000Z",
		"claude":{"ok":true,"session":{"pct":20,"resets_at":"2026-10-20T13:00:00Z"},"weekly":{"pct":70,"resets_at":"2026-10-25T03:00:00Z"}},
		"codex":{"ok":true,"five_hour":{"pct":95,"resets_at":"2026-10-20T14:00:00Z"},"weekly":{"pct":100,"resets_at":"2026-10-26T08:04:00Z"}}}`)
	budget, err := Budget(testUsageConfig(t, file))
	if err != nil || budget == nil {
		t.Fatalf("Budget = %v, %v", budget, err)
	}
	got, _ := json.Marshal(budget)
	want := `{"anthropic":{"level":"green","five_hour_pct":20,"weekly_pct":70,"resets_at":"2026-10-20T13:00:00.000Z",` +
		`"weekly_resets_at":"2026-10-25T03:00:00.000Z","window":"five_hour"},` +
		`"openai":{"level":"red","five_hour_pct":95,"weekly_pct":100,"resets_at":"2026-10-20T14:00:00.000Z",` +
		`"weekly_resets_at":"2026-10-26T08:04:00.000Z","window":"weekly"}}`
	if string(got) != want {
		t.Errorf("Budget = %s\nwant %s", got, want)
	}
}

func TestBudgetIsNullWithoutAFreshCache(t *testing.T) {
	cases := map[string]string{
		"no cache":            filepath.Join(t.TempDir(), "absent.json"),
		"older than 30 min":   writeUsage(t, `{"generated_at":"2026-10-20T11:29:59.000Z","claude":{"ok":true}}`),
		"no generated_at":     writeUsage(t, `{"claude":{"ok":true}}`),
		"not a usage reading": writeUsage(t, `null`),
	}
	for name, file := range cases {
		if budget, err := Budget(testUsageConfig(t, file)); budget != nil || err != nil {
			t.Errorf("%s: Budget = %v, %v; want nil, nil", name, budget, err)
		}
	}
}

func TestBudgetReportsAnUnreadableRegistry(t *testing.T) {
	cfg := testUsageConfig(t, filepath.Join("testdata", "usage", "usage.json"))
	cfg.RegistryFile = filepath.Join(t.TempDir(), "absent-models.json")
	if budget, err := Budget(cfg); budget != nil || err == nil {
		t.Errorf("Budget = %v, %v; want the registry error", budget, err)
	}
}

// A dry run must run nothing: with opencode's database present, role routing would detect
// channels with sqlite3 (and then `opencode auth list`), so both are planted on an otherwise
// empty PATH and must never be called.
func TestBudgetRunsNoProgramWithAFreshCache(t *testing.T) {
	home := t.TempDir()
	database := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(database), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	invocations := filepath.Join(t.TempDir(), "invocations")
	for _, program := range []string{"sqlite3", "opencode"} {
		script := "#!/bin/sh\necho " + program + " >> '" + invocations + "'\n"
		if err := os.WriteFile(filepath.Join(bin, program), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	cfg := testUsageConfig(t, writeUsage(t, `{"generated_at":"2026-10-20T11:59:00.000Z",
		"claude":{"ok":true,"session":{"pct":10}},"codex":{"ok":true,"five_hour":{"pct":10}}}`))
	cfg.Home = home
	if budget, err := Budget(cfg); budget == nil || err != nil {
		t.Fatalf("Budget = %v, %v; want the fresh cache's postures", budget, err)
	}
	if ran, err := os.ReadFile(invocations); err == nil {
		t.Errorf("Budget ran %q", ran)
	}
}
