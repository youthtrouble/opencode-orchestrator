package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Every case passes a temp --cwd: no test may reach a real project's supervisor files.
func TestSupervisorDispatch(t *testing.T) {
	for _, tc := range []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string
	}{
		{"no command is the usage line", nil, 2, "", "commands: start | serve | status | stop | ack --event ID\n"},
		{"an unknown command prints nothing", []string{"foo"}, 0, "", ""},
		{"serve waits for S8c", []string{"serve"}, 2, "", "orch supervisor serve: not implemented yet (S8c)\n"},
		{"start waits for S8c", []string{"start"}, 2, "", "orch supervisor start: not implemented yet (S8c)\n"},
		{"status of a fresh project", []string{"status"}, 0, "{\n  \"status\": \"not_started\"\n}\n", ""},
		{"ack without an event", []string{"ack"}, 1, "", "unknown event id\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			code, stdout, stderr := run(t, Supervisor, append(tc.args, "--cwd", root), "")
			if code != tc.code || stdout != tc.stdout || stderr != tc.stderr {
				t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Errorf("wrote %v", entries)
			}
		})
	}
}

func TestSupervisorStopAndAckCrashLines(t *testing.T) {
	root := t.TempDir()
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
