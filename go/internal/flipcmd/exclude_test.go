package flipcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// An exclude that is a directory fails with Node's error.
func TestEnsureGitExcludeFailsWhenTheExcludeIsADirectory(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".git", "info", "exclude"))
	_, err := EnsureGitExclude(root)
	if err == nil || err.Error() != "EISDIR: illegal operation on a directory, read" {
		t.Errorf("err = %v", err)
	}
}

// A .git file points at the git dir; a linked worktree's git dir names the common dir, whose
// info/exclude git reads (gitrepository-layout).
func TestEnsureGitExcludeFollowsAGitFile(t *testing.T) {
	for _, c := range []struct {
		name, gitFile, commonDir, exclude string // exclude is relative to the temp dir
	}{
		{"worktree with a relative commondir", "gitdir: {abs}/main/.git/worktrees/w\n", "../..\n", "main/.git/info/exclude"},
		{"worktree with an absolute commondir", "gitdir: {abs}/main/.git/worktrees/w", "{abs}/main/.git", "main/.git/info/exclude"},
		{"submodule", "gitdir: ../main/.git/modules/sub\n", "", "main/.git/modules/sub/info/exclude"},
		{"relative gitdir ending in several line endings", "gitdir: .gitmeta/worktrees/w\r\n\r\n", "../..\n\r\n", "project/.gitmeta/info/exclude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			abs := func(s string) string { return strings.ReplaceAll(s, "{abs}", dir) }
			root := filepath.Join(dir, "project")
			writeFile(t, filepath.Join(root, ".git"), abs(c.gitFile))
			gitDir := strings.TrimRight(strings.TrimPrefix(abs(c.gitFile), "gitdir: "), "\r\n")
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(root, gitDir)
			}
			mkdir(t, gitDir)
			if c.commonDir != "" {
				writeFile(t, filepath.Join(gitDir, "commondir"), abs(c.commonDir))
			}
			hasGit, err := EnsureGitExclude(root)
			if err != nil || !hasGit {
				t.Fatalf("EnsureGitExclude = %v, %v; want true, nil", hasGit, err)
			}
			if got := readFile(t, filepath.Join(dir, c.exclude)); got != ".orchestrator/\n" {
				t.Errorf("%s = %q", c.exclude, got)
			}
		})
	}
}

// Git strips only the CR/LF ending a .git or commondir file, so a trailing space names a
// different directory than its space-less sibling, whose exclude must stay untouched (C4-SOL-R1).
func TestEnsureGitExcludeKeepsATrailingSpaceInAGitPath(t *testing.T) {
	for _, c := range []struct {
		name, gitFile, commonDir, target, sibling string // target and sibling are relative to root
	}{
		{"gitdir", "gitdir: meta \r\n", "", "meta ", "meta"},
		{"commondir", "gitdir: g\n", "../common \n", "common ", "common"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, ".git"), c.gitFile)
			if c.commonDir != "" {
				writeFile(t, filepath.Join(root, "g", "commondir"), c.commonDir)
			}
			mkdir(t, filepath.Join(root, c.target))
			mkdir(t, filepath.Join(root, c.sibling))
			hasGit, err := EnsureGitExclude(root)
			if err != nil || !hasGit {
				t.Fatalf("EnsureGitExclude = %v, %v; want true, nil", hasGit, err)
			}
			if got := readFile(t, filepath.Join(root, c.target, "info", "exclude")); got != ".orchestrator/\n" {
				t.Errorf("exclude = %q", got)
			}
			if entries, _ := os.ReadDir(filepath.Join(root, c.sibling)); len(entries) != 0 {
				t.Errorf("sibling %q gained %v", c.sibling, entries)
			}
		})
	}
}

// A .git file that is not "gitdir: <path>", or names no directory, is no repository.
func TestEnsureGitExcludeIgnoresABrokenGitFile(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":         "not a pointer\n",
		"empty":             "",
		"no space":          "gitdir:x\n",
		"missing gitdir":    "gitdir: /elsewhere\n",
		"gitdir is a file":  "gitdir: file\n",
		"relative, missing": "gitdir: elsewhere\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, ".git"), content)
			writeFile(t, filepath.Join(root, "file"), "")
			hasGit, err := EnsureGitExclude(root)
			if err != nil || hasGit {
				t.Fatalf("EnsureGitExclude = %v, %v; want false, nil", hasGit, err)
			}
			if entries, _ := os.ReadDir(root); len(entries) != 2 {
				t.Errorf("wrote %v", entries)
			}
		})
	}
}

// A linked worktree whose commondir names no directory is no repository, as git judges it:
// nothing is created where the commondir points (C4-F1).
func TestEnsureGitExcludeIgnoresACommondirNamingNoDirectory(t *testing.T) {
	for name, commonDir := range map[string]string{
		"relative, missing": "../../../absent\n",
		"absolute, missing": "{root}/absent",
		"a file":            "../../file\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitDir := filepath.Join(root, "meta", "worktrees", "w")
			writeFile(t, filepath.Join(root, ".git"), "gitdir: meta/worktrees/w\n")
			writeFile(t, filepath.Join(root, "meta", "file"), "")
			writeFile(t, filepath.Join(gitDir, "commondir"), strings.ReplaceAll(commonDir, "{root}", root))
			hasGit, err := EnsureGitExclude(root)
			if err != nil || hasGit {
				t.Fatalf("EnsureGitExclude = %v, %v; want false, nil", hasGit, err)
			}
			if entries, _ := os.ReadDir(filepath.Join(root, "meta")); len(entries) != 2 {
				t.Errorf("meta holds %v", entries)
			}
			if _, err := os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
				t.Errorf("absent: %v", err)
			}
		})
	}
}

// Real git agrees: in a linked worktree, a submodule and a repository whose .git file holds a
// relative gitdir, the line lands where git reads it.
func TestEnsureGitExcludeBehindAGitFileAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	runGit(t, dir, "init", "-q", main)
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "first")

	worktree := filepath.Join(dir, "worktree")
	runGit(t, main, "worktree", "add", "-q", worktree)
	wantExcluded(t, worktree, filepath.Join(main, ".git", "info", "exclude"))

	library := filepath.Join(dir, "library")
	runGit(t, dir, "init", "-q", library)
	runGit(t, library, "commit", "-q", "--allow-empty", "-m", "first")
	runGit(t, main, "-c", "protocol.file.allow=always", "submodule", "add", "-q", library, "sub")
	wantExcluded(t, filepath.Join(main, "sub"), filepath.Join(main, ".git", "modules", "sub", "info", "exclude"))

	// Git resolves a relative gitdir against the directory holding the .git file.
	moved := filepath.Join(dir, "moved")
	runGit(t, dir, "init", "-q", moved)
	if err := os.Rename(filepath.Join(moved, ".git"), filepath.Join(moved, ".gitmeta")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(moved, ".git"), "gitdir: .gitmeta\n")
	wantExcluded(t, moved, filepath.Join(moved, ".gitmeta", "info", "exclude"))

	// Git keeps a trailing space in the gitdir path, stripping only the line ending (C4-SOL-R1).
	spaced := filepath.Join(dir, "spaced")
	runGit(t, dir, "init", "-q", spaced)
	if err := os.Rename(filepath.Join(spaced, ".git"), filepath.Join(spaced, ".gitmeta ")); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(spaced, ".gitmeta"))
	writeFile(t, filepath.Join(spaced, ".git"), "gitdir: .gitmeta \r\n")
	wantExcluded(t, spaced, filepath.Join(spaced, ".gitmeta ", "info", "exclude"))
	if entries, _ := os.ReadDir(filepath.Join(spaced, ".gitmeta")); len(entries) != 0 {
		t.Errorf("space-less sibling gained %v", entries)
	}
}

// wantExcluded runs EnsureGitExclude in root and checks that exclude holds the line and that
// git ignores .orchestrator/ there.
func wantExcluded(t *testing.T, root, exclude string) {
	t.Helper()
	hasGit, err := EnsureGitExclude(root)
	if err != nil || !hasGit {
		t.Fatalf("EnsureGitExclude(%s) = %v, %v; want true, nil", root, hasGit, err)
	}
	if got := readFile(t, exclude); !strings.Contains(got, "\n"+excludeLine+"\n") && !strings.HasPrefix(got, excludeLine+"\n") {
		t.Errorf("%s = %q, want a line %q", exclude, got, excludeLine)
	}
	if err := exec.Command("git", "-C", root, "check-ignore", "-q", ".orchestrator/x").Run(); err != nil {
		t.Errorf("git -C %s check-ignore .orchestrator/x = %v, want ignored", root, err)
	}
}

// runGit runs git in dir without the user's config (no signing, no hooks), as a fixed author.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func text(s string) *string { return &s }

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
