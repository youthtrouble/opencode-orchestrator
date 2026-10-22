package worker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

const detachRunID = "0123abcd-0000-4000-8000-000000000000"

// detachRun is a detached launch of the project's b.md whose runner is a sh script that logs
// its arguments and working directory into a temp directory, then writes to stdout and stderr.
type detachRun struct {
	log            string
	runner         string
	stdout, stderr bytes.Buffer
}

func newDetachRun(t *testing.T) *detachRun {
	t.Helper()
	d := &detachRun{log: t.TempDir()}
	d.runner = filepath.Join(d.log, "runner")
	writeFile(t, d.runner, `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done > "`+d.log+`/argv.tmp"
pwd -P > "`+d.log+`/cwd"
echo "runner out"
echo "runner err" >&2
mv "`+d.log+`/argv.tmp" "`+d.log+`/argv"
`)
	if err := os.Chmod(d.runner, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *detachRun) runtime(argv ...string) DetachRuntime {
	return DetachRuntime{
		Self:      d.runner,
		Argv:      argv,
		Stdout:    &d.stdout,
		Stderr:    &d.stderr,
		NewID:     func() string { return detachRunID },
		KeepAwake: func(cmd string, args []string) (string, []string) { return cmd, args },
	}
}

func detach(t *testing.T, p Project, rt DetachRuntime) (int, error) {
	t.Helper()
	launch, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	return launch.Detach(rt)
}

// reapRunner waits for the runner, still a child of the test process, to exit.
func reapRunner(t *testing.T, pid int) {
	t.Helper()
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &status, 0, nil); err != nil {
		t.Errorf("wait for runner %d: %v", pid, err)
	}
}

var launchedPid = regexp.MustCompile(`"runner_pid":(\d+),`)

func TestDetachLaunchesTheRunnerAndPrintsTheLaunch(t *testing.T) {
	p := testProject(t, "brief")
	d := newDetachRun(t)
	argv := []string{"start", "--brief", "b.md", "--detach", "--harness", "claude"}
	code, err := detach(t, p, d.runtime(argv...))
	if err != nil || code != 0 {
		t.Fatalf("Detach = %d, %v", code, err)
	}
	match := launchedPid.FindStringSubmatch(d.stdout.String())
	if match == nil {
		t.Fatalf("stdout %q has no runner_pid", d.stdout.String())
	}
	pid, _ := strconv.Atoi(match[1])
	reapRunner(t, pid)
	logPath := filepath.Join(p.LogsDir, "b-0123abcd.log")
	want := `{"lane":"b","runner_pid":` + match[1] + `,"log_path":"` + logPath + `","run_id":"` + detachRunID + `","keep_awake":false}` + "\n"
	if d.stdout.String() != want || d.stderr.String() != "" {
		t.Errorf("stdout %q, stderr %q; want %q", d.stdout.String(), d.stderr.String(), want)
	}
	wantArgv := "worker\nstart\n--brief\nb.md\n--harness\nclaude\n--log-path\n" + logPath + "\n--run-id\n" + detachRunID + "\n--cwd\n" + p.Root + "\n"
	if got := readText(t, filepath.Join(d.log, "argv")); got != wantArgv {
		t.Errorf("runner argv %q, want %q", got, wantArgv)
	}
	if got := readText(t, filepath.Join(d.log, "cwd")); got != p.Root+"\n" {
		t.Errorf("runner cwd %q, want the project root", got)
	}
	if got := readText(t, logPath); got != "runner out\nrunner err\n" {
		t.Errorf("log %q, want both of the runner's streams", got)
	}
	if info, err := os.Stat(logPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v (%v), want 0600", info.Mode(), err)
	}
}

func TestDetachReportsTheKeepAwakeWrapper(t *testing.T) {
	p := testProject(t, "brief")
	d := newDetachRun(t)
	wrapper := filepath.Join(d.log, "wrapper")
	writeFile(t, wrapper, "#!/bin/sh\nshift 2\nexec \"$@\"\n")
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	rt := d.runtime("start", "--brief", "b.md", "--harness", "claude", "--detach")
	var wrapped []string
	rt.KeepAwake = func(cmd string, args []string) (string, []string) {
		wrapped = append([]string{cmd}, args...)
		return wrapper, append([]string{"-i", "-s", cmd}, args...)
	}
	if code, err := detach(t, p, rt); err != nil || code != 0 {
		t.Fatalf("Detach = %d, %v", code, err)
	}
	match := launchedPid.FindStringSubmatch(d.stdout.String())
	if match == nil {
		t.Fatalf("stdout %q has no runner_pid", d.stdout.String())
	}
	pid, _ := strconv.Atoi(match[1])
	reapRunner(t, pid)
	if !strings.HasSuffix(d.stdout.String(), `"keep_awake":true}`+"\n") {
		t.Errorf("stdout %q, want keep_awake true", d.stdout.String())
	}
	if len(wrapped) == 0 || wrapped[0] != d.runner || wrapped[1] != "worker" {
		t.Errorf("KeepAwake got %q, want the runner and its arguments", wrapped)
	}
	if got := readText(t, filepath.Join(d.log, "argv")); !strings.HasPrefix(got, "worker\nstart\n") {
		t.Errorf("the wrapper did not run the runner: argv %q", got)
	}
}

// A runner that cannot be started is an error event the JS never sees: no runner_pid, exit 0.
func TestDetachOfARunnerThatCannotStart(t *testing.T) {
	p := testProject(t, "brief")
	d := newDetachRun(t)
	rt := d.runtime("start")
	rt.Self = filepath.Join(d.log, "absent")
	code, err := detach(t, p, rt)
	if err != nil || code != 0 {
		t.Fatalf("Detach = %d, %v", code, err)
	}
	want := `{"lane":"b","log_path":"` + filepath.Join(p.LogsDir, "b-0123abcd.log") + `","run_id":"` + detachRunID + `","keep_awake":false}` + "\n"
	if d.stdout.String() != want {
		t.Errorf("stdout %q, want %q", d.stdout.String(), want)
	}
}

// A start failure Node throws synchronously (here ENOTDIR: the runner's path goes through a
// regular file) is the exception the JS dies of: exit 1, nothing on stdout.
func TestDetachCrashesOnAStartFailureNodeThrows(t *testing.T) {
	p := testProject(t, "brief")
	d := newDetachRun(t)
	rt := d.runtime("start")
	rt.Self = filepath.Join(d.runner, "x")
	code, err := detach(t, p, rt)
	var notStarted *procs.StartError
	if code != 1 || !errors.As(err, &notStarted) || notStarted.Code != "" || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("Detach = %d, %v; want exit 1 with an uncoded ENOTDIR start error", code, err)
	}
	if d.stdout.String() != "" {
		t.Errorf("stdout %q", d.stdout.String())
	}
}

// flip.mjs relies on a live holder being refused synchronously: the lane-locked JSON without a
// hint on stderr (alive is the holder's, true), nothing on stdout, exit 3, no log, lock kept.
// The holder is a helper process holding the lane's kernel lock.
func TestDetachRefusesALockHeldByALiveProcess(t *testing.T) {
	for _, c := range []struct{ state, previous string }{
		{"", `null`},
		{`{"status":"running","pid":0,"extra":1}`, `{"status":"running","pid":0,"alive":true}`},
		{`0`, `0`},
	} {
		p := testProject(t, "brief")
		holder := strconv.Itoa(holdLock(t, p.LockPath("b"), false).pid())
		if c.state != "" {
			writeFile(t, p.StatePath("b"), c.state)
		}
		d := newDetachRun(t)
		code, err := detach(t, p, d.runtime("start"))
		if err != nil || code != ExitLaneLocked {
			t.Fatalf("Detach = %d, %v", code, err)
		}
		want := `{"error":"lane locked","lane":"b","previous":` + c.previous + `}` + "\n"
		if d.stderr.String() != want || d.stdout.String() != "" {
			t.Errorf("state %s: stderr %q, stdout %q; want stderr %q", c.state, d.stderr.String(), d.stdout.String(), want)
		}
		if got := readText(t, p.LockPath("b")); got != holder {
			t.Errorf("the lock was changed to %q", got)
		}
		if _, err := os.Stat(p.LogsDir); err == nil {
			t.Error("a refused launch created the logs directory")
		}
	}
}

// PE-29: a lock no process holds, whatever its text, does not refuse the launch, and the
// launcher leaves it for the runner's claim (C9).
func TestDetachLeavesAStaleLockToItsRunner(t *testing.T) {
	dead := strconv.Itoa(int(deadPid(t)))
	for _, holder := range []string{dead, "abc", "", "0", "-0", "1.5", strconv.Itoa(os.Getpid())} {
		p := testProject(t, "brief")
		writeFile(t, p.LockPath("b"), holder)
		d := newDetachRun(t)
		rt := d.runtime("start")
		rt.Self = filepath.Join(d.log, "absent") // nothing to reap
		if code, err := detach(t, p, rt); err != nil || code != 0 {
			t.Fatalf("holder %q: Detach = %d, %v", holder, code, err)
		}
		if got := readText(t, p.LockPath("b")); got != holder {
			t.Errorf("holder %q: the launcher changed the lock to %q", holder, got)
		}
	}
}

// L8, R2: after a runner is killed with SIGKILL, a detached launch starts a runner (a helper
// process) that reclaims the lane, runs and removes the lock.
func TestDetachedRunnerReclaimsAKilledRunnersLock(t *testing.T) {
	p := testProject(t, "brief")
	killed := holdLock(t, p.LockPath("b"), false)
	killed.killHard(t)
	newFakeRun(t, map[string]string{"FAKE_STDOUT": okResult}) // the runner's bridge and HOME
	t.Setenv("WORKER_BRIDGE", bridgeScript(t))
	d := newDetachRun(t)
	writeFile(t, d.runner, "#!/bin/sh\nWORKER_HELPER=run exec '"+os.Args[0]+"' -test.run='^TestHelperProcess$' -- \"$@\"\n")
	code, err := detach(t, p, d.runtime("start", "--brief", "b.md", "--harness", "claude", "--detach"))
	if err != nil || code != 0 {
		t.Fatalf("Detach = %d, %v; stderr %q", code, err, d.stderr.String())
	}
	match := launchedPid.FindStringSubmatch(d.stdout.String())
	if match == nil {
		t.Fatalf("launch %q has no runner_pid", d.stdout.String())
	}
	runner, _ := strconv.Atoi(match[1])
	reapRunner(t, runner)
	state := stateFile(t, p, "b")
	if !strings.Contains(state, `"status": "finished"`) || !strings.Contains(state, `"pid": `+match[1]+`,`) {
		t.Errorf("state of runner %d:\n%s\nlog:\n%s", runner, state, readText(t, filepath.Join(p.LogsDir, "b-0123abcd.log")))
	}
	requireNoLock(t, p, "b")
}

// PE-29: a lock path that is no file fails the launch with exit 1, as the JS's EISDIR does,
// but with the probe's error.
func TestDetachLockErrorsFailTheLaunch(t *testing.T) {
	p := testProject(t, "brief")
	if err := os.MkdirAll(p.LockPath("b"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDetachRun(t)
	if code, err := detach(t, p, d.runtime("start")); code != 1 || err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("Detach = %d, %v; want the probe's error", code, err)
	}
	if d.stdout.String() != "" {
		t.Errorf("stdout %q", d.stdout.String())
	}
}

func TestDetachRefusesAnExistingLog(t *testing.T) {
	p := testProject(t, "brief")
	logPath := filepath.Join(p.LogsDir, "b-0123abcd.log")
	writeFile(t, logPath, "earlier run")
	d := newDetachRun(t)
	if code, err := detach(t, p, d.runtime("start")); code != 1 || err == nil || err.Error() != "EEXIST: file already exists, open '"+logPath+"'" {
		t.Errorf("Detach = %d, %v; want EEXIST", code, err)
	}
	if d.stdout.String() != "" {
		t.Errorf("stdout %q", d.stdout.String())
	}
}

func TestRunnerArgsDropEveryDetachToken(t *testing.T) {
	for _, c := range []struct{ argv, want []string }{
		{[]string{"start", "--brief", "b.md", "--detach"}, []string{"start", "--brief", "b.md"}},
		{[]string{"--detach", "start", "--detach", "--harness", "claude", "--detach"}, []string{"start", "--harness", "claude"}},
		// --detach as --lane's value: the runner's --lane takes the next token instead.
		{[]string{"start", "--lane", "--detach", "--harness", "claude"}, []string{"start", "--lane", "--harness", "claude"}},
		// Only exact tokens: these are other values.
		{[]string{"start", "--detach=1", "--lane", "x--detach", "-detach"}, []string{"start", "--detach=1", "--lane", "x--detach", "-detach"}},
		// An earlier --log-path, --run-id or --cwd stays; the appended ones come last and win.
		{[]string{"start", "--run-id", "mine", "--log-path", "/tmp/x"}, []string{"start", "--run-id", "mine", "--log-path", "/tmp/x"}},
		{[]string{"start", "--cwd", "sub"}, []string{"start", "--cwd", "sub"}},
	} {
		want := append(append([]string{"worker"}, c.want...), "--log-path", "/l/b-1.log", "--run-id", "r", "--cwd", "/p")
		if got := runnerArgs(c.argv, "/p", "/l/b-1.log", "r"); !slices.Equal(got, want) {
			t.Errorf("runnerArgs(%q) = %q, want %q", c.argv, got, want)
		}
	}
}

// The runner parses its arguments as `orch worker` does, so its last --cwd, the root, wins.
func TestRunnerArgsRunInTheProjectRoot(t *testing.T) {
	args := runnerArgs([]string{"start", "--cwd", "sub", "--detach"}, "/repo/sub", "/l", "r")
	opts, _ := cliargs.ParseArgs(args[1:], []string{"cwd", "log-path", "run-id"}, []string{"detach"})
	if cwd, _ := opts.Value("cwd"); cwd != "/repo/sub" {
		t.Errorf("the runner's --cwd is %q, want the root", cwd)
	}
}

// PE-21: a launch creates logs/ and a run creates workers/ owner-only, whatever the umask.
func TestWorkerDirectoriesAreOwnerOnly(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0o022))
	p := testProject(t, "brief")
	d := newDetachRun(t)
	if code, err := detach(t, p, d.runtime("start", "--brief", "b.md", "--harness", "claude", "--detach")); err != nil || code != 0 {
		t.Fatalf("Detach = %d, %v", code, err)
	}
	match := launchedPid.FindStringSubmatch(d.stdout.String())
	if match == nil {
		t.Fatalf("stdout %q has no runner_pid", d.stdout.String())
	}
	pid, _ := strconv.Atoi(match[1])
	reapRunner(t, pid)
	f := newFakeRun(t, map[string]string{"FAKE_STDOUT": okResult})
	if code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "claude"); err != nil || code != 0 {
		t.Fatalf("Foreground = %d, %v", code, err)
	}
	for _, dir := range []string{p.LogsDir, p.WorkersDir} {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v (%v), want 0700", dir, info.Mode(), err)
		}
	}
}

// KeepAwakeEnabled is `loadRegistry().runtime?.keep_awake !== false`, on for any registry error.
func TestKeepAwakeEnabledReadsTheRegistry(t *testing.T) {
	for _, c := range []struct {
		name, registry string // "" registry: no file
		want           bool
	}{
		{"false", `{"runtime":{"keep_awake":false}}`, false},
		{"true", `{"runtime":{"keep_awake":true}}`, true},
		{"absent", `{"runtime":{}}`, true},
		{"no runtime", `{}`, true},
		{"runtime null", `{"runtime":null}`, true},
		{"runtime a string", `{"runtime":"x"}`, true},
		{"string false", `{"runtime":{"keep_awake":"false"}}`, true},
		{"zero", `{"runtime":{"keep_awake":0}}`, true},
		{"registry null", `null`, true},
		{"registry an array", `[]`, true},
		{"unparsable", `{`, true},
		{"unreadable", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "models.json")
			if c.registry != "" {
				writeFile(t, file, c.registry)
			}
			env := map[string]string{"HOME": dir, "ORCH_REGISTRY": file}
			if got := KeepAwakeEnabled(func(key string) string { return env[key] }); got != c.want {
				t.Errorf("KeepAwakeEnabled = %v, want %v", got, c.want)
			}
		})
	}
}
