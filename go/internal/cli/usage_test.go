package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// usageEnv points the usage commands at a temp HOME, the checkout's registry and a PATH
// without tools, and returns the usage cache path.
func usageEnv(t *testing.T) (usageFile string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORCH_HOME", "")
	t.Setenv("ORCH_REGISTRY", "../../../orchestrator/models.json")
	t.Setenv("PATH", t.TempDir())
	return filepath.Join(home, ".local", "state", "opencode", "orchestrator", "usage.json")
}

func writeUsageCache(t *testing.T, file string, generatedAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"generated_at":"` + jsjson.ISOString(generatedAt) + `","claude":{"ok":false,"error":"a"},"codex":{"ok":false,"error":"b"}}`
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsageJSONFromAFreshCache(t *testing.T) {
	writeUsageCache(t, usageEnv(t), time.Now())
	code, stdout, stderr := run(t, Usage, []string{"--json"}, "")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	doc, err := jsjson.Unmarshal([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	keys := strings.Join(doc.(*jsjson.Object).Keys(), ",")
	if keys != "generated_at,stale,channels,usage,postures,roles,reasons,paused,openai_credits,openai_credits_policy" || !strings.HasPrefix(stdout, "{\n  \"generated_at\"") {
		t.Errorf("stdout %s", stdout)
	}
	code, stdout, _ = run(t, Usage, nil, "")
	if code != 0 || !strings.HasPrefix(stdout, "Claude usage: unavailable (a).\nCodex usage: unavailable (b).\nRouting now:\n") {
		t.Errorf("text: code %d, stdout %q", code, stdout)
	}
}

func TestRouting(t *testing.T) {
	usageFile := usageEnv(t)
	if code, _, stderr := run(t, Routing, []string{"--cached"}, ""); code != 3 || stderr == "" {
		t.Errorf("--cached without a cache: code %d, stderr %q", code, stderr)
	}
	writeUsageCache(t, usageFile, time.Now().Add(-time.Hour))
	code, stdout, stderr := run(t, Routing, []string{"--cached", "--json"}, "")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	doc, err := jsjson.Unmarshal([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if keys := strings.Join(doc.(*jsjson.Object).Keys(), ","); keys != "usage,registry,channels,creditsPolicy,resolved,prompt" {
		t.Errorf("keys %s", keys)
	}
	if stale := jsjson.Path(doc, "usage", "stale"); stale != true {
		t.Errorf("usage.stale = %v", stale)
	}
	if code, stdout, _ := run(t, Routing, []string{"--cached"}, ""); code != 0 || !strings.HasPrefix(stdout, "Claude usage: unavailable (a).") {
		t.Errorf("text: code %d, stdout %q", code, stdout)
	}
	for _, args := range [][]string{{"--refresh", "--cached"}, {"--bogus"}, {"extra"}} {
		if code, _, stderr := run(t, Routing, args, ""); code != 2 || !strings.HasPrefix(stderr, "usage:") {
			t.Errorf("%v: code %d, stderr %q", args, code, stderr)
		}
	}
}

func TestBudgetGate(t *testing.T) {
	writeUsageCache(t, usageEnv(t), time.Now())
	code, stdout, stderr := run(t, Budget, []string{"gate", "--vendor", "openai", "--force"}, "")
	if code != 0 || stdout != `{"allowed":true,"note":"openai usage unknown; proceeding"}`+"\n" {
		t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, args := range [][]string{nil, {"gate"}, {"gate", "--vendor", "google"}, {"open", "--vendor", "openai"}, {"gate", "--vendor", "openai", "--soon"}} {
		if code, _, stderr := run(t, Budget, args, ""); code != 2 || !strings.HasPrefix(stderr, "usage:") {
			t.Errorf("%v: code %d, stderr %q", args, code, stderr)
		}
	}
}

func TestEffort(t *testing.T) {
	usageEnv(t)
	cases := []struct {
		args     []string
		wantCode int
		wantOut  string
	}{
		{[]string{"--role", "planner", "--size", "hard"}, 0, "max\n"},
		{[]string{"--role", "planner"}, 0, "high\n"},
		{[]string{"--role", "planner", "--size", "enormous"}, 0, "high\n"},
		{[]string{"--role", "conductor"}, 0, "null\n"},
		{nil, 2, ""},
		{[]string{"--role", "planner", "--size"}, 2, ""},
	}
	for _, c := range cases {
		code, stdout, stderr := run(t, Effort, c.args, "")
		if code != c.wantCode || stdout != c.wantOut {
			t.Errorf("%v: code %d, stdout %q, stderr %q", c.args, code, stdout, stderr)
		}
	}
}
