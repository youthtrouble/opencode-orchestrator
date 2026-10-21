// Package discover finds the orchestrated projects Work Trains reports: under each configured
// root, the root itself and each direct child directory that holds a .orchestrator/ directory.
// It only stats and reads small git pointer files; it never writes.
package discover

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/config"
)

// Project is one discovered project.
type Project struct {
	Name     string // the directory basename, as found
	Root     string // the path discovery found it at; its state is read from here
	RealPath string // Root with every symlink resolved: the project's identity
}

// Error is a root or candidate directory that could not be examined. It never stops the scan:
// the other projects are still found.
type Error struct {
	Path string
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }

func (e *Error) Unwrap() error { return e.Err }

// Projects scans cfg's roots and returns the projects found, sorted by name (then real path),
// one per real path, and an Error for each root or candidate that could not be examined. Linked
// git worktrees are skipped unless cfg.IncludeWorktrees, and so is any directory cfg.Exclude
// names by basename or by absolute path (the path as found or its real path).
func Projects(cfg config.Config) ([]Project, []*Error) {
	s := &scan{cfg: cfg, seen: map[string]bool{}}
	for _, root := range cfg.Roots {
		s.scanRoot(root)
	}
	slices.SortFunc(s.projects, func(a, b Project) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.RealPath, b.RealPath))
	})
	return s.projects, s.errors
}

type scan struct {
	cfg      config.Config
	seen     map[string]bool // real paths already found
	projects []Project
	errors   []*Error
}

func (s *scan) scanRoot(root string) {
	s.consider(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		s.fail(root, err)
		return
	}
	for _, entry := range entries {
		s.consider(filepath.Join(root, entry.Name()))
	}
}

// consider adds dir when it is a project that is neither excluded, a skipped linked worktree,
// nor already found. A file or a dangling symlink is simply not a project. Excludes that name
// dir as found are tested before anything is read, so an excluded directory the owner cannot
// read is skipped quietly rather than reported.
func (s *scan) consider(dir string) {
	if s.excluded(dir) {
		return
	}
	var notAProject *paths.NotAProjectError
	if err := paths.RequireProject(dir, ""); errors.As(err, &notAProject) {
		return
	} else if err != nil {
		s.fail(dir, err)
		return
	}
	realPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		s.fail(dir, err)
		return
	}
	if s.excluded(realPath) || s.seen[realPath] {
		return
	}
	if !s.cfg.IncludeWorktrees {
		linked, err := isLinkedWorktree(realPath)
		if err != nil {
			s.fail(dir, err)
			return
		}
		if linked {
			return
		}
	}
	s.seen[realPath] = true
	s.projects = append(s.projects, Project{Name: filepath.Base(dir), Root: dir, RealPath: realPath})
}

// excluded reports whether a cfg.Exclude entry names path: an absolute entry by the whole path,
// any other entry by its basename. consider asks for the path as found and for its real path.
func (s *scan) excluded(path string) bool {
	for _, entry := range s.cfg.Exclude {
		if filepath.IsAbs(entry) {
			if filepath.Clean(entry) == filepath.Clean(path) {
				return true
			}
		} else if entry == filepath.Base(path) {
			return true
		}
	}
	return false
}

func (s *scan) fail(path string, err error) {
	s.errors = append(s.errors, &Error{Path: path, Err: err})
}

// gitFilePrefix starts a .git file, a linked worktree's or a submodule's pointer to its git dir.
const gitFilePrefix = "gitdir: "

// isLinkedWorktree reports whether dir is a linked git worktree, judged as git lays them out
// (gitrepository-layout, and flipcmd's gitExcludeFile): its .git is a file "gitdir: <path>"
// (relative to dir) whose git dir holds a commondir file. A submodule's git dir has none, and a
// main checkout's .git is a directory.
func isLinkedWorktree(dir string) (bool, error) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case info.IsDir():
		return false, nil
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return false, err
	}
	pointer, isPointer := strings.CutPrefix(strings.TrimRight(string(data), " \t\r\n"), gitFilePrefix)
	if !isPointer {
		return false, nil
	}
	gitDir := pointer
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	_, err = os.Stat(filepath.Join(gitDir, "commondir"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return false, nil // a git dir that is missing or not a directory: no worktree, as git judges it
	}
	return false, err
}
