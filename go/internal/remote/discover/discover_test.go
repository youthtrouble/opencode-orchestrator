package discover

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/config"
)

// tree builds dir layouts: every path ending in "/" is a directory, anything else a file with
// the given content.
func tree(t *testing.T, root string, entries map[string]string) {
	t.Helper()
	for path, content := range entries {
		full := filepath.Join(root, path)
		if path[len(path)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// realTempDir is t.TempDir with symlinks resolved (macOS's /var is a link to /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func names(projects []Project) []string {
	out := []string{}
	for _, p := range projects {
		out = append(out, p.Name)
	}
	return out
}

func TestProjectsFindsTheRootAndItsDirectChildrenWithOrchestratorState(t *testing.T) {
	root := filepath.Join(realTempDir(t), "workspace")
	tree(t, root, map[string]string{
		".orchestrator/":                 "",
		"beta/.orchestrator/":            "",
		"alpha/.orchestrator/STATE.md":   "x",
		"plain/README.md":                "x",
		"file-not-dir/.orchestrator":     "a file is not project state",
		"deep/nested/.orchestrator/":     "",
		"loose-file":                     "x",
		"gamma/.orchestrator/mailbox/":   "",
		"gamma/sub/.orchestrator/":       "",
		"alpha/child/.orchestrator/mail": "",
	})
	projects, errs := Projects(config.Config{Roots: []string{root}})
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	want := []string{"alpha", "beta", "gamma", "workspace"}
	if got := names(projects); !reflect.DeepEqual(got, want) {
		t.Errorf("projects = %v, want %v", got, want)
	}
	if projects[0].Root != filepath.Join(root, "alpha") || projects[0].RealPath != filepath.Join(root, "alpha") {
		t.Errorf("alpha = %+v", projects[0])
	}
}

func TestProjectsSkipsLinkedWorktreesUnlessIncluded(t *testing.T) {
	root := realTempDir(t)
	tree(t, root, map[string]string{
		"main/.orchestrator/":                    "",
		"main/.git/worktrees/main-wt2/commondir": "../..",
		"main-wt2/.orchestrator/":                "",
		"main-wt2/.git":                          "gitdir: ../main/.git/worktrees/main-wt2\n",
		"with-submodule/.orchestrator/":          "",
		"with-submodule/.git":                    "gitdir: ../main/.git/modules/sub\n",
		"main/.git/modules/sub/HEAD":             "ref: refs/heads/main\n",
		"dangling-pointer/.orchestrator/":        "",
		"dangling-pointer/.git":                  "gitdir: /nowhere/at/all\n",
		"not-a-pointer/.orchestrator/":           "",
		"not-a-pointer/.git":                     "something else\n",
	})
	projects, errs := Projects(config.Config{Roots: []string{root}})
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got, want := names(projects), []string{"dangling-pointer", "main", "not-a-pointer", "with-submodule"}; !reflect.DeepEqual(got, want) {
		t.Errorf("projects = %v, want %v", got, want)
	}
	projects, _ = Projects(config.Config{Roots: []string{root}, IncludeWorktrees: true})
	if got, want := names(projects), []string{"dangling-pointer", "main", "main-wt2", "not-a-pointer", "with-submodule"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with worktrees: projects = %v, want %v", got, want)
	}
}

func TestProjectsHonoursExcludesByNameAndByPath(t *testing.T) {
	root := realTempDir(t)
	tree(t, root, map[string]string{
		"keep/.orchestrator/":      "",
		"scratch/.orchestrator/":   "",
		"by-path/.orchestrator/":   "",
		"elsewhere/.orchestrator/": "",
	})
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Roots:   []string{root},
		Exclude: []string{"scratch", filepath.Join(root, "by-path") + "/", filepath.Join(root, "elsewhere")},
	}
	projects, errs := Projects(cfg)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	// alias resolves to the excluded real path, so it goes too.
	if got, want := names(projects), []string{"keep"}; !reflect.DeepEqual(got, want) {
		t.Errorf("projects = %v, want %v", got, want)
	}
}

func TestProjectsDeduplicatesByRealPathAcrossRootsAndLinks(t *testing.T) {
	root := realTempDir(t)
	tree(t, root, map[string]string{"one/.orchestrator/": ""})
	if err := os.Symlink(filepath.Join(root, "one"), filepath.Join(root, "link-to-one")); err != nil {
		t.Fatal(err)
	}
	projects, errs := Projects(config.Config{Roots: []string{root, filepath.Join(root, "one")}})
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(projects) != 1 || projects[0].RealPath != filepath.Join(root, "one") {
		t.Errorf("projects = %+v, want the one real path once", projects)
	}
}

func TestProjectsReportsAMissingRootAndKeepsScanning(t *testing.T) {
	root := realTempDir(t)
	tree(t, root, map[string]string{"ok/.orchestrator/": ""})
	missing := filepath.Join(root, "no-such-root")
	projects, errs := Projects(config.Config{Roots: []string{missing, root}})
	if got := names(projects); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("projects = %v", got)
	}
	if len(errs) != 1 || errs[0].Path != missing || !errors.Is(errs[0], fs.ErrNotExist) {
		t.Errorf("errors = %v, want one ErrNotExist for %s", errs, missing)
	}
}

func TestProjectsReportsAnUnreadableCandidateAndKeepsScanning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through permissions")
	}
	root := realTempDir(t)
	tree(t, root, map[string]string{"ok/.orchestrator/": "", "locked/.orchestrator/": ""})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	projects, errs := Projects(config.Config{Roots: []string{root}})
	if got := names(projects); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("projects = %v", got)
	}
	if len(errs) != 1 || errs[0].Path != locked || !errors.Is(errs[0], fs.ErrPermission) {
		t.Errorf("errors = %v, want one permission error for %s", errs, locked)
	}
}

func TestProjectsSkipsAnExcludedUnreadableDirectoryWithoutAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through permissions")
	}
	root := realTempDir(t)
	tree(t, root, map[string]string{"ok/.orchestrator/": "", "private/.orchestrator/": "", "vault/.orchestrator/": ""})
	for _, name := range []string{"private", "vault"} {
		locked := filepath.Join(root, name)
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
	}
	cfg := config.Config{Roots: []string{root}, Exclude: []string{"private", filepath.Join(root, "vault")}}
	projects, errs := Projects(cfg)
	if len(errs) > 0 {
		t.Errorf("errors = %v, want none: both unreadable directories are excluded", errs)
	}
	if got := names(projects); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("projects = %v", got)
	}
}
