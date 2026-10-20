package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// emptyProject is a temp project root whose .orchestrator/ directory exists and is empty.
func emptyProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".orchestrator"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// Every case passes a temp --cwd: no test may reach a real project's supervisor files.
func TestSupervisorDispatch(t *testing.T) {
	notAProject := "not an orchestrator project: %s has no .orchestrator/ directory; cd into the project or pass --cwd <project dir>\n"
	for _, tc := range []struct {
		name           string
		args           []string
		project        bool
		code           int
		stdout, stderr string
	}{
		{"no command is the usage line", nil, false, 2, "", "commands: start | serve | status | stop | ack --event ID\n"},
		{"an unknown command prints nothing", []string{"foo"}, false, 0, "", ""},
		{"status of a fresh project", []string{"status"}, true, 0, "{\n  \"status\": \"not_started\"\n}\n", ""},
		{"ack without an event", []string{"ack"}, true, 1, "", "unknown event id\n"},
		{"status outside a project", []string{"status"}, false, 2, "", notAProject},
		{"stop outside a project", []string{"stop"}, false, 2, "", notAProject},
		{"ack outside a project", []string{"ack", "--event", "e1"}, false, 2, "", notAProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, written := t.TempDir(), ""
			if tc.project {
				root = emptyProject(t)
				written = ".orchestrator"
			}
			code, stdout, stderr := run(t, Supervisor, append(tc.args, "--cwd", root), "")
			if want := strings.ReplaceAll(tc.stderr, "%s", root); code != tc.code || stdout != tc.stdout || stderr != want {
				t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if entries, _ := os.ReadDir(filepath.Join(root, written)); len(entries) != 0 {
				t.Errorf("wrote %v", entries)
			}
		})
	}
}

func TestSupervisorStopAndAckCrashLines(t *testing.T) {
	root := emptyProject(t)
	if code, stdout, _ := run(t, Supervisor, []string{"stop", "--cwd", root}, ""); code != 0 || stdout != "stop requested; workers are unaffected\n" {
		t.Fatalf("stop: code %d, stdout %q", code, stdout)
	}
	state := filepath.Join(root, ".orchestrator", "supervisor-state.json")
	if err := os.WriteFile(state, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run(t, Supervisor, []string{"ack", "--event", "e1", "--cwd", root}, "")
	if code != 1 || stderr != "TypeError: Cannot read properties of null (reading 'events')\n" {
		t.Errorf("ack over a null state: code %d, stderr %q", code, stderr)
	}
}

// A lock held by a live process (this test's) makes serve refuse and start report it; neither
// launches or ticks anything.
func TestSupervisorServeAndStartOverALiveLock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".orchestrator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	lock := filepath.Join(dir, "supervisor.lock")
	if err := os.WriteFile(lock, []byte(pid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, Supervisor, []string{"serve", "--cwd", root}, "")
	if code != 1 || stdout != "" || stderr != "a supervisor already owns this project\n" {
		t.Errorf("serve: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, _ = run(t, Supervisor, []string{"start", "--cwd", root}, "")
	if want := `{"started":false,"reason":"already running","pid":` + pid + "}\n"; code != 0 || stdout != want {
		t.Errorf("start: code %d, stdout %q, want %q", code, stdout, want)
	}
	if data, _ := os.ReadFile(lock); string(data) != pid+"\n" {
		t.Errorf("lock rewritten: %q", data)
	}
}

// A stop file present at serve's first loop stops it before any tick: the state records the
// stop and the lock is released.
func TestSupervisorServeStopsBeforeTheFirstTick(t *testing.T) {
	root := emptyProject(t)
	if code, _, _ := run(t, Supervisor, []string{"stop", "--cwd", root}, ""); code != 0 {
		t.Fatal("stop failed")
	}
	if code, stdout, stderr := run(t, Supervisor, []string{"serve", "--cwd", root}, ""); code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("serve: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, ".orchestrator", "supervisor-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err := json.Unmarshal(data, &state); err != nil || state["status"] != "stopped" || !strings.HasSuffix(state["stopped_at"], "Z") || len(state) != 2 {
		t.Errorf("state %s (%v)", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".orchestrator", "supervisor.lock")); !os.IsNotExist(err) {
		t.Errorf("lock left: %v", err)
	}
}
