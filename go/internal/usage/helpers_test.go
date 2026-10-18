package usage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const testdataDir = "../../testdata/usage"

// checkoutRegistry is the real registry of this checkout.
const checkoutRegistry = "../../../orchestrator/models.json"

// answeredTimeout is the app-server deadline of every test that is not about the deadline
// itself: far beyond any process start, so it never expires there, however loaded the
// machine. A session without an answer is ended by readiness instead (see endWhenReady).
const answeredTimeout = time.Minute

// fixedNow is the clock of every test that does not need the wall clock.
var fixedNow = time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)

// sandbox isolates a test from the machine: a temp HOME and a PATH of fakes plus /bin (for
// the shell utilities the fakes use). A failing `security` fake is always present so no test
// can read the real keychain; a `codex` fake fails unless the test installs another.
type sandbox struct {
	home string
	bin  string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	s := &sandbox{home: t.TempDir(), bin: t.TempDir()}
	t.Setenv("PATH", s.bin+":/bin")
	s.fake(t, "security", "exit 44")
	s.fake(t, "codex", "echo 'codex fake: not scripted' >&2; exit 97")
	return s
}

// fake installs an executable sh script named name on the sandbox PATH.
func (s *sandbox) fake(t *testing.T, name, body string) {
	t.Helper()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(s.bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// linkReal puts the machine's tool on the sandbox PATH, skipping the test when it is missing.
func (s *sandbox) linkReal(t *testing.T, name string) {
	t.Helper()
	real, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	if err := os.Symlink(real, filepath.Join(s.bin, name)); err != nil {
		t.Fatal(err)
	}
}

// writeFile writes content to a path relative to HOME, creating directories.
func (s *sandbox) writeFile(t *testing.T, rel, content string) string {
	t.Helper()
	file := filepath.Join(s.home, rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// config is a Config rooted in the sandbox whose Claude endpoint is url (an unreachable one
// by default, so a test that finds a token can never call the real endpoint).
func (s *sandbox) config(url string) Config {
	if url == "" {
		url = "http://127.0.0.1:1/unreachable"
	}
	return Config{
		Home:           s.home,
		UsageFile:      filepath.Join(s.home, ".local", "state", "opencode", "orchestrator", "usage.json"),
		RegistryFile:   checkoutRegistry,
		ClaudeUsageURL: url,
		ClaudeTimeout:  2 * time.Second,
		CodexBin:       "codex",
		CodexTimeout:   answeredTimeout,
		Now:            func() time.Time { return fixedNow },
	}
}

func readFixture(t *testing.T, name string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func indented(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.MarshalIndent(v, "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func compact(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
