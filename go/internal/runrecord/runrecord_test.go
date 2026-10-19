package runrecord

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestWriteOverwritesWithMode0644UnderUmask022(t *testing.T) {
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
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestWriteMatchesNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable; fixed expectations still tested")
	}
	_, here, _, _ := runtime.Caller(0)
	lib := filepath.Join(filepath.Dir(here), "..", "..", "..", "orchestrator", "lib", "run-record.mjs")
	full := `{"started_at":"2026-10-18T10:11:12.345Z","harness":"claude","session_id":"ses_0123456789","prompt":"é\n😀","cost":{"usd":0.5,"tokens":[1,2]},"thread_id":null,"10":"index key"}`
	cases := []struct{ projectRoot, record string }{
		{"proj", full},
		{"proj/../proj/", full},
		{"proj", `{"started_at":"s","harness":"h","session_id":[]}`},
		{"proj", `{"started_at":"s","harness":"h","session_id":["s"]}`},
		{"proj", `{"started_at":"s","harness":"h","session_id":["a","b"]}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":[],"session_id":"fallback"}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":0,"session_id":["0123456789abcdef",1,2,3,4,5,6,7,8,9]}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":[["x","y"],null,{"k":1},1.5,true,"😀😀😀😀😀"]}`},
	}
	for _, c := range cases {
		jsDir, goDir := t.TempDir(), t.TempDir()
		script := `const { writeRunRecord } = await import(process.argv[1]); console.log(writeRunRecord(process.argv[2], JSON.parse(process.argv[3])))`
		cmd := exec.Command("node", "--input-type=module", "-e", script, lib, c.projectRoot, c.record)
		cmd.Dir = jsDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node %s: %v", c.record, err)
		}
		jsFile := strings.TrimSuffix(string(out), "\n")

		t.Chdir(goDir)
		goFile, err := Write(c.projectRoot, mustObject(t, c.record), testNow)
		if err != nil {
			t.Fatalf("Write(%s): %v", c.record, err)
		}
		if goFile != jsFile {
			t.Errorf("%q %s: path %q, Node %q", c.projectRoot, c.record, goFile, jsFile)
		}
		jsData, err := os.ReadFile(filepath.Join(jsDir, jsFile))
		if err != nil {
			t.Fatal(err)
		}
		goData, err := os.ReadFile(filepath.Join(goDir, goFile))
		if err != nil {
			t.Fatal(err)
		}
		if string(goData) != string(jsData) {
			t.Errorf("%q %s: content\n%s\nNode\n%s", c.projectRoot, c.record, goData, jsData)
		}
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
