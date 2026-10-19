package manifest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

var testNow = time.Date(2026, 10, 19, 1, 2, 3, 4_000_000, time.UTC)

func TestBuildWalksLikeTheJS(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"src/a.go", "src/dist/skipped", "src/node_modules/skipped", "dist", "1", "42", "10", "__proto__", "", "😀", "z", "node_modules/named.js"} {
		writeFile(t, filepath.Join(root, name), name)
	}
	symlink(t, "src/a.go", filepath.Join(root, "src/link-to-file"))
	symlink(t, ".", filepath.Join(root, "src/link-to-dir"))
	symlink(t, "loop", filepath.Join(root, "src/loop"))
	if err := syscall.Mkfifo(filepath.Join(root, "src/fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, "z", filepath.Join(root, "explicit-link"))

	m, err := Build(Request{Root: root, Paths: []string{".", "src", "explicit-link", "node_modules"}, Task: "t", Author: "a", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	files := jsjson.Path(m, "files").(*jsjson.Object)
	// Index-like names first, then UTF-16 order (U+1F600 before U+E000); __proto__ and the
	// ignored, symlinked and FIFO entries below a walked directory are absent.
	want := []string{"1", "10", "42", "dist", "explicit-link", "node_modules/named.js", "src/a.go", "z", "😀", ""}
	if got := files.Keys(); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %q, want %q", got, want)
	}
	if got := jsjson.Path(m, "file_count"); got != float64(len(want)) {
		t.Errorf("file_count = %v", got)
	}
	if got := jsjson.Path(m, "baseline"); got != nil {
		t.Errorf("baseline = %v, want null", got)
	}
	if got := jsjson.Path(m, "created_at"); got != "2026-10-19T01:02:03.004Z" {
		t.Errorf("created_at = %v", got)
	}
	filesJSON, err := jsjson.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsjson.Path(m, "manifest_sha256"); got != sha256Hex(filesJSON) {
		t.Errorf("manifest_sha256 = %v, want sha256 of %s", got, filesJSON)
	}
	if got := jsjson.Path(files, "explicit-link"); got != sha256Hex([]byte("z")) {
		t.Errorf("explicit symlink hash = %v, want the hash of its target", got)
	}
}

func TestBuildPathsOutsideAndEqualToRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x"), "outside")
	writeFile(t, filepath.Join(dir, "sub/f"), "file root")
	root := filepath.Join(dir, "sub")

	m, err := Build(Request{Root: root, Paths: []string{"../x", "../x", filepath.Join(dir, "x")}, Task: "t", Author: "a", Baseline: "b.json", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsjson.Path(m, "files").(*jsjson.Object).Keys(); !reflect.DeepEqual(got, []string{"../x"}) {
		t.Errorf("files = %q, want the deduplicated ../x", got)
	}
	if got := jsjson.Path(m, "baseline"); got != "b.json" {
		t.Errorf("baseline = %v", got)
	}

	// A root that is itself a file names that file with path.relative's "".
	fileRoot := filepath.Join(root, "f")
	m, err = Build(Request{Root: fileRoot, Paths: []string{""}, Task: "t", Author: "a", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsjson.Path(m, "files", ""); got != sha256Hex([]byte("file root")) {
		t.Errorf(`files[""] = %v`, got)
	}
}

func TestBuildMissingPaths(t *testing.T) {
	root := t.TempDir()
	symlink(t, "nowhere", filepath.Join(root, "broken"))
	symlink(t, "loop", filepath.Join(root, "loop"))
	for _, p := range []string{"absent", "broken", "loop"} {
		_, err := Build(Request{Root: root, Paths: []string{p}, Now: testNow})
		if !errors.Is(err, ErrPathMissing) || err.Error() != "manifest path does not exist: "+p {
			t.Errorf("Build(%q) error = %v", p, err)
		}
	}
}

func TestVerify(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "same"), "same")
	writeFile(t, filepath.Join(root, "edited"), "new")
	writeFile(t, filepath.Join(root, "file"), "f")
	files := mustParse(t, `{"files":{"same":"`+sha256Hex([]byte("same"))+`","edited":"`+sha256Hex([]byte("old"))+`","gone":"x","file/":"x","n":1,"same/../same":"`+sha256Hex([]byte("same"))+`"}}`)
	v, err := Verify(root, files)
	if err != nil {
		t.Fatal(err)
	}
	want := Verification{Mismatched: []string{"edited"}, Missing: []string{"gone", "file/", "n"}, Checked: 6}
	if !reflect.DeepEqual(v, want) || v.OK() {
		t.Errorf("Verify = %+v, want %+v (not ok)", v, want)
	}
	data, err := jsjson.Marshal(v.Object())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"ok":false,"mismatched":["edited"],"missing":["gone","file/","n"],"checked":6}` {
		t.Errorf("JSON = %s", data)
	}
}

func TestVerifyOddShapes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "0"), "a")
	cases := []struct{ manifest, want, err string }{
		{`{"files":{}}`, `{"ok":true,"mismatched":[],"missing":[],"checked":0}`, ""},
		{`{"files":"ab"}`, `{"ok":false,"mismatched":["0"],"missing":["1"],"checked":2}`, ""},
		{`{"files":["` + sha256Hex([]byte("a")) + `"]}`, `{"ok":true,"mismatched":[],"missing":[],"checked":1}`, ""},
		{`{"files":7}`, `{"ok":true,"mismatched":[],"missing":[],"checked":0}`, ""},
		{`{}`, "", "Cannot convert undefined or null to object"},
		{`{"files":null}`, "", "Cannot convert undefined or null to object"},
		{`[]`, "", "Cannot convert undefined or null to object"},
		{`null`, "", "Cannot read properties of null (reading 'files')"},
	}
	for _, c := range cases {
		v, err := Verify(root, mustParse(t, c.manifest))
		if c.err != "" {
			var shape *ShapeError
			if !errors.As(err, &shape) || err.Error() != c.err {
				t.Errorf("Verify(%s) error = %v, want TypeError %q", c.manifest, err, c.err)
			}
			continue
		}
		data, _ := jsjson.Marshal(v.Object())
		if err != nil || string(data) != c.want {
			t.Errorf("Verify(%s) = %s, %v; want %s", c.manifest, data, err, c.want)
		}
	}
}

func TestVerifyDirectoryEntryFailsLikeNode(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(root, mustParse(t, `{"files":{"d":"x"}}`))
	if err == nil || err.Error() != "EISDIR: illegal operation on a directory, read" {
		t.Errorf("error = %v", err)
	}
}

func TestDiff(t *testing.T) {
	cases := []struct{ before, after, want, err string }{
		{`{"files":{"same":"1","changed":"1","removed":"1"}}`, `{"files":{"same":"1","changed":"2","added":"1"}}`,
			`{"added":["added"],"removed":["removed"],"changed":["changed"],"unchanged":1}`, ""},
		// Prototype names are `in` every object, and their values equal nothing.
		{`{"files":{}}`, `{"files":{"constructor":"1","toString":"1","__proto__":"1","hasOwnProperty":"1"}}`,
			`{"added":[],"removed":[],"changed":["constructor","toString","__proto__","hasOwnProperty"],"unchanged":0}`, ""},
		{`{"files":{"constructor":"1"}}`, `{"files":{}}`, `{"added":[],"removed":[],"changed":[],"unchanged":0}`, ""},
		{`{"files":{"a":1,"b":null,"c":{},"d":[]}}`, `{"files":{"a":1,"b":null,"c":{},"d":[]}}`,
			`{"added":[],"removed":[],"changed":["c","d"],"unchanged":2}`, ""},
		{`{"files":["x","y"]}`, `{"files":{"0":"x","1":"z","2":"x","length":2,"map":"m"}}`,
			`{"added":["2"],"removed":[],"changed":["1","map"],"unchanged":2}`, ""},
		{`null`, `{"files":{}}`, "", "Cannot read properties of null (reading 'files')"},
		{`null`, `{"files":{"a":"1"}}`, "", "Cannot read properties of null (reading 'files')"},
		{`{}`, `{"files":{"a":"1"}}`, "", "Cannot use 'in' operator to search for 'a' in undefined"},
		{`{"files":"s"}`, `{"files":{"a":"1"}}`, "", "Cannot use 'in' operator to search for 'a' in s"},
		{`{"files":{"a":"1"}}`, `{"files":"s"}`, "", "Cannot use 'in' operator to search for 'a' in s"},
		{`{}`, `{"files":{}}`, "", "Cannot convert undefined or null to object"},
		{`{"files":{}}`, `{}`, "", "Cannot convert undefined or null to object"},
	}
	for _, c := range cases {
		d, err := Diff(mustParse(t, c.before), mustParse(t, c.after))
		if c.err != "" {
			var shape *ShapeError
			if !errors.As(err, &shape) || err.Error() != c.err {
				t.Errorf("Diff(%s, %s) error = %v, want TypeError %q", c.before, c.after, err, c.err)
			}
			continue
		}
		data, _ := jsjson.Marshal(d.Object())
		if err != nil || string(data) != c.want {
			t.Errorf("Diff(%s, %s) = %s, %v; want %s", c.before, c.after, data, err, c.want)
		}
	}
}

func TestPrototypeNamesMatchNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable; the pinned lists are still used")
	}
	script := `console.log(Object.getOwnPropertyNames(Object.prototype).join("\n")); console.log("--"); console.log(Object.getOwnPropertyNames(Array.prototype).join("\n"))`
	out, err := exec.Command("node", "-e", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	objectNames, arrayNames, _ := strings.Cut(strings.TrimSuffix(string(out), "\n"), "\n--\n")
	if got := strings.Split(objectNames, "\n"); !reflect.DeepEqual(got, objectPrototype) {
		t.Errorf("Object.prototype names = %q, pinned %q", got, objectPrototype)
	}
	if got := strings.Split(arrayNames, "\n"); !reflect.DeepEqual(got, arrayPrototype) {
		t.Errorf("Array.prototype names = %q, pinned %q", got, arrayPrototype)
	}
}

func mustParse(t *testing.T, text string) any {
	t.Helper()
	v, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
