//go:build parity

package parity

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

type workerCase struct {
	name      string
	args      []string
	seed      seed
	crash     crashClass
	errorLine string
	// victims, when set, are the processes the case's stop must interrupt: each runtime's
	// seed starts its own, and every one must have died of SIGINT once the pair has run.
	victims *victims
}

// workerStatePid is a stored pid in stop output and state files, and workerLockPid the whole
// text of a lock, rewritten for the cases whose pid is a victim started per runtime.
var (
	workerStatePid = regexp.MustCompile(`("pid": ?"?)\d+`)
	workerLockPid  = regexp.MustCompile(`^\d+$`)
)

func normalizeVictimPid(s string) string {
	return workerLockPid.ReplaceAllString(workerStatePid.ReplaceAllString(s, "${1}<pid>"), "<pid>")
}

func (h *harness) workerInvocation(c workerCase) invocation {
	inv := invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "worker.mjs")}, c.args...),
		orch:         append([]string{h.orchBin, "worker"}, c.args...),
		env:          []string{noPostRunRefresh},
		seed:         c.seed,
		crash:        c.crash,
		errorLine:    c.errorLine,
		projectPaths: true,
	}
	if c.victims != nil {
		inv.normalize = normalizeVictimPid
	}
	return inv
}

// seedWorkerState writes the lane's state file as given.
func seedWorkerState(lane, content string) seed {
	return seedProjectFile(".orchestrator/workers/"+lane+".json", content)
}

// seedWorkerLock writes a lock file naming pid 1 that no process holds: a lock that is
// present, which only status looks at, or stale (CA2: pid text alone never models a live
// holder).
func seedWorkerLock(lane string) seed {
	return seedWorkerLockHolding(lane, "1")
}

// seedWorkerLockHolding writes the lane's lock holding pid as text and no kernel lock: what a
// runner that died leaves.
func seedWorkerLockHolding(lane, pid string) seed {
	return seedProjectFile(workerLockPath(lane), pid)
}

func workerLockPath(lane string) string { return ".orchestrator/workers/" + lane + ".lock" }

// seedHeldLock has a helper process hold the kernel lock of the project file rel with its own
// pid, as a running runner holds its lane, until the test ends (CA2). The JS sees a live pid in
// the text, orch the kernel's lock.
func seedHeldLock(rel string) seed {
	return func(t *testing.T, sb sandbox) { startLockHolder(t, filepath.Join(sb.project, rel), false) }
}

// TestParityLockHolder is not a test: re-executed by startLockHolder, it holds the kernel lock
// of $PARITY_LOCK_HOLDER with its own pid, prints "locked" and keeps the lock until its stdin
// closes.
func TestParityLockHolder(t *testing.T) {
	path := os.Getenv("PARITY_LOCK_HOLDER")
	if path == "" {
		return
	}
	lock, err := ownlock.Acquire(path, os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// lockHolders are the pids, as text, of the holders startLockHolder started.
var lockHolders sync.Map

// startLockHolder starts a holder of path, in a session of its own when leader is set, and
// returns it once it holds the lock. A holder still running when the test ends is killed: it is
// the test's own child.
func startLockHolder(t *testing.T, path string, leader bool) *exec.Cmd {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	cmd := exec.Command(os.Args[0], "-test.run=^TestParityLockHolder$")
	cmd.Env = append(os.Environ(), "PARITY_LOCK_HOLDER="+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: leader}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if _, err := cmd.StdinPipe(); err != nil { // kept open by cmd until Wait
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line := make(chan string, 1)
	go func() {
		first, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- first
	}()
	select {
	case got := <-line:
		if got != "locked\n" {
			t.Fatalf("lock holder of %s printed %q; stderr: %s", path, got, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("lock holder of %s did not lock", path)
	}
	lockHolders.Store(strconv.Itoa(cmd.Process.Pid), true)
	return cmd
}

// withLockHolderPids rewrites a lock whose text is a holder's pid, which differs per runtime,
// to "<holder>".
func withLockHolderPids(o outcome) outcome {
	for name, content := range o.files {
		if _, held := lockHolders.Load(content); held && strings.HasSuffix(name, ".lock") {
			o.files[name] = "<holder>"
		}
	}
	return o
}

func seedProjectDir(rel string) seed {
	return func(t *testing.T, sb sandbox) { mustMkdir(t, filepath.Join(sb.project, rel)) }
}

// testWorker compares `orch worker start|status|stop` and its usage errors with bin/worker.mjs.
// Status only sends signal 0, so any pid is safe there; stop signals only victims the case
// started itself. Stop of the pids the JS would turn into kill(-1) (PE-14), and of a live pid
// without its lock (PE-23), runs orch only.
func (h *harness) testWorker(t *testing.T) {
	live := strconv.Itoa(os.Getpid())
	dead := strconv.Itoa(reapedPid(t))
	pidState := func(pid string) string { return `{"lane":"w","status":"running","pid":` + pid + `}` }
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`

	cases := []workerCase{
		{name: "status/empty-project", args: []string{"status"}, seed: seedProjectDir(".orchestrator")},
		{name: "status/empty-workers", args: []string{"status"}, seed: seedProjectDir(".orchestrator/workers")},
		{name: "status/lane-order", args: []string{"status"}, seed: seeds(
			seedWorkerState("b", `{"lane":"b"}`), seedWorkerState("a", `{"lane":"a"}`), seedWorkerState("1", `{"lane":"1"}`),
			seedWorkerState("10", `{"lane":"10"}`), seedWorkerState("2", `{"lane":"2"}`), seedProjectFile(".orchestrator/workers/notes.txt", ""),
			seedProjectFile(".orchestrator/workers/c.json.tmp", "{}"))},
		{name: "status/live-pid", args: []string{"status"}, seed: seeds(seedWorkerState("w", pidState(live)), seedWorkerLock("w"))},
		{name: "status/dead-pid", args: []string{"status"}, seed: seedWorkerState("w", pidState(dead))},
		{name: "status/live-pid-string", args: []string{"status"}, seed: seedWorkerState("w", pidState(`"`+live+`"`))},
		{name: "status/pid-true", args: []string{"status"}, seed: seedWorkerState("w", pidState(`true`))},
		{name: "status/pid-minus-one", args: []string{"status"}, seed: seedWorkerState("w", pidState(`-1`))},
		{name: "status/pid-fraction", args: []string{"status"}, seed: seedWorkerState("w", pidState(`1.5`))},
		{name: "status/pid-array", args: []string{"status"}, seed: seedWorkerState("w", pidState(`[`+live+`]`))},
		{name: "status/pid-object", args: []string{"status"}, seed: seedWorkerState("w", pidState(`{}`))},
		{name: "status/pid-beyond-int32", args: []string{"status"}, seed: seedWorkerState("w", pidState(`3000000000`))},
		{name: "status/pid-missing", args: []string{"status"}, seed: seedWorkerState("w", `{"lane":"w","status":"finished"}`)},
		{name: "status/lock-without-state", args: []string{"status", "--lane", "w"}, seed: seedWorkerLock("w")},
		{name: "status/unparsable", args: []string{"status"}, seed: seedWorkerState("w", `{"lane":`)},
		{name: "status/null", args: []string{"status"}, seed: seedWorkerState("w", `null`)},
		{name: "status/zero", args: []string{"status"}, seed: seedWorkerState("w", `0`)},
		{name: "status/empty-string", args: []string{"status"}, seed: seedWorkerState("w", `""`)},
		{name: "status/string", args: []string{"status"}, seed: seedWorkerState("w", `"abc"`)},
		{name: "status/number", args: []string{"status"}, seed: seedWorkerState("w", `5`)},
		{name: "status/array", args: []string{"status"}, seed: seedWorkerState("w", `[1,2]`)},
		{name: "status/own-output-keys", args: []string{"status"}, seed: seeds(seedWorkerState("w", `{"process_alive":"x","lane":"w","lock_present":"y","pid":`+live+`,"z":1}`), seedWorkerLock("w"))},
		{name: "status/directory-state", args: []string{"status"}, seed: seedProjectDir(".orchestrator/workers/x.json")},
		{name: "status/lane-with-file", args: []string{"status", "--lane", "w"}, seed: seeds(seedWorkerState("w", pidState(live)), seedWorkerState("other", `{}`))},
		{name: "status/lane-without-file", args: []string{"status", "--lane", "x"}, seed: seedWorkerState("w", `{}`)},
		{name: "status/lane-empty", args: []string{"status", "--lane", ""}, seed: seedWorkerState("w", `{}`)},
		{name: "status/lane-bare", args: []string{"status", "--lane"}, seed: seeds(seedWorkerState("true", `{"status":"x"}`), seedWorkerLock("true"))},
		{name: "status/workers-is-file", args: []string{"status"}, seed: seedProjectFile(".orchestrator/workers", ""), crash: crashC1,
			errorLine: "Error: ENOTDIR: not a directory, scandir '$PROJECT/.orchestrator/workers'"},
		{name: "status/cwd-bare", args: []string{"status", "--cwd"}, crash: crashC1, errorLine: cwdBoolean},
		{name: "status/cwd-relative", args: []string{"status", "--cwd", "sub"}, seed: seedProjectFile("sub/.orchestrator/workers/s.json", `{"lane":"s"}`)},
		// Sol S2/R8: a lane with a slash is refused before anything is read; ".." stays a name
		// inside workers/ ("...json").
		{name: "status/lane-traversal", args: []string{"status", "--lane", "../flip"}, seed: seedProjectFile(".orchestrator/flip.json", `{"lane":"w","status":"running","pid":`+dead+`}`)},
		{name: "status/lane-absolute", args: []string{"status", "--lane", "/etc/hosts"}, seed: seedProjectDir(".orchestrator")},
		{name: "status/lane-dot-dot", args: []string{"status", "--lane", ".."}, seed: seedWorkerState("..", `{"lane":".."}`)},

		{name: "stop/no-state", args: []string{"stop", "--lane", "w"}, seed: seedProjectDir(".orchestrator")},
		{name: "stop/dead-pid", args: []string{"stop", "--lane", "w"}, seed: seedWorkerState("w", pidState(dead))},
		{name: "stop/falsy-state", args: []string{"stop", "--lane", "w"}, seed: seedWorkerState("w", `0`)},
		{name: "stop/lane-empty", args: []string{"stop", "--lane", ""}, seed: seedWorkerState("", pidState(dead))},
		{name: "stop/lane-traversal", args: []string{"stop", "--lane", "../flip"}, seed: seeds(seedProjectFile(".orchestrator/flip.json", `{"lane":"w","status":"running","pid":`+dead+`}`),
			seedProjectFile(".orchestrator/flip.lock", dead))},

		{name: "usage/no-command", args: nil},
		{name: "usage/unknown-command", args: []string{"frob", "--lane", "w"}},
		{name: "usage/start-without-brief", args: []string{"start", "--harness", "claude"}},
		{name: "usage/cwd-bare", args: []string{"frob", "--cwd"}, crash: crashC1, errorLine: cwdBoolean},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.workerInvocation(c)) })
	}
	for _, args := range [][]string{{"status"}, {"stop", "--lane", "w"}} {
		t.Run(args[0]+"/outside-project", func(t *testing.T) { h.compareOutsideProject(t, h.workerInvocation(workerCase{args: args}), "--cwd") })
	}
	h.testWorkerStopVictims(t)
	t.Run("stop/PE-14", h.testWorkerStopGuard)
	t.Run("stop/PE-23", h.testWorkerStopNeedsTheLock)
	t.Run("start", h.testWorkerStart)
	t.Run("detach", h.testWorkerDetach)
}

// testWorkerStopVictims stops live workers: a sleep that leads its own process group (the
// group is signalled) and one that does not (the group signal fails, the pid is signalled).
func (h *harness) testWorkerStopVictims(t *testing.T) {
	for _, c := range []struct {
		name   string
		args   []string
		lane   string
		leader bool
		quote  bool
	}{
		{name: "stop/group-leader", args: []string{"stop", "--lane", "w"}, lane: "w", leader: true},
		{name: "stop/not-group-leader", args: []string{"stop", "--lane", "w"}, lane: "w"},
		{name: "stop/pid-string", args: []string{"stop", "--lane", "w"}, lane: "w", leader: true, quote: true},
		{name: "stop/lane-missing", args: []string{"stop"}, lane: "undefined", leader: true},
		{name: "stop/lane-bare", args: []string{"stop", "--lane"}, lane: "true", leader: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := &victims{}
			seedVictim := func(t *testing.T, sb sandbox) {
				// PE-23: the running worker holds its lane's kernel lock.
				pid := strconv.Itoa(v.holdLock(t, filepath.Join(sb.project, workerLockPath(c.lane)), c.leader))
				if c.quote {
					pid = `"` + pid + `"`
				}
				seedWorkerState(c.lane, `{"lane":"w","status":"running","pid":`+pid+`}`)(t, sb)
			}
			h.comparePair(t, h.workerInvocation(workerCase{args: c.args, seed: seedVictim, victims: v}))
			v.requireInterrupted(t, 2)
		})
	}
}

// testWorkerStopGuard asserts PE-14 on orch alone. The JS is never run here: for pid 1, true,
// "1" and [1] it sends SIGINT to every process of the user, and for a negative pid to that
// process. The lock holds the pid as process.kill coerces it, so PE-23 alone would not refuse
// it. A victim started for the case must survive, and so must this test process.
func (h *harness) testWorkerStopGuard(t *testing.T) {
	for _, c := range []struct{ pid, lock string }{
		{`1`, "1"}, {`true`, "1"}, {`"1"`, "1"}, {`[1]`, "1"}, {`-1`, "-1"}, {`-12345`, "-12345"}, {`"0"`, "0"}, {`[]`, "0"},
		{`-<victim>`, "-<victim>"},
	} {
		t.Run(c.pid, func(t *testing.T) {
			v := &victims{}
			survivor := strconv.Itoa(v.start(t, true))
			sb := h.newSandbox(t)
			seedWorkerState("w", `{"lane":"w","status":"running","pid":`+strings.ReplaceAll(c.pid, "<victim>", survivor)+`}`)(t, sb)
			seedWorkerLockHolding("w", strings.ReplaceAll(c.lock, "<victim>", survivor))(t, sb)
			h.requireOrchStopsNothing(t, sb, v, "PE-14")
		})
	}
}

// testWorkerStopNeedsTheLock asserts PE-23 on orch alone: a live process recorded as the
// state's pid is not signalled unless the kernel says it holds the lane's lock. The JS is never
// run here: it would interrupt the stand-in, as it would an unrelated process that reused a
// finished or killed worker's pid.
func (h *harness) testWorkerStopNeedsTheLock(t *testing.T) {
	for _, c := range []struct {
		name string
		lock func(t *testing.T, sb sandbox, v *victims, standIn string) // nil: no lock
	}{
		{name: "no-lock"},
		{name: "lock-of-another-pid", lock: func(t *testing.T, sb sandbox, _ *victims, _ string) {
			seedWorkerLockHolding("w", strconv.Itoa(reapedPid(t)))(t, sb)
		}},
		// R3: the text names the state's pid, but no process holds the lock, as after the
		// runner was killed with SIGKILL and its pid reused.
		{name: "text-of-the-state-pid-unheld", lock: func(t *testing.T, sb sandbox, _ *victims, standIn string) {
			seedWorkerLockHolding("w", standIn)(t, sb)
		}},
		{name: "held-by-another-process", lock: func(t *testing.T, sb sandbox, v *victims, _ string) {
			v.holdLock(t, filepath.Join(sb.project, workerLockPath("w")), true)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := &victims{}
			standIn := strconv.Itoa(v.start(t, true))
			sb := h.newSandbox(t)
			seedWorkerState("w", `{"lane":"w","status":"running","pid":`+standIn+`}`)(t, sb)
			if c.lock != nil {
				c.lock(t, sb, v, standIn)
			}
			h.requireOrchStopsNothing(t, sb, v, "PE-23")
		})
	}
}

// requireOrchStopsNothing runs `orch worker stop --lane w` in sb and requires "no live worker"
// with every victim still running.
func (h *harness) requireOrchStopsNothing(t *testing.T, sb sandbox, v *victims, exception string) {
	t.Helper()
	o := h.run(t, sb, []string{h.orchBin, "worker", "stop", "--lane", "w"}, nil, []string{noPostRunRefresh})
	if o.exitCode != 0 || o.stdout != `{"lane":"w","stopped":false,"reason":"no live worker"}`+"\n" || o.stderr != "" {
		t.Errorf("orch: exit %d, stdout %q, stderr %q; want 0 and no live worker (%s)", o.exitCode, o.stdout, o.stderr, exception)
	}
	v.requireRunning(t)
}

// reapedPid is the pid of a child that has exited and been reaped: no process holds it (until
// the system reuses it).
func reapedPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// victims are sleep processes started for one case, killed when the test ends.
type victims struct{ cmds []*exec.Cmd }

// start runs /bin/sleep, in a new session (leading its own process group) when leader is set,
// and returns its pid.
func (v *victims) start(t *testing.T, leader bool) int {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: leader}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	v.cmds = append(v.cmds, cmd)
	return cmd.Process.Pid
}

// holdLock starts a victim that holds the kernel lock of path (see startLockHolder) and returns
// its pid.
func (v *victims) holdLock(t *testing.T, path string, leader bool) int {
	t.Helper()
	cmd := startLockHolder(t, path, leader)
	v.cmds = append(v.cmds, cmd)
	return cmd.Process.Pid
}

// requireInterrupted waits for every victim to exit and requires count of them, each killed
// by SIGINT.
func (v *victims) requireInterrupted(t *testing.T, count int) {
	t.Helper()
	if len(v.cmds) != count {
		t.Fatalf("%d victims started, want %d", len(v.cmds), count)
	}
	for _, cmd := range v.cmds {
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("victim %d was not stopped", cmd.Process.Pid)
		}
		status := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Errorf("victim %d ended with %v, want SIGINT", cmd.Process.Pid, cmd.ProcessState)
		}
	}
}

// requireRunning requires every victim to be still running (not exited, not a zombie).
func (v *victims) requireRunning(t *testing.T) {
	t.Helper()
	for _, cmd := range v.cmds {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
		if err != nil || pid != 0 {
			t.Errorf("victim %d is gone (wait4 %d, %v, %v)", cmd.Process.Pid, pid, status, err)
		}
	}
}
