package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessionProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".orchestrator"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func sessionRecord(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".orchestrator", "sessions", "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSessionBeatRefusesABadCommandLineWithExit2(t *testing.T) {
	root := sessionProject(t)
	cases := []struct {
		args   []string
		stderr string
	}{
		{[]string{"beat", "--cwd", root}, "session beat: --door opencode|claude required\n"},
		{[]string{"beat", "--door", "claude", "--progress", "1/0", "--cwd", root}, "session beat: --progress must be N/M\n"},
		{[]string{"beat", "--door", "claude", "--progress", "", "--cwd", root}, "session beat: --progress must be N/M\n"},
		{[]string{"beat", "--door", "claude", "--state", "busy", "--cwd", root}, "session beat: --state must be working|waiting|done|ended\n"},
		{[]string{"beat", "--door", "claude", "--now", "--cwd", root}, "session beat: --now needs a value\n"},
		{[]string{"beat", "--door", "claude", "extra"}, "session beat: unknown argument extra\n"},
		{[]string{"frob"}, "commands: beat | show\n"},
	}
	for _, c := range cases {
		if code, _, stderr := run(t, Session, c.args, ""); code != 2 || stderr != c.stderr {
			t.Errorf("%q: exit %d, stderr %q; want 2 and %q", c.args, code, stderr, c.stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".orchestrator", "sessions")); !os.IsNotExist(err) {
		t.Errorf("a refused beat wrote state (stat err %v)", err)
	}
}

func TestSessionHookBeatRecordsTheEventStateAndNeverThePrompt(t *testing.T) {
	root := sessionProject(t)
	input := `{"session_id":"s1","cwd":"` + root + `","hook_event_name":"Stop","prompt":"secret"}`
	code, stdout, stderr := run(t, Session, []string{"beat", "--door", "claude", "--hook"}, input)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	record := sessionRecord(t, root)
	if !strings.Contains(record, `"state": "waiting"`) || !strings.Contains(record, `"session_id": "s1"`) || strings.Contains(record, "secret") {
		t.Errorf("record:\n%s", record)
	}
}

func TestSessionHookBeatNeverExits2(t *testing.T) {
	outside := t.TempDir()
	stop := `{"cwd":"` + outside + `","hook_event_name":"Stop"}`
	cases := []struct {
		args          []string
		input, stderr string
	}{
		{nil, stop, "session beat: " + outside + " has no .orchestrator/ directory\n"},
		{nil, "nope", "session beat: hook input is not valid JSON\n"},
		{nil, `{"hook_event_name":"Stop"}`, "session beat: hook input has no cwd\n"},
		{nil, `{"cwd":"` + outside + `\u0000x","hook_event_name":"Stop"}`, "session beat: hook input cwd contains a NUL character\n"},
		{[]string{"--door", "codex"}, stop, "session beat: --door opencode|claude required\n"},
		{[]string{"--state", "done"}, stop, "session beat: --state cannot be combined with --hook\n"},
	}
	for _, c := range cases {
		args := append([]string{"beat", "--door", "claude", "--hook"}, c.args...)
		if code, _, stderr := run(t, Session, args, c.input); code != 1 || stderr != c.stderr {
			t.Errorf("%v %q: exit %d, stderr %q; want 1 and %q", c.args, c.input, code, stderr, c.stderr)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("hook mode created %d entries outside a project", len(entries))
	}
}

func TestSessionHookBeatIgnoresAnEventItDoesNotFollow(t *testing.T) {
	root := sessionProject(t)
	input := `{"cwd":"` + root + `","hook_event_name":"PreToolUse"}`
	if code, _, stderr := run(t, Session, []string{"beat", "--door", "claude", "--hook"}, input); code != 0 || stderr != "" {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".orchestrator", "sessions")); !os.IsNotExist(err) {
		t.Errorf("an unfollowed event wrote state (stat err %v)", err)
	}
}

func TestSessionShow(t *testing.T) {
	root := sessionProject(t)
	if code, stdout, _ := run(t, Session, []string{"show", "--cwd", root}, ""); code != 0 || stdout != "{}\n" {
		t.Errorf("empty show: exit %d, stdout %q", code, stdout)
	}
	if code, _, stderr := run(t, Session, []string{"beat", "--door", "claude", "--cwd", root}, ""); code != 0 {
		t.Fatal(stderr)
	}
	code, stdout, _ := run(t, Session, []string{"show", "--cwd", root}, "")
	if code != 0 || !strings.HasPrefix(stdout, "{\n  \"claude\": {\n    \"door\": \"claude\",") {
		t.Errorf("show: exit %d, stdout %q", code, stdout)
	}
	outside := t.TempDir()
	if code, _, stderr := run(t, Session, []string{"show", "--cwd", outside}, ""); code != 2 || !strings.HasPrefix(stderr, "not an orchestrator project: ") {
		t.Errorf("outside a project: exit %d, stderr %q", code, stderr)
	}
}
