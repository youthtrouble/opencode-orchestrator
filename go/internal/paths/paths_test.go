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
		Home:          "/home/u",
		OrchHome:      "/home/u/.config/opencode/orchestrator",
		RegistryFile:  "/home/u/.config/opencode/orchestrator/models.json",
		StateDir:      "/home/u/.local/state/opencode/orchestrator",
		UsageFile:     "/home/u/.local/state/opencode/orchestrator/usage.json",
		IncidentsFile: "/home/u/.local/state/opencode/orchestrator/incidents.jsonl",
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

func TestProjectPaths(t *testing.T) {
	if got := RunsDir("/p"); got != "/p/.orchestrator/runs" {
		t.Errorf("RunsDir = %s", got)
	}
	if got := MailboxDir("/p"); got != "/p/.orchestrator/mailbox" {
		t.Errorf("MailboxDir = %s", got)
	}
}
