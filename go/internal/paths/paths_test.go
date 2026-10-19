package paths

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestResolveDefaultsMatchTheInstalledLayout(t *testing.T) {
	got, err := Resolve(envOf(map[string]string{"HOME": "/home/u"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		Home:         "/home/u",
		OrchHome:     "/home/u/.config/opencode/orchestrator",
		RegistryFile: "/home/u/.config/opencode/orchestrator/models.json",
		StateDir:     "/home/u/.local/state/opencode/orchestrator",
		UsageFile:    "/home/u/.local/state/opencode/orchestrator/usage.json",
	}
	if got != want {
		t.Errorf("Resolve = %+v\nwant %+v", got, want)
	}
}

func TestResolveHonoursOverrides(t *testing.T) {
	cwd, _ := os.Getwd()
	got, err := Resolve(envOf(map[string]string{"HOME": "/home/u", "ORCH_HOME": "checkout/orchestrator"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, "checkout/orchestrator"); got.OrchHome != want {
		t.Errorf("OrchHome = %s, want %s", got.OrchHome, want)
	}
	if want := filepath.Join(cwd, "checkout/orchestrator/models.json"); got.RegistryFile != want {
		t.Errorf("RegistryFile = %s, want %s", got.RegistryFile, want)
	}

	got, err = Resolve(envOf(map[string]string{"HOME": "/home/u", "ORCH_REGISTRY": "/etc/models.json"}))
	if err != nil || got.RegistryFile != "/etc/models.json" {
		t.Errorf("RegistryFile = %s, %v; want /etc/models.json", got.RegistryFile, err)
	}
}

func TestResolveFallsBackToCurrentUserWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(os.Getenv)
	if err != nil || got.Home != current.HomeDir {
		t.Errorf("Resolve = %+v, %v; want home %q", got, err, current.HomeDir)
	}
}

// TestNodeHomeKeepsAnEmptyHome pins os.homedir(): an explicitly empty HOME is the home (so
// the incident log is relative, never the user's own), and only an unset one falls back.
func TestNodeHomeKeepsAnEmptyHome(t *testing.T) {
	lookup := func(vars map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) { v, ok := vars[key]; return v, ok }
	}
	if home, err := NodeHome(lookup(map[string]string{"HOME": ""})); err != nil || home != "" {
		t.Errorf("empty HOME: %q, %v", home, err)
	}
	if home, err := NodeHome(lookup(map[string]string{"HOME": "/h"})); err != nil || home != "/h" {
		t.Errorf("HOME=/h: %q, %v", home, err)
	}
	current, err := user.Current()
	if err != nil {
		t.Skip("no user database entry:", err)
	}
	if home, err := NodeHome(lookup(nil)); err != nil || home != current.HomeDir {
		t.Errorf("unset HOME: %q, %v; want %q", home, err, current.HomeDir)
	}
	if got := IncidentsFileIn(""); got != ".local/state/opencode/orchestrator/incidents.jsonl" {
		t.Errorf("IncidentsFileIn(\"\") = %q", got)
	}
	if got := IncidentsFileIn("/home/u/"); got != "/home/u/.local/state/opencode/orchestrator/incidents.jsonl" {
		t.Errorf("IncidentsFileIn = %q", got)
	}
}

func TestProjectPaths(t *testing.T) {
	if got := RunsDir("/p"); got != "/p/.orchestrator/runs" {
		t.Errorf("RunsDir = %s", got)
	}
	if got := MailboxDir("/p"); got != "/p/.orchestrator/mailbox" {
		t.Errorf("MailboxDir = %s", got)
	}
}
