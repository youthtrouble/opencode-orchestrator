package runrecord

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

var testNow = time.Date(2026, 10, 19, 1, 2, 3, 4_000_000, time.UTC)

func TestWriteNamesTheFile(t *testing.T) {
	cases := []struct{ name, record, want string }{
		{"started_at stamped", `{"started_at":"2026-10-18T10:11:12.345Z","harness":"codex","thread_id":"0123456789abcdef"}`, "2026-10-18T10-11-12-345Z-codex-01234567.json"},
		{"now when started_at is empty", `{"started_at":"","harness":"claude","session_id":"ses"}`, "2026-10-19T01-02-03-004Z-claude-ses.json"},
		{"undefined harness, run id", `{"thread_id":"","session_id":null}`, "2026-10-19T01-02-03-004Z-undefined-run.json"},
		{"thread before session", `{"harness":"h","thread_id":"t","session_id":"s"}`, "2026-10-19T01-02-03-004Z-h-t.json"},
		{"falsy thread falls back", `{"harness":"h","thread_id":0,"session_id":"s"}`, "2026-10-19T01-02-03-004Z-h-s.json"},
		{"id sliced by UTF-16 units", `{"harness":null,"thread_id":"😀😀😀😀😀"}`, "2026-10-19T01-02-03-004Z-null-😀😀😀😀.json"},
		{"empty array id is truthy", `{"harness":"h","thread_id":[],"session_id":"s"}`, "2026-10-19T01-02-03-004Z-h-.json"},
		{"array id joined", `{"harness":"h","session_id":["a","b"]}`, "2026-10-19T01-02-03-004Z-h-a,b.json"},
		{"array id sliced by element", `{"harness":"h","thread_id":["0123456789",1,2,3,4,5,6,7,8]}`, "2026-10-19T01-02-03-004Z-h-0123456789,1,2,3,4,5,6,7.json"},
		{"array id elements coerced", `{"harness":"h","thread_id":[["x","y"],null,{"k":1},1.5,true]}`, "2026-10-19T01-02-03-004Z-h-x,y,,[object Object],1.5,true.json"},
		{"slashes in a string id stay in runs/", `{"harness":"h","thread_id":"/../../x"}`, "2026-10-19T01-02-03-004Z-h-_.._.._x.json"},
		{"slashes in an array id stay in runs/", `{"harness":"h","thread_id":["a/../../b"]}`, "2026-10-19T01-02-03-004Z-h-a_.._.._b.json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			file, err := Write(root, mustObject(t, c.record), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(root, ".orchestrator", "runs", c.want); file != want {
				t.Errorf("path = %s, want %s", file, want)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := jsjson.MarshalIndent(mustObject(t, c.record), "  ")
			if string(data) != string(want)+"\n" {
				t.Errorf("content = %q, want %q", data, string(want)+"\n")
			}
		})
	}
}

func TestWriteRejectsFieldsWithoutTheStringMethod(t *testing.T) {
	records := []string{`{"started_at":5}`, `{"started_at":["s"]}`, `{"thread_id":true,"session_id":"s"}`, `{"session_id":5}`, `{"session_id":{"slice":1}}`}
	for _, record := range records {
		if _, err := Write(t.TempDir(), mustObject(t, record), testNow); !errors.Is(err, ErrNoStringMethod) {
			t.Errorf("Write(%s) error = %v, want ErrNoStringMethod", record, err)
		}
	}
}

// Records hold prompts and answers: owner-only under a permissive umask (PE-21).
func TestWriteOverwritesOwnerOnlyUnderUmask022(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	root := t.TempDir()
	record := `{"started_at":"s","harness":"h","thread_id":"t","n":1}`
	if _, err := Write(root, mustObject(t, `{"started_at":"s","harness":"h","thread_id":"t","long":"`+strings.Repeat("x", 100)+`"}`), testNow); err != nil {
		t.Fatal(err)
	}
	file, err := Write(root, mustObject(t, record), testNow)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"started_at\": \"s\",\n  \"harness\": \"h\",\n  \"thread_id\": \"t\",\n  \"n\": 1\n}\n"; string(data) != want {
		t.Errorf("content = %q, want the second record only", data)
	}
	wantPerm(t, file, 0o600)
	wantPerm(t, filepath.Dir(file), 0o700)
}

func TestWriteTightensAnExistingRunsDirectory(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	root := t.TempDir()
	runs := filepath.Join(root, ".orchestrator", "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, mustObject(t, `{"harness":"h"}`), testNow); err != nil {
		t.Fatal(err)
	}
	wantPerm(t, runs, 0o700)
}

func TestWriteReplacesAPlantedSymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, ".orchestrator", "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(runs, "s-h-t.json")
	if err := os.Symlink(victim, file); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, mustObject(t, `{"started_at":"s","harness":"h","thread_id":"t"}`), testNow); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() {
		t.Errorf("record = %v, %v; want a regular file", info, err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "keep" {
		t.Errorf("symlink target = %q, want it untouched", data)
	}
}

func wantPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode %v, want %v", path, got, want)
	}
}

func mustObject(t *testing.T, text string) *jsjson.Object {
	t.Helper()
	v, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*jsjson.Object)
}
