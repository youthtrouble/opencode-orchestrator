package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var stamp = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func readRecord(t *testing.T, root, door string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(root), door+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseProgress(t *testing.T) {
	for text, want := range map[string]Progress{"0/1": {0, 1}, "3/2": {3, 2}, "007/10": {7, 10}} {
		if got, ok := ParseProgress(text); !ok || got != want {
			t.Errorf("ParseProgress(%q) = %v, %v; want %v", text, got, ok, want)
		}
	}
	for _, text := range []string{"1/0", "1", "1/2/3", "-1/2", "1/2\n", "a/b", "", "١/٢"} {
		if _, ok := ParseProgress(text); ok {
			t.Errorf("ParseProgress(%q) accepted", text)
		}
	}
}

func TestWriteMergesOverThePreviousRecord(t *testing.T) {
	root := t.TempDir()
	first := Beat{State: "done", SessionID: To("s1"), Now: To("reviewing"), Slice: To("wt3"), Progress: To(Progress{1, 3})}
	if _, err := Write(root, "claude", first, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "claude", Beat{State: "working", Now: Clear[string]()}, stamp.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	want := `{
  "door": "claude",
  "session_id": "s1",
  "state": "working",
  "now": null,
  "slice": "wt3",
  "progress": {
    "done": 1,
    "total": 3
  },
  "updated_at": "2026-10-20T12:00:00.001Z"
}
`
	if got := readRecord(t, root, "claude"); got != want {
		t.Errorf("record:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteIsOwnerOnlyAndLeavesNoTemp(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, "opencode", Beat{State: "working"}, stamp); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filepath.Join(Dir(root), "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("modes: dir %v, file %v; want 0700 and 0600", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	entries, _ := os.ReadDir(Dir(root))
	if len(entries) != 1 {
		t.Errorf("sessions dir holds %d entries, want only opencode.json", len(entries))
	}
}

func TestWriteTightensAnExistingSessionsDirToOwnerOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "claude", Beat{State: "working"}, stamp); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("sessions dir mode %v, want 0700", info.Mode().Perm())
	}
}

func TestWriteCutsNowAndSliceInUTF16Units(t *testing.T) {
	root := t.TempDir()
	now := strings.Repeat("a", 199) + "😀tail"
	if _, err := Write(root, "claude", Beat{State: "working", Now: To(now), Slice: To(strings.Repeat("s", 50))}, stamp); err != nil {
		t.Fatal(err)
	}
	record := readRecord(t, root, "claude")
	if !strings.Contains(record, `"now": "`+strings.Repeat("a", 199)+"\ufffd\"") || !strings.Contains(record, `"slice": "`+strings.Repeat("s", 40)+`"`) {
		t.Errorf("record not cut as expected:\n%s", record)
	}
}

func TestWriteReplacesAPreviousRecordThatIsNotAnObject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(root), "claude.json"), []byte(`{"now":"kept?"`), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced, err := Write(root, "claude", Beat{State: "waiting"}, stamp)
	if err != nil || !replaced {
		t.Fatalf("Write = %v, %v; want replaced", replaced, err)
	}
	if record := readRecord(t, root, "claude"); !strings.Contains(record, `"now": null`) {
		t.Errorf("record kept a field of an unparsable file:\n%s", record)
	}
}

func TestWriteKeepsOnlyWellShapedPreviousFields(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := `{"session_id":5,"now":"n","slice":null,"progress":{"done":"1","total":2},"extra":true}`
	if err := os.WriteFile(filepath.Join(Dir(root), "claude.json"), []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "claude", Beat{State: "working"}, stamp); err != nil {
		t.Fatal(err)
	}
	record := readRecord(t, root, "claude")
	for _, want := range []string{`"session_id": null`, `"now": "n"`, `"slice": null`, `"progress": null`} {
		if !strings.Contains(record, want) {
			t.Errorf("record lacks %s:\n%s", want, record)
		}
	}
	if strings.Contains(record, "extra") {
		t.Errorf("record kept an unknown key:\n%s", record)
	}
}

func TestWriteFailsWhenThePreviousRecordCannotBeRead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(Dir(root), "claude.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Write(root, "claude", Beat{State: "working"}, stamp)
	if err == nil || err.Error() != "could not read .orchestrator/sessions/claude.json: EISDIR" {
		t.Errorf("Write error = %v", err)
	}
}

func TestParseHookInputReadsOnlyTheFieldsItUses(t *testing.T) {
	input, err := ParseHookInput(`{"session_id":"s1","cwd":"/p","hook_event_name":"Stop","prompt":"secret"}`)
	if err != nil || input.SessionID == nil || *input.SessionID != "s1" || input.Cwd != "/p" || input.Event != "Stop" {
		t.Errorf("ParseHookInput = %+v, %v", input, err)
	}
	input, err = ParseHookInput(`{"session_id":7,"cwd":null}`)
	if err != nil || input.SessionID != nil || input.Cwd != "" || input.Event != nil {
		t.Errorf("ParseHookInput of odd types = %+v, %v", input, err)
	}
	for text, want := range map[string]string{"nope": "hook input is not valid JSON", "[1]": "hook input is not a JSON object", "null": "hook input is not a JSON object"} {
		if _, err := ParseHookInput(text); err == nil || err.Error() != want {
			t.Errorf("ParseHookInput(%q) error = %v, want %q", text, err, want)
		}
	}
}

func TestHookEventState(t *testing.T) {
	for event, want := range map[string]string{"UserPromptSubmit": "working", "SessionStart": "working", "Stop": "waiting", "SessionEnd": "ended"} {
		if got, ok := HookEventState(event); !ok || got != want {
			t.Errorf("HookEventState(%q) = %q, %v; want %q", event, got, ok, want)
		}
	}
	for _, event := range []any{"PreToolUse", "constructor", 5.0, nil} {
		if _, ok := HookEventState(event); ok {
			t.Errorf("HookEventState(%v) is followed", event)
		}
	}
}

func TestReadReturnsRecordsInDoorOrderAndSkipsUnparsableOnes(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, "opencode", Beat{State: "working"}, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(root), "claude.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, skipped := Read(root)
	if keys := sessions.Keys(); len(keys) != 1 || keys[0] != "opencode" {
		t.Errorf("Read keys = %v, want [opencode]", keys)
	}
	if len(skipped) != 1 || skipped[0] != ".orchestrator/sessions/claude.json: not valid JSON" {
		t.Errorf("skipped = %q", skipped)
	}
}
