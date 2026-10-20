package flipcmd

import (
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// excludeLine is the line EnsureGitExclude appends to .git/info/exclude.
const excludeLine = ".orchestrator/"

// EnsureGitExclude is ensureGitExclude (bin/flip.mjs l.24-32; kickoff inlines the same): it
// keeps the project's orchestrator state out of git by adding ".orchestrator/" to
// .git/info/exclude unless a line already names it, and reports whether the project has a
// .git at all (nothing is written when it has none).
//
// Like the JS, it assumes .git is a directory: when .git is a file (a worktree or a
// submodule) creating .git/info fails with Node's ENOTDIR before anything is written. An
// exclude that is a directory fails with Node's EISDIR read. A dangling symlink counts as
// missing and the append creates its target.
func EnsureGitExclude(root string) (bool, error) {
	if !nodefs.Exists(filepath.Join(root, ".git")) {
		return false, nil
	}
	info := filepath.Join(root, ".git", "info")
	if err := nodefs.MkdirAll(info); err != nil {
		return false, err
	}
	file := filepath.Join(info, "exclude")
	current := ""
	if nodefs.Exists(file) {
		data, err := nodefs.ReadFile(file)
		if err != nil {
			return false, err
		}
		current = jsstr.DecodeUTF8(data)
	}
	if excludesOrchestrator(current) {
		return true, nil
	}
	separator := "\n"
	if current == "" || strings.HasSuffix(current, "\n") {
		separator = ""
	}
	return true, nodefs.AppendFile(file, []byte(separator+excludeLine+"\n"))
}

// excludesOrchestrator reports whether some line of current is exactly ".orchestrator" or
// ".orchestrator/" as Git reads the file: lines end only at \n, and one trailing \r is
// dropped. A lone \r, U+2028 or U+2029 is part of a line, so "a\r.orchestrator/" excludes
// nothing; counting it would leak .orchestrator/ into commits, while missing a line only
// appends a harmless duplicate. Git also ignores trailing unescaped spaces, so it reads
// ".orchestrator/ " as the pattern; that line is deliberately not recognised, the duplicate
// append being the safe direction (the JS regexes match exactly as this does).
func excludesOrchestrator(current string) bool {
	for _, line := range strings.Split(current, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == ".orchestrator" || line == excludeLine {
			return true
		}
	}
	return false
}
