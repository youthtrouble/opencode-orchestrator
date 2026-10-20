package flipcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureGitExcludeAddsTheLineUnlessALineNamesIt(t *testing.T) {
	const added = ".orchestrator/\n"
	for _, c := range []struct {
		name    string
		current *string // nil: no exclude file
		want    string
	}{
		{"absent", nil, added},
		{"empty", text(""), added},
		{"no trailing newline", text("foo"), "foo\n" + added},
		{"trailing newline", text("foo\n"), "foo\n" + added},
		{"trailing carriage return", text("foo\r"), "foo\r\n" + added},
		{"bare name", text(".orchestrator"), ".orchestrator"},
		{"name with slash", text("a\n.orchestrator/\nb\n"), "a\n.orchestrator/\nb\n"},
		{"CRLF lines", text("a\r\n.orchestrator/\r\n"), "a\r\n.orchestrator/\r\n"},
		{"CRLF bare name last", text("a\r\n.orchestrator\r"), "a\r\n.orchestrator\r"},
		// Git ends lines only at \n, so a lone \r, U+2028 or U+2029 is inside a line.
		{"carriage return lines", text("a\r.orchestrator\rb"), "a\r.orchestrator\rb\n" + added},
		{"U+2028 lines", text("a\u2028.orchestrator/\u2028b"), "a\u2028.orchestrator/\u2028b\n" + added},
		{"U+2029 lines", text("a\u2029.orchestrator/"), "a\u2029.orchestrator/\n" + added},
		{"two carriage returns", text(".orchestrator/\r\r\n"), ".orchestrator/\r\r\n" + added},
		{"trailing space", text(".orchestrator/ \n"), ".orchestrator/ \n" + added},
		{"leading space", text(" .orchestrator/"), " .orchestrator/\n" + added},
		{"double slash", text(".orchestrator//\n"), ".orchestrator//\n" + added},
		{"other case", text(".Orchestrator/\n"), ".Orchestrator/\n" + added},
		{"invalid UTF-8 at the end", text("foo\xff"), "foo\xff\n" + added},
		{"truncated sequence at the end", text("foo\xe2\x80"), "foo\xe2\x80\n" + added},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			exclude := filepath.Join(root, ".git", "info", "exclude")
			mkdir(t, filepath.Join(root, ".git"))
			if c.current != nil {
				writeFile(t, exclude, *c.current)
			}
			hasGit, err := EnsureGitExclude(root)
			if err != nil || !hasGit {
				t.Fatalf("EnsureGitExclude = %v, %v; want true, nil", hasGit, err)
			}
			if got := readFile(t, exclude); got != c.want {
				t.Errorf("exclude = %q, want %q", got, c.want)
			}
		})
	}
}

// Whatever EnsureGitExclude accepts as already excluding .orchestrator/, Git ignores: a line
// it counts but Git does not would let orchestrator state into commits.
func TestEnsureGitExcludeAgreesWithGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	for _, current := range []string{
		".orchestrator/\n", ".orchestrator\r\n", "a\r.orchestrator/\rb", "a\u2028.orchestrator/\u2028b", "a .orchestrator/ b\n",
	} {
		root := t.TempDir()
		if out, err := exec.Command(git, "init", "-q", root).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		writeFile(t, filepath.Join(root, ".git", "info", "exclude"), current)
		if _, err := EnsureGitExclude(root); err != nil {
			t.Fatal(err)
		}
		checkIgnore := exec.Command(git, "-C", root, "check-ignore", "-q", ".orchestrator/x")
		if err := checkIgnore.Run(); err != nil {
			t.Errorf("exclude %q: git check-ignore .orchestrator/x = %v, want ignored", readFile(t, filepath.Join(root, ".git", "info", "exclude")), err)
		}
	}
}

func TestEnsureGitExcludeCreatesTheFileWithTheUmaskApplied(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".git"))
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	if _, err := EnsureGitExclude(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil || info.Mode().Perm() != 0o666&^os.FileMode(umask) {
		t.Errorf("mode %v (%v), want 0666 less the umask %03o", info.Mode(), err, umask)
	}
}

// A dangling symlink is a missing exclude for existsSync; the append creates its target.
func TestEnsureGitExcludeAppendsThroughADanglingSymlink(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".git", "info"))
	target := filepath.Join(root, "target")
	if err := os.Symlink(target, filepath.Join(root, ".git", "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGitExclude(root); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != ".orchestrator/\n" {
		t.Errorf("target = %q", got)
	}
}

func TestEnsureGitExcludeWithoutGitWritesNothing(t *testing.T) {
	root := t.TempDir()
	hasGit, err := EnsureGitExclude(root)
	if err != nil || hasGit {
		t.Fatalf("EnsureGitExclude = %v, %v; want false, nil", hasGit, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// The JS assumes .git is a directory; a worktree's or submodule's .git file and an exclude
// that is a directory fail with Node's errors.
func TestEnsureGitExcludeFailsWithNodesErrors(t *testing.T) {
	t.Run(".git is a file", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, ".git"), "gitdir: elsewhere\n")
		_, err := EnsureGitExclude(root)
		want := "ENOTDIR: not a directory, mkdir '" + filepath.Join(root, ".git", "info") + "'"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
		if got := readFile(t, filepath.Join(root, ".git")); got != "gitdir: elsewhere\n" {
			t.Errorf(".git = %q", got)
		}
	})
	t.Run("exclude is a directory", func(t *testing.T) {
		root := t.TempDir()
		mkdir(t, filepath.Join(root, ".git", "info", "exclude"))
		_, err := EnsureGitExclude(root)
		if err == nil || err.Error() != "EISDIR: illegal operation on a directory, read" {
			t.Errorf("err = %v", err)
		}
	})
}

func text(s string) *string { return &s }

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
