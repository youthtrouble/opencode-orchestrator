package snapshot

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// go test ./internal/remote/snapshot -update rewrites the goldens; without it the test only
// reads. remote/protocol/test/go-goldens.test.ts validates the goldens against the schema.
var update = flag.Bool("update", false, "rewrite testdata/*.golden.json from the fixture projects")

const (
	testMachineID = "00000000-0000-4000-8000-000000000001"
	testHome      = "/srv/owner-home" // the fixtures plant it in free text; it must never come out
	livePID       = 4242              // the only pid the fixtures' lanes may name as alive
)

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func testEnv() Env {
	return Env{
		MachineID: testMachineID,
		Home:      testHome,
		Now:       testNow,
		Alive:     func(pid any) bool { return pid == float64(livePID) },
		CI:        NoCI{},
	}
}

func testUsageConfig(t *testing.T, usageFile string) usage.Config {
	t.Helper()
	return usage.Config{
		UsageFile:    usageFile,
		RegistryFile: filepath.Join("testdata", "usage", "models.json"),
		Now:          func() time.Time { return testNow },
	}
}

// fixtureProject is a fixture tree as discovery would report it, with a fixed real path so the
// key does not depend on where the checkout lives.
func fixtureProject(t *testing.T, name string) discover.Project {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "projects", name))
	if err != nil {
		t.Fatal(err)
	}
	return discover.Project{Name: name, Root: root, RealPath: filepath.Join(testHome, "Developer", name)}
}

func encodeIndented(t *testing.T, value any) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestGoldenPushRequests(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "projects"))
	if err != nil {
		t.Fatal(err)
	}
	budget, err := Budget(testUsageConfig(t, filepath.Join("testdata", "usage", "usage.json")))
	if err != nil || budget == nil {
		t.Fatalf("Budget = %v, %v", budget, err)
	}
	var cases []string
	for _, entry := range entries {
		name := entry.Name()
		cases = append(cases, name)
		t.Run(name, func(t *testing.T) {
			snapshot, err := Build(fixtureProject(t, name), testEnv())
			if err != nil {
				t.Fatal(err)
			}
			got := encodeIndented(t, NewPushRequest(testMachineID, "0.0.0-test", testNow, budget, []wire.ProjectSnapshot{snapshot}))
			if err := wire.Decode(bytes.NewReader(got), new(wire.PushRequest)); err != nil {
				t.Errorf("the request does not decode as a PushRequest: %v", err)
			}
			golden := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/remote/snapshot -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs (run go test ./internal/remote/snapshot -update and review the diff)\n got:\n%s", golden, got)
			}
		})
	}
	goldens, err := filepath.Glob(filepath.Join("testdata", "*.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, golden := range goldens {
		if name := strings.TrimSuffix(filepath.Base(golden), ".golden.json"); !slices.Contains(cases, name) {
			t.Errorf("%s has no fixture project; delete it", golden)
		}
	}
}

// The goldens pin each case byte for byte; these name the facts each case exists to show, so a
// regenerated golden that breaks one fails here too.
func TestGoldenCasesShowTheirStates(t *testing.T) {
	want := map[string]struct{ state, reason, door, run string }{
		"headless-working":              {stateWorking, "the headless conductor is running", "opencode", "headless"},
		"interactive-blocking-question": {stateNeedsYou, "1 blocking question", "claude", "interactive"},
		"human-gated":                   {stateNeedsYou, "awaiting your answer: push approval", "opencode", "none"},
		"page-paused":                   {statePaused, "paused from Work Trains", "claude", "none"},
		"budget-paused":                 {statePaused, "both vendors red until a reset", "opencode", "none"},
		"failed-lane":                   {stateFailed, "the headless conductor stopped without reporting (its process is gone)", "", "none"},
		"done":                          {stateDone, "the last session finished", "claude", "none"},
		"idle":                          {stateIdle, "nothing is running", "claude", "none"},
		"legacy-question":               {stateIdle, "nothing is running", "opencode", "interactive"},
		"action-question":               {stateNeedsYou, "1 blocking question", "claude", "interactive"},
		"secrets":                       {stateNeedsYou, "1 blocking question", "claude", "interactive"},
	}
	for name, w := range want {
		snapshot, err := Build(fixtureProject(t, name), testEnv())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		door := ""
		if snapshot.Door != nil {
			door = *snapshot.Door
		}
		if snapshot.State != w.state || snapshot.StateReason != w.reason || door != w.door || snapshot.Run.Kind != w.run {
			t.Errorf("%s: state %q (%q), door %q, run %q; want %+v", name, snapshot.State, snapshot.StateReason, door, snapshot.Run.Kind, w)
		}
	}
}
