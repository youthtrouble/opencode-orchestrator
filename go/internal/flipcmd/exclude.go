package flipcmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// excludeLine is the line EnsureGitExclude appends to the repository's info/exclude.
const excludeLine = ".orchestrator/"

// gitFilePrefix starts a .git file: a linked worktree's or a submodule's pointer to its git dir.
const gitFilePrefix = "gitdir: "

// EnsureGitExclude is ensureGitExclude (lib/flip.mjs): it keeps the project's orchestrator
// state out of git by adding ".orchestrator/" to the repository's info/exclude unless a line
// already names it, and reports whether the project has a git repository at all (nothing is
// written when it has none). The exclude is found as gitExcludeFile finds it. The info
// directory is created when missing; an exclude that is a directory fails with Node's EISDIR
// read. A dangling symlink counts as missing and the append creates its target.
func EnsureGitExclude(root string) (bool, error) {
	file, found, err := gitExcludeFile(root)
	if err != nil || !found {
		return false, err
	}
	if err := nodefs.MkdirAll(filepath.Dir(file)); err != nil {
		return false, err
	}
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

// gitExcludeFile is the info/exclude git reads for the repository at root
// (gitrepository-layout): <root>/.git/info/exclude when .git is a directory. A .git file
// holds "gitdir: <path>", relative to root as git resolves it; when that git dir has a
// commondir file (a linked worktree) the exclude is the common dir's, the commondir path being
// relative to the git dir, else (a submodule) it is the git dir's own. No .git, a .git file
// that does not start with the prefix, one naming no directory, or a commondir naming no
// directory is no repository, as git judges it: found is false, so nothing is created outside
// the repository. Reading the .git or commondir file can fail with Node's errors.
func gitExcludeFile(root string) (file string, found bool, err error) {
	dotGit := filepath.Join(root, ".git")
	if !nodefs.Exists(dotGit) {
		return "", false, nil
	}
	if isDirectory(dotGit) {
		return filepath.Join(dotGit, "info", "exclude"), true, nil
	}
	pointer, err := readTrimmed(dotGit)
	if err != nil || !strings.HasPrefix(pointer, gitFilePrefix) {
		return "", false, err
	}
	gitDir := resolveAgainst(root, strings.TrimPrefix(pointer, gitFilePrefix))
	if !isDirectory(gitDir) {
		return "", false, nil
	}
	commonDirFile := filepath.Join(gitDir, "commondir")
	if !nodefs.Exists(commonDirFile) {
		return filepath.Join(gitDir, "info", "exclude"), true, nil
	}
	commonDirPath, err := readTrimmed(commonDirFile)
	if err != nil {
		return "", false, err
	}
	commonDir := resolveAgainst(gitDir, commonDirPath)
	if !isDirectory(commonDir) {
		return "", false, nil
	}
	return filepath.Join(commonDir, "info", "exclude"), true, nil
}

// isDirectory is a stat that follows symlinks; like nodefs.Exists, any failure is "no".
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// readTrimmed is fs.readFileSync(path, "utf8").trimEnd().
func readTrimmed(path string) (string, error) {
	data, err := nodefs.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(jsstr.DecodeUTF8(data), jsstr.WhiteSpace), nil
}

// resolveAgainst is path.resolve(base, p) for an absolute base.
func resolveAgainst(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
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
