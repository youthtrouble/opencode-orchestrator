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
	want := `{"anthropic":{"level":"red","five_hour_pct":90,"weekly_pct":null,"resets_at":"2026-10-20T13:00:00.000Z"},` +
		`"openai":{"level":"red","five_hour_pct":10,"weekly_pct":null,"resets_at":null}}`
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
