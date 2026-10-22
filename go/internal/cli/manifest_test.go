package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestManifestCreateVerifyDiff(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", "a")

	code, stdout, stderr := run(t, Manifest, []string{"create", "--task", "t", "--author", "me", "--out", "out/m.json", "src"}, "")
	receipt := regexp.MustCompile(`^\{"out":"out/m\.json","file_count":1,"manifest_sha256":"[0-9a-f]{64}"\}\n$`)
	if code != 0 || !receipt.MatchString(stdout) || stderr != "" {
		t.Fatalf("create: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	code, stdout, _ = run(t, Manifest, []string{"verify", "out/m.json"}, "")
	if code != 0 || stdout != `{"ok":true,"mismatched":[],"missing":[],"checked":1}`+"\n" {
		t.Errorf("verify: code %d stdout %q", code, stdout)
	}

	writeTestFile(t, "src/a.txt", "edited")
	code, stdout, _ = run(t, Manifest, []string{"verify", "out/m.json"}, "")
	if code != 1 || stdout != `{"ok":false,"mismatched":["src/a.txt"],"missing":[],"checked":1}`+"\n" {
		t.Errorf("verify after edit: code %d stdout %q", code, stdout)
	}

	// toString is never removed: `"toString" in after.files` holds through the prototype.
	writeTestFile(t, "before.json", `{"files":{"src/a.txt":"x","toString":"y","gone":"z"}}`)
	code, stdout, _ = run(t, Manifest, []string{"diff", "before.json", "out/m.json"}, "")
	if want := "{\n  \"added\": [],\n  \"removed\": [\n    \"gone\"\n  ],\n  \"changed\": [\n    \"src/a.txt\"\n  ],\n  \"unchanged\": 0\n}\n"; code != 0 || stdout != want {
		t.Errorf("diff: code %d stdout %q, want %q", code, stdout, want)
	}
}

// A symlink at --out is replaced by a private regular file; the file it pointed at is untouched (R15).
func TestManifestCreateReplacesSymlinkAtOut(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "src/a.txt", "a")
	writeTestFile(t, "victim.txt", "keep me")
	if err := os.Symlink("victim.txt", "m.json"); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := run(t, Manifest, []string{"create", "--task", "t", "--author", "me", "--out", "m.json", "src"}, ""); code != 0 || stderr != "" {
		t.Fatalf("create: code %d stderr %q", code, stderr)
	}
	info, err := os.Lstat("m.json")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("m.json mode %v, want a regular 0600 file", info.Mode())
	}
	if victim, err := os.ReadFile("victim.txt"); err != nil || string(victim) != "keep me" {
		t.Errorf("victim: %q, %v", victim, err)
	}
	if code, stdout, _ := run(t, Manifest, []string{"verify", "m.json"}, ""); code != 0 {
		t.Errorf("verify: code %d stdout %q", code, stdout)
	}
}

func TestManifestUsageErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	const createUsage = "usage: orch manifest create --task T --author A --out file.json [--baseline b.json] [--root dir] <paths...>\n"
	for _, args := range [][]string{
		{"create", "--task", "t", "--author", "a", "--out", "o.json"},
		{"create", "--task", "", "--author", "a", "--out", "o.json", "x"},
		{"create", "--author", "a", "--out", "o.json", "x", "--task"},
	} {
		if code, stdout, stderr := run(t, Manifest, args, ""); code != 2 || stdout != "" || stderr != createUsage {
			t.Errorf("%q: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	for _, args := range [][]string{nil, {"frob"}} {
		if code, _, stderr := run(t, Manifest, args, ""); code != 2 || stderr != "commands: create | verify | diff\n" {
			t.Errorf("%q: code %d stderr %q", args, code, stderr)
		}
	}
}

// The error lines Node prints for the uncaught exceptions (C1), captured from Node v26.5.1.
func TestManifestCrashLines(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	physical, err := filepath.EvalSymlinks(dir) // process.cwd() is the physical directory
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, "nofiles.json", `{}`)
	writeTestFile(t, "bad.json", `{x`)
	if err := os.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"verify"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined`},
		{[]string{"diff", "nofiles.json"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined`},
		{[]string{"create", "--task", "t", "--author", "a", "--out", "o.json", "nope"}, "Error: manifest path does not exist: nope"},
		{[]string{"verify", "nofiles.json"}, "TypeError: Cannot convert undefined or null to object"},
		{[]string{"verify", "missing.json"}, "Error: ENOENT: no such file or directory, open '" + filepath.Join(physical, "missing.json") + "'"},
		{[]string{"verify", "d"}, "Error: EISDIR: illegal operation on a directory, read"},
	}
	for _, c := range cases {
		code, stdout, stderr := run(t, Manifest, c.args, "")
		if code != 1 || stdout != "" || stderr != c.want+"\n" {
			t.Errorf("%q: code %d stdout %q stderr %q, want %q", c.args, code, stdout, stderr, c.want)
		}
	}
	// JSON.parse failures keep the class; V8's wording is not reproduced.
	if code, _, stderr := run(t, Manifest, []string{"verify", "bad.json"}, ""); code != 1 || !regexp.MustCompile(`^SyntaxError: .+\n$`).MatchString(stderr) {
		t.Errorf("bad JSON: code %d stderr %q", code, stderr)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
