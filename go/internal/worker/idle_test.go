package worker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

func TestIdleMinutesWorkerValidationAndReplay(t *testing.T) {
	p := testProject(t, "brief")
	for _, harness := range []string{"claude", "codex", "opencode"} {
		for _, value := range []string{"0", "1.5", "-1", "1e3", "", "10081"} {
			args := opts("--brief", "b.md", "--harness", harness, "--idle-min", value)
			launch, err := p.PrepareStart(args)
			valid := harness == "opencode" && (value == "0" || value == "1.5")
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"opencode-exec", "--cwd", p.Root, "--timeout-min", "120", "--idle-min", value}
				if got := launch.bridgeArgs(); !slices.Equal(got, want) {
					t.Errorf("argv %q, want %q", got, want)
				}
				if got := jsjson.Path(launch.launchOptions(), "idle-min"); got != value {
					t.Errorf("replay value %v", got)
				}
			} else if _, ok := err.(*UsageError); !ok {
				t.Errorf("%s %q: expected usage error, got %v", harness, value, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(p.Root, ".orchestrator")); !os.IsNotExist(err) {
		t.Errorf("validation wrote artifacts: %v", err)
	}
	t.Setenv("ORCH_IDLE_MIN", "invalid")
	if _, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "opencode")); err == nil {
		t.Error("invalid env accepted")
	}
	if _, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "opencode", "--idle-min", "0")); err != nil {
		t.Errorf("flag did not override env: %v", err)
	}
	if _, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "opencode", "--idle-min")); err == nil {
		t.Error("bare idle flag accepted")
	}
}
