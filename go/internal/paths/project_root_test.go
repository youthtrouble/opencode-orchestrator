package paths

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProjectRoot(t *testing.T) {
	for _, c := range []struct{ input, want string }{
		{"/a/.orchestrator", "/a"},
		{"/a/.orchestrator/briefs/x", "/a"},
		{"/a/b", "/a/b"},
		{"/a/.orchestrator/.orchestrator", "/a"},
		{"/.orchestrator/briefs", "/"},
		{"/a/.orchestrator-backup/x", "/a/.orchestrator-backup/x"},
		{"/a//b/../.orchestrator/briefs/..", "/a"},
		{"/a/.orchestrator/../b", "/a/b"},
	} {
		t.Run(c.input, func(t *testing.T) {
			got, err := ProjectRoot(c.input)
			if err != nil || got != c.want {
				t.Fatalf("ProjectRoot(%q) = %q, %v; want %q", c.input, got, err, c.want)
			}
		})
	}
}

func TestProjectRootRelativeAndNoAncestorSearch(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".orchestrator/briefs", "child"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	physical, err := syscall.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ input, want string }{
		{"", physical}, {".orchestrator/briefs", physical}, {"child", filepath.Join(physical, "child")},
	} {
		if got, err := ProjectRoot(c.input); err != nil || got != c.want {
			t.Errorf("ProjectRoot(%q) = %q, %v; want %q", c.input, got, err, c.want)
		}
	}
	t.Chdir(filepath.Join(root, ".orchestrator", "briefs"))
	if got, err := ProjectRoot(""); err != nil || got != physical {
		t.Fatalf("nested cwd: %q, %v; want %q", got, err, physical)
	}
}

func TestProjectRootDoesNotResolveSymlinks(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".orchestrator")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := ProjectRoot(alias); err != nil || got != alias {
		t.Fatalf("symlink: %q, %v; want lexical %q", got, err, alias)
	}
}
