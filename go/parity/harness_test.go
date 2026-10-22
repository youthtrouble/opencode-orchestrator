//go:build parity

// Package parity runs the JS runtime and orch on identical inputs in identical sandboxes and
// requires identical exit codes, stdout, stderr and state files. Run it through run.sh, which
// builds the binary and sets ORCH_BIN and ORCH_JS.
package parity

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestParity(t *testing.T) {
	h := newHarness(t)
	t.Run("json", h.testJSON)
	t.Run("usage", h.testUsage)
	t.Run("mailbox", h.testMailbox)
	t.Run("manifest", h.testManifest)
	t.Run("incident", h.testIncident)
	t.Run("policy", h.testPolicy)
	t.Run("bridges", h.testBridges)
	t.Run("worker", h.testWorker)
	t.Run("flip", h.testFlip)
	t.Run("supervisor", h.testSupervisor)
	t.Run("doctor", h.testDoctor)
	t.Run("watch", h.testWatch)
	t.Run("kickoff", h.testKickoff)
	t.Run("session", h.testSession)
}

// testJSON compares JSON.stringify(JSON.parse(stdin), null, indent) with `orch json-echo` over
// every golden input plus the JS reference's models.json.
func (h *harness) testJSON(t *testing.T) {
	inputs, err := filepath.Glob("../testdata/json/*.json")
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no JSON inputs (err %v)", err)
	}
	inputs = append(inputs, filepath.Join(h.orchJS, "models.json"))
	const echo = `const indent = Number(process.argv[1]); console.log(JSON.stringify(JSON.parse(require("node:fs").readFileSync(0, "utf8")), null, indent))`
	for _, input := range inputs {
		stdin, err := os.ReadFile(input)
		if err != nil {
			t.Fatal(err)
		}
		for _, indent := range []string{"0", "2"} {
			t.Run(filepath.Base(input)+"/indent="+indent, func(t *testing.T) {
				h.comparePair(t, invocation{
					js:    []string{h.node, "-e", echo, indent},
					orch:  []string{h.orchBin, "json-echo", "--indent", indent},
					stdin: stdin,
				})
			})
		}
	}
}

type harness struct {
	orchBin   string
	orchJS    string
	node      string
	nodeMajor int // major version of node, for the cases whose JS output depends on it (PE-5)
	fakesDir  string
	toolsDir  string // links to node and sqlite3, the only real tools on path
	path      string
}

// referenceNodeMajor is the Node release line whose JS output parity is defined against where
// the JS itself differs between Node versions (PE-5: V8's sort of short arrays under an
// inconsistent comparator). Every golden was generated with it; CI installs it.
const referenceNodeMajor = 26

func newHarness(t *testing.T) *harness {
	orchBin, orchJS := os.Getenv("ORCH_BIN"), os.Getenv("ORCH_JS")
	if orchBin == "" || orchJS == "" {
		t.Fatal("ORCH_BIN and ORCH_JS must be set; run go/parity/run.sh")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for parity runs:", err)
	}
	fakesDir, err := filepath.Abs("fakes")
	if err != nil {
		t.Fatal(err)
	}
	toolsDir, err := linkRealTools(node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(toolsDir) })
	version, err := exec.Command(node, "-p", "process.versions.node").Output()
	if err != nil {
		t.Fatal("node version:", err)
	}
	major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(string(version)), ".", 2)[0])
	if err != nil {
		t.Fatalf("node version %q: %v", version, err)
	}
	// Fakes come first so no case can reach a real claude, codex or opencode. node and sqlite3
	// are the only real tools either runtime may use, linked into a private directory rather
	// than putting their host directories on PATH, where an installed orch (or anything else)
	// beside them could change a case (S4/R10).
	return &harness{orchBin: orchBin, orchJS: orchJS, node: node, nodeMajor: major, fakesDir: fakesDir, toolsDir: toolsDir, path: fakesDir + ":" + toolsDir}
}

// linkRealTools makes a temporary directory holding a symlink named node to node and, when
// sqlite3 is installed, one named sqlite3 to it. newHarness removes it when the test ends.
func linkRealTools(node string) (string, error) {
	dir, err := os.MkdirTemp("", "orch-parity-tools-")
	if err != nil {
		return "", err
	}
	links := map[string]string{"node": node}
	if sqlite, err := exec.LookPath("sqlite3"); err == nil {
		links["sqlite3"] = sqlite
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			return "", errors.Join(err, os.RemoveAll(dir))
		}
	}
	return dir, nil
}

// invocation is one parity case: the same input given to each runtime. seed, when set,
// prepares each runtime's fresh sandbox identically before the run; env adds entries to the
// environment (where "$PROJECT" stands for the sandbox project directory); extraFiles are
// project-relative files compared besides .orchestrator/ (as "extra/<path>"), and
// extraFilesMayBeBlocked lets one of them be a directory or lie under a file (as no file) for
// the cases that make it so, where any other read error stays fatal; normalize, when
// set, rewrites stdout, stderr and file contents of both before comparing (for values that
// legitimately differ between runs, such as a fresh timestamp); crash, when set, is the
// class of uncaught JS exception the case ends in (see crashError). C1 also pins errorLine;
// progress, when set, is the prefix of the progress lines a crash may follow (see
// crashAfterProgressError). projectPaths replaces the sandbox project directory with "$PROJECT"
// in stdout, stderr and every file, for commands that print or record their working directory.
// normalizeWrites receives seeded files separately so fresh fields cannot mask seed mutations.
type invocation struct {
	js                     []string
	orch                   []string
	stdin                  []byte
	env                    []string
	extraFiles             []string
	extraFilesMayBeBlocked bool
	seed                   func(t *testing.T, sb sandbox)
	normalize              func(string) string
	crash                  crashClass
	errorLine              string
	progress               string
	projectPaths           bool
	normalizeWrites        func(outcome, map[string]string) outcome
	// daemon runs the command as a supervisor serve: started, stopped through its stop file
	// once it holds its lock, and waited for (see runDaemon).
	daemon bool
}

// crashClass names how a case that ends in an uncaught JS exception is compared (plan
// go-s4.md section 3). Node prints a source excerpt, a stack and its version around the error
// line; orch prints the error line alone.
type crashClass int

const (
	noCrash crashClass = iota
	// crashC1 is a fixed message: orch's whole stderr is one line that must appear as a
	// whole line in the JS stderr.
	crashC1
	// crashC2 is a V8 TypeError on a malformed value: both stderrs must hold a line starting
	// "TypeError: ", whose wording is not compared.
	crashC2
)

// outcome is everything a run can be compared on. files maps paths relative to the sandbox
// (project .orchestrator/, the HOME state dir and the fakes' logs) to their contents.
type outcome struct {
	exitCode int
	stdout   string
	stderr   string
	files    map[string]string
}

// comparePair runs each runtime in its own fresh sandbox and reports every difference.
func (h *harness) comparePair(t *testing.T, inv invocation) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	if inv.crash != noCrash {
		err := crashError(inv.crash, inv.errorLine, js, orch)
		if inv.progress != "" {
			err = crashAfterProgressError(inv.crash, inv.errorLine, inv.progress, js, orch)
		}
		if err != nil {
			t.Error(err)
		}
		js.stderr, orch.stderr = "", ""
	}
	compareOutcomes(t, js, orch)
}

// runPair runs each runtime in its own fresh sandbox and returns both normalized outcomes,
// for cases that assert a documented difference before comparing the rest.
func (h *harness) runPair(t *testing.T, inv invocation) (js, orch outcome) {
	t.Helper()
	run := func(argv []string) outcome {
		sb := h.newSandbox(t)
		if inv.seed != nil {
			inv.seed(t, sb)
		}
		before := map[string]string{}
		if inv.normalizeWrites != nil {
			collectFiles(t, filepath.Join(sb.project, ".orchestrator"), "project", before)
			collectExtraFiles(t, sb, inv, before)
		}
		runner := h.run
		if inv.daemon {
			runner = h.runDaemon
		}
		o := runner(t, sb, argv, inv.stdin, inv.env)
		if strings.Contains(inv.errorLine, "$PROJECT") {
			canonicalProject, err := filepath.EvalSymlinks(sb.project)
			if err != nil {
				t.Fatal(err)
			}
			o.stderr = strings.ReplaceAll(o.stderr, canonicalProject, "$PROJECT")
			o.stderr = strings.ReplaceAll(o.stderr, sb.project, "$PROJECT")
		}
		collectExtraFiles(t, sb, inv, o.files)
		if inv.projectPaths {
			o = o.withProjectPlaceholder(t, sb)
		}
		if inv.normalizeWrites != nil {
			o = inv.normalizeWrites(o, before)
		}
		return o.normalized(inv.normalize)
	}
	return run(inv.js), run(inv.orch)
}

// crashError checks diagnostics independently of stdout and files, which remain exact inputs
// to compareOutcomes. Returning an error makes negative comparator tests nonvacuous too.
func crashError(class crashClass, expected string, js, orch outcome) error {
	if js.exitCode != 1 || orch.exitCode != 1 {
		return fmt.Errorf("crash exits: js %d, orch %d; want both 1", js.exitCode, orch.exitCode)
	}
	line, isOneLine := strings.CutSuffix(orch.stderr, "\n")
	if !isOneLine || strings.Contains(line, "\n") || strings.TrimSpace(line) == "" {
		return fmt.Errorf("orch stderr is not one nonempty line: %q", orch.stderr)
	}
	jsLines := strings.Split(js.stderr, "\n")
	switch class {
	case crashC1:
		if !strings.HasPrefix(expected, "Error: ") && !strings.HasPrefix(expected, "TypeError: ") && !strings.HasPrefix(expected, "TypeError [") {
			return fmt.Errorf("C1 needs a pinned error line, got %q", expected)
		}
		if line != expected || !slices.Contains(jsLines, expected) {
			return fmt.Errorf("C1 want %q; orch %q; js stderr:\n%s", expected, line, js.stderr)
		}
	case crashC2:
		if !strings.HasPrefix(line, "TypeError: ") {
			return fmt.Errorf("orch stderr %q is not a TypeError", line)
		}
		if !slices.ContainsFunc(jsLines, func(l string) bool { return strings.HasPrefix(l, "TypeError: ") }) {
			return fmt.Errorf("js stderr has no TypeError line:\n%s", js.stderr)
		}
	}
	return nil
}

// crashAfterProgressError compares a crash that follows progress output: orch's stderr is
// progress lines (each starting with prefix) and then the error line. The JS stderr must begin
// with exactly those progress lines and hold no further line starting with prefix; the rest of
// each stderr is then compared as crashError compares a crash.
func crashAfterProgressError(class crashClass, expected, prefix string, js, orch outcome) error {
	cut := strings.LastIndex(strings.TrimSuffix(orch.stderr, "\n"), "\n") + 1
	progress := orch.stderr[:cut]
	for _, line := range strings.SplitAfter(progress, "\n") {
		if line != "" && !strings.HasPrefix(line, prefix) {
			return fmt.Errorf("orch stderr line %q before the error line is not progress", line)
		}
	}
	rest, found := strings.CutPrefix(js.stderr, progress)
	if !found {
		return fmt.Errorf("progress differs\n  js: %q\norch: %q", js.stderr, progress)
	}
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, prefix) {
			return fmt.Errorf("js printed progress orch did not: %q", line)
		}
	}
	js.stderr, orch.stderr = rest, orch.stderr[cut:]
	return crashError(class, expected, js, orch)
}

func compareOutcomes(t *testing.T, js, orch outcome) {
	t.Helper()
	if js.exitCode != orch.exitCode {
		t.Errorf("exit code: js %d, orch %d", js.exitCode, orch.exitCode)
	}
	if js.stdout != orch.stdout {
		t.Errorf("stdout differs\n  js: %q\norch: %q", js.stdout, orch.stdout)
	}
	if js.stderr != orch.stderr {
		t.Errorf("stderr differs\n  js: %q\norch: %q", js.stderr, orch.stderr)
	}
	for name, content := range js.files {
		if other, ok := orch.files[name]; !ok {
			t.Errorf("file %s written by js only", name)
		} else if other != content {
			t.Errorf("file %s differs\n  js: %q\norch: %q", name, content, other)
		}
	}
	for name := range orch.files {
		if _, ok := js.files[name]; !ok {
			t.Errorf("file %s written by orch only", name)
		}
	}
}

func (o outcome) normalized(normalize func(string) string) outcome {
	if normalize == nil {
		return o
	}
	files := map[string]string{}
	for name, content := range o.files {
		files[name] = normalize(content)
	}
	return outcome{exitCode: o.exitCode, stdout: normalize(o.stdout), stderr: normalize(o.stderr), files: files}
}

// withProjectPlaceholder replaces the sandbox project directory, as the physical path Node's
// process.cwd() reports and as created, with "$PROJECT".
func (o outcome) withProjectPlaceholder(t *testing.T, sb sandbox) outcome {
	t.Helper()
	physical, err := filepath.EvalSymlinks(sb.project)
	if err != nil {
		t.Fatal(err)
	}
	replace := strings.NewReplacer(physical, "$PROJECT", sb.project, "$PROJECT").Replace
	files := map[string]string{}
	for name, content := range o.files {
		files[name] = replace(content)
	}
	return outcome{exitCode: o.exitCode, stdout: replace(o.stdout), stderr: replace(o.stderr), files: files}
}

// sandbox is a fresh temp HOME and a fresh project (with .git/), the working directory.
type sandbox struct {
	home    string
	project string
}

func (h *harness) newSandbox(t *testing.T) sandbox {
	t.Helper()
	sb := sandbox{home: t.TempDir(), project: t.TempDir()}
	if err := os.Mkdir(filepath.Join(sb.project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sb
}

// run runs argv in the sandbox with an environment holding nothing but what section 5.1 of
// the plan allows, then collects the files it may have written.
func (h *harness) run(t *testing.T, sb sandbox, argv []string, stdin []byte, env []string) outcome {
	t.Helper()
	cmd := h.command(sb, argv, stdin, env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return outcome{exitCode: exitCode(t, argv[0], cmd.Run()), stdout: stdout.String(), stderr: stderr.String(), files: sb.files(t)}
}

// command is argv prepared to run in the sandbox project with the plan's environment.
func (h *harness) command(sb sandbox, argv []string, stdin []byte, env []string) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = sb.project
	cmd.Env = []string{
		"HOME=" + sb.home,
		"PATH=" + h.path,
		"ORCH_HOME=" + h.orchJS,
		"ORCH_REGISTRY=" + filepath.Join(h.orchJS, "models.json"),
	}
	for _, entry := range env {
		cmd.Env = append(cmd.Env, strings.ReplaceAll(entry, "$PROJECT", sb.project))
	}
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd
}

// exitCode is the exit code of a finished run, whose error is runErr.
func exitCode(t *testing.T, name string, runErr error) int {
	t.Helper()
	if runErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("run %s: %v", name, runErr)
	}
	return exitErr.ExitCode()
}

// files collects what a run may have written: the project's .orchestrator/, the HOME state dir
// and the fakes' logs.
func (sb sandbox) files(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	collectFiles(t, filepath.Join(sb.project, ".orchestrator"), "project", files)
	collectFiles(t, filepath.Join(sb.home, ".local", "state", "opencode", "orchestrator"), "state", files)
	collectFiles(t, filepath.Join(sb.home, "fake-log"), "fakelog", files)
	return files
}

// collectExtraFiles adds each of the invocation's extra files that is a regular file to
// files, keyed "extra/<path>". A missing path is not one; neither, when the invocation's
// extraFilesMayBeBlocked is set, is a directory or a path under a file (a case may make .git
// a file or the exclude a directory). Any other read error fails the test.
func collectExtraFiles(t *testing.T, sb sandbox, inv invocation, files map[string]string) {
	t.Helper()
	for _, rel := range inv.extraFiles {
		content, err := os.ReadFile(filepath.Join(sb.project, rel))
		switch {
		case err == nil:
			files["extra/"+rel] = string(content)
		case errors.Is(err, fs.ErrNotExist):
		case inv.extraFilesMayBeBlocked && (errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR)):
		default:
			t.Fatal(err)
		}
	}
}

// collectFiles adds every regular file under root to files, keyed "<label>/<relative path>".
// A missing root contributes nothing.
func collectFiles(t *testing.T, root, label string, files map[string]string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[fmt.Sprintf("%s/%s", label, rel)] = string(content)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("collect %s: %v", root, err)
	}
}
