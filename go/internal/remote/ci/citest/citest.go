// Package citest puts a fake git and a fake gh first on PATH, so tests exercise package ci and
// the commands built on it without the network or the owner's gh login. The fakes use only
// shell builtins; gh logs the directory and argv of every call.
package citest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitScript answers the two git commands ci runs: origin's URL (exit 2, git's "no such remote",
// when there is none) and origin/HEAD (exit 1 when unset).
const gitScript = `#!/bin/sh
case "$1 $2" in
  "remote get-url") %s ;;
  "symbolic-ref --quiet") %s ;;
esac
exit 99
`

// ghScript logs the call, one argument per line after the physical working directory, and the
// GH_HOST it saw, then does what the test set.
const ghScript = `#!/bin/sh
{ pwd -P; printf '%%s\n' "$@"; } > %s
printf '%%s' "$GH_HOST" > %s
printf x >> %s
%s
`

// Fake is an installed fake git and gh.
type Fake struct {
	t   *testing.T
	dir string
}

// Install writes a git whose origin is origin and whose origin/HEAD is head ("" for none),
// and a gh that lists no runs, and puts them first on PATH.
func Install(t *testing.T, origin, head string) *Fake {
	t.Helper()
	f := &Fake{t: t, dir: t.TempDir()}
	f.SetGit(origin, head)
	f.PrintRuns("[]")
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

// SetGit changes what git answers.
func (f *Fake) SetGit(origin, head string) {
	f.write("git", fmt.Sprintf(gitScript, answer(origin, 2), answer(head, 1)))
}

// FailOrigin makes git remote get-url write stderr and exit with code; origin/HEAD is unset.
func (f *Fake) FailOrigin(code int, stderr string) {
	f.write("git", fmt.Sprintf(gitScript, fmt.Sprintf("printf '%%s\\n' %s >&2; exit %d", quote(stderr), code), answer("", 1)))
}

// PrintRuns makes gh print runsJSON and succeed.
func (f *Fake) PrintRuns(runsJSON string) { f.setGh("printf '%s' " + quote(runsJSON)) }

// Fail makes gh write stderr and exit with code.
func (f *Fake) Fail(code int, stderr string) {
	f.setGh(fmt.Sprintf("printf '%%s\\n' %s >&2\nexit %d", quote(stderr), code))
}

// Hang makes gh outlast any timeout a test sets.
func (f *Fake) Hang() { f.setGh("exec /bin/sleep 30") }

// HangWithChild makes gh start a child that outlasts any timeout, then wait for it. It returns
// the file the child's pid is written to.
func (f *Fake) HangWithChild() (pidFile string) {
	pidFile = filepath.Join(f.dir, "gh.child")
	f.setGh("/bin/sleep 30 &\nprintf '%s' $! > " + quote(pidFile) + "\nwait")
	return pidFile
}

// RemoveGh deletes gh and leaves PATH holding only the fake git, so no real gh further along
// PATH is found either.
func (f *Fake) RemoveGh() {
	if err := os.Remove(filepath.Join(f.dir, "gh")); err != nil {
		f.t.Fatal(err)
	}
	f.t.Setenv("PATH", f.dir)
}

// Calls is how many times gh ran.
func (f *Fake) Calls() int {
	data, err := os.ReadFile(f.callsFile())
	if os.IsNotExist(err) {
		return 0
	} else if err != nil {
		f.t.Fatal(err)
	}
	return len(data)
}

// LastCall is the physical directory and the argv of gh's last call.
func (f *Fake) LastCall() (dir string, args []string) {
	data, err := os.ReadFile(filepath.Join(f.dir, "gh.args"))
	if err != nil {
		f.t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return lines[0], lines[1:]
}

// LastHost is the GH_HOST gh's last call ran with.
func (f *Fake) LastHost() string {
	data, err := os.ReadFile(filepath.Join(f.dir, "gh.host"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *Fake) setGh(behaviour string) {
	f.write("gh", fmt.Sprintf(ghScript, quote(filepath.Join(f.dir, "gh.args")), quote(filepath.Join(f.dir, "gh.host")),
		quote(f.callsFile()), behaviour))
}

func (f *Fake) callsFile() string { return filepath.Join(f.dir, "gh.calls") }

func (f *Fake) write(name, script string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

// answer prints value and succeeds, or fails with code when value is empty.
func answer(value string, code int) string {
	if value == "" {
		return fmt.Sprintf("exit %d", code)
	}
	return "printf '%s\\n' " + quote(value) + "; exit 0"
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
