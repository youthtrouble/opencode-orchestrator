package procs

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/proctest"
)

func TestRunSyncCapturesBothOutputsAndTheExitStatus(t *testing.T) {
	for _, want := range []int{0, 3} {
		child := script(t, `echo "out $1"; echo "err $(pwd -P)" >&2; read line; echo "stdin <$line>"; exit `+strconv.Itoa(want))
		dir := t.TempDir()
		got := RunSync(SyncSpec{Name: child, Args: []string{"a b"}, Dir: dir, Grace: testGrace})
		wantCode(t, got.Status, intp(want))
		physicalDir, _ := filepath.EvalSymlinks(dir)
		if got.Stdout != "out a b\nstdin <>\n" || got.Stderr != "err "+physicalDir+"\n" {
			t.Errorf("outputs %q, %q", got.Stdout, got.Stderr)
		}
		if got.TimedOut || got.Err != nil {
			t.Errorf("TimedOut %v, Err %v", got.TimedOut, got.Err)
		}
	}
}

func TestRunSyncOfASignalledChildHasNoStatus(t *testing.T) {
	got := RunSync(SyncSpec{Name: script(t, "kill -TERM $$"), Grace: testGrace})
	wantCode(t, got.Status, nil)
	if got.TimedOut || got.Err != nil {
		t.Errorf("TimedOut %v, Err %v", got.TimedOut, got.Err)
	}
}

// TestRunSyncTimeoutSendsTerm: at the deadline the child gets SIGTERM, which kills it, so the
// result has no status and is marked timed out, well before the child would have finished.
func TestRunSyncTimeoutSendsTerm(t *testing.T) {
	started := time.Now()
	got := RunSync(SyncSpec{Name: "/bin/sleep", Args: []string{"5"}, Timeout: 200 * time.Millisecond, Grace: killGrace})
	if elapsed := time.Since(started); elapsed > killGrace+slack/2 {
		t.Errorf("RunSync took %v", elapsed)
	}
	wantCode(t, got.Status, nil)
	if !got.TimedOut || got.Err != nil {
		t.Errorf("TimedOut %v, Err %v", got.TimedOut, got.Err)
	}
}

// trapTimeout is the timeout of children that must have installed a SIGTERM trap before it
// fires. RunSync arms it at the spawn, so it is generous: a fresh shell under parallel -race
// load can take over a second to reach its trap (S8c-1 review F1).
const trapTimeout = slack

// runTrapping runs a script whose body first installs a SIGTERM trap and then runs forever,
// and fails the test when the timeout's SIGTERM arrived before that trap was in place: the
// script records readiness in a file right after its trap.
func runTrapping(t *testing.T, trap, forever string) SyncResult {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	got := RunSync(SyncSpec{Name: script(t, trap+`; : > "$1"; `+forever), Args: []string{ready}, Timeout: trapTimeout, Grace: killGrace})
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("the child had not installed its trap when the timeout fired: %v (Err %v, status %s, TimedOut %v, stderr %q)",
			err, got.Err, show(got.Status), got.TimedOut, got.Stderr)
	}
	return got
}

// TestRunSyncTimeoutKeepsTheStatusOfAChildThatHandlesTerm: Node keeps the exit status of a
// child that exits on its own after the timeout's SIGTERM (probed: status 3, ETIMEDOUT).
func TestRunSyncTimeoutKeepsTheStatusOfAChildThatHandlesTerm(t *testing.T) {
	t.Parallel()
	got := runTrapping(t, "trap 'exit 3' TERM", "/bin/sleep 15 & wait")
	wantCode(t, got.Status, intp(3))
	if !got.TimedOut {
		t.Error("not marked timed out")
	}
}

// TestRunSyncKillsAChildThatIgnoresTerm: PE-10 ext., SIGKILL one Grace after the SIGTERM.
func TestRunSyncKillsAChildThatIgnoresTerm(t *testing.T) {
	t.Parallel()
	started := time.Now()
	got := runTrapping(t, "trap '' TERM", "while :; do /bin/sleep 1; done")
	if elapsed := time.Since(started); elapsed < trapTimeout+killGrace || elapsed > trapTimeout+killGrace+slack {
		t.Errorf("RunSync took %v, want the deadline plus the grace", elapsed)
	}
	wantCode(t, got.Status, nil)
	if !got.TimedOut {
		t.Error("not marked timed out")
	}
}

// TestRunSyncBoundsTheWaitForAGrandchildHoldingStdout: PE-10 ext., the child exits and a
// grandchild keeps its stdout open; RunSync returns one Grace later with the output so far.
func TestRunSyncBoundsTheWaitForAGrandchildHoldingStdout(t *testing.T) {
	started := time.Now()
	got := RunSync(SyncSpec{Name: script(t, "/bin/sleep 30 & echo $!; exit 0"), Grace: killGrace})
	if elapsed := time.Since(started); elapsed > killGrace+slack {
		t.Errorf("RunSync took %v", elapsed)
	}
	if grandchild, err := strconv.Atoi(strings.TrimSpace(got.Stdout)); err == nil {
		t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })
	} else {
		t.Errorf("stdout %q, not the grandchild's pid", got.Stdout)
	}
	wantCode(t, got.Status, intp(0))
	if got.Err != nil {
		t.Errorf("Err %v", got.Err)
	}
}

func TestRunSyncOfAMissingCommandIsAStartError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := RunSync(SyncSpec{Name: "no-such-command", Grace: testGrace})
	var startErr *StartError
	if !errors.As(got.Err, &startErr) || startErr.Code != "ENOENT" {
		t.Fatalf("Err = %v, want the ENOENT StartError", got.Err)
	}
	wantCode(t, got.Status, nil)
	if got.Stdout != "" || got.Stderr != "" || got.TimedOut {
		t.Errorf("result %+v", got)
	}
}

// TestStartReapedLeavesNoZombie: the child exits at once; the goroutine reaps it, so the pid
// disappears (a zombie would still answer kill(pid, 0)).
func TestStartReapedLeavesNoZombie(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report")
	pid, err := Detached{Name: script(t, `echo $$ > "$1"`), Args: []string{report}}.StartReaped()
	if err != nil {
		t.Fatal(err)
	}
	if reported := waitForPid(t, report); reported != pid {
		t.Fatalf("child reported %d, want %d", reported, pid)
	}
	waitGone(t, pid)
	assertGone(t, pid)
}

func TestStartReapedReturnsAStartFailure(t *testing.T) {
	_, err := Detached{Name: filepath.Join(t.TempDir(), "missing")}.StartReaped()
	var startErr *StartError
	if !errors.As(err, &startErr) || startErr.Code != "ENOENT" {
		t.Fatalf("err = %v, want the ENOENT StartError", err)
	}
}

// TestADeadlinePastTheReapIsNoTimeout: a deadline that fires once the child has exited and
// been reaped signals nothing, so the run is not marked timed out (S8b review L1).
func TestADeadlinePastTheReapIsNoTimeout(t *testing.T) {
	child, err := Start(Spec{Name: script(t, "exit 0"), Grace: testGrace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	deadline := startTimeout(child, time.Nanosecond)
	<-deadline.done
	if deadline.stop() {
		t.Error("a deadline after the reap reported a timeout")
	}
}

// runOverflowing runs a child that passes NodeMaxBuffer and checks what spawnSync's ENOBUFS
// gives (PE-20): the child is stopped, so it has no status, the result is marked overflowed,
// not timed out, and exactly NodeMaxBuffer bytes of output are kept over both streams.
func runOverflowing(t *testing.T, body string, grace time.Duration) SyncResult {
	t.Helper()
	started := time.Now()
	got := RunSync(SyncSpec{Name: script(t, body), Grace: grace})
	if elapsed := time.Since(started); elapsed > grace+slack {
		t.Errorf("RunSync took %v", elapsed)
	}
	wantCode(t, got.Status, nil)
	if !got.Overflowed || got.TimedOut || got.Err != nil {
		t.Errorf("Overflowed %v, TimedOut %v, Err %v", got.Overflowed, got.TimedOut, got.Err)
	}
	if kept := len(got.Stdout) + len(got.Stderr); kept != NodeMaxBuffer {
		t.Errorf("kept %d bytes, want %d", kept, NodeMaxBuffer)
	}
	return got
}

func TestRunSyncStopsAChildWhoseStdoutPassesNodeMaxBuffer(t *testing.T) {
	t.Parallel()
	got := runOverflowing(t, "exec cat /dev/zero", testGrace)
	if len(got.Stdout) != NodeMaxBuffer {
		t.Errorf("stdout kept %d bytes", len(got.Stdout))
	}
}

func TestRunSyncStopsAChildWhoseStderrPassesNodeMaxBuffer(t *testing.T) {
	t.Parallel()
	got := runOverflowing(t, "exec cat /dev/zero >&2", testGrace)
	if len(got.Stderr) != NodeMaxBuffer {
		t.Errorf("stderr kept %d bytes", len(got.Stderr))
	}
}

// TestRunSyncCountsNodeMaxBufferOverBothStreams: 600,000 bytes on each stream overflow, as in
// Node 26.
func TestRunSyncCountsNodeMaxBufferOverBothStreams(t *testing.T) {
	t.Parallel()
	runOverflowing(t, "head -c 600000 /dev/zero >&2; head -c 600000 /dev/zero; exec /bin/sleep 30", testGrace)
}

// TestRunSyncKillsAnOverflowingChildThatIgnoresTerm: PE-20, the SIGTERM is escalated to
// SIGKILL one Grace later (PE-10).
func TestRunSyncKillsAnOverflowingChildThatIgnoresTerm(t *testing.T) {
	t.Parallel()
	runOverflowing(t, "trap '' TERM; exec cat /dev/zero", killGrace)
}

func TestRunSyncKeepsExactlyNodeMaxBuffer(t *testing.T) {
	t.Parallel()
	got := RunSync(SyncSpec{Name: script(t, "head -c 524288 /dev/zero >&2; head -c 524288 /dev/zero"), Grace: testGrace})
	wantCode(t, got.Status, intp(0))
	if got.Overflowed || got.Err != nil {
		t.Errorf("Overflowed %v, Err %v", got.Overflowed, got.Err)
	}
	if len(got.Stdout) != NodeMaxBuffer/2 || len(got.Stderr) != NodeMaxBuffer/2 {
		t.Errorf("kept %d and %d bytes", len(got.Stdout), len(got.Stderr))
	}
}

// detachReportEnv names the file TestHelperStartsADetachedSleep reports its sleep's pid to;
// without it the helper is skipped.
const detachReportEnv = "ORCH_PROCS_TEST_DETACH_REPORT"

// TestHelperStartsADetachedSleep is not a test: a RunSync child runs the test binary as this
// helper to start a sleep in a session of its own, as `orch worker start --detach` starts a
// worker runner.
func TestHelperStartsADetachedSleep(t *testing.T) {
	report := os.Getenv(detachReportEnv)
	if report == "" {
		t.Skip("run only as a RunSync child's helper")
	}
	pid, err := Detached{Name: "/bin/sleep", Args: []string{"30"}}.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// PE-22: the timeout stops the child's group, so a grandchild that ignores SIGTERM is gone once
// the child has exited (here on the SIGTERM itself, so before any escalation), while a
// descendant in a session of its own keeps running.
func TestRunSyncTimeoutStopsTheGroupButNotADetachedDescendant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	grouped, detached := filepath.Join(dir, "grouped"), filepath.Join(dir, "detached")
	startGrouped := `/bin/sh -c 'trap "" TERM; echo $$ > "$0.tmp"; mv "$0.tmp" "$0"; exec /bin/sleep 30' '` + grouped + `' </dev/null >/dev/null 2>&1 &`
	startDetached := detachReportEnv + `='` + detached + `' '` + os.Args[0] + `' -test.run='^TestHelperStartsADetachedSleep$' >/dev/null`
	untilGrouped := `until [ -e '` + grouped + `' ]; do /bin/sleep 0.01; done`
	got := runTrapping(t, "trap 'exit 3' TERM; "+startGrouped+" "+startDetached+"; "+untilGrouped, "/bin/sleep 15 & wait")
	groupedPid, detachedPid := waitForPid(t, grouped), waitForPid(t, detached)
	t.Cleanup(func() { _ = syscall.Kill(groupedPid, syscall.SIGKILL); _ = syscall.Kill(detachedPid, syscall.SIGKILL) })
	wantCode(t, got.Status, intp(3))
	if !got.TimedOut {
		t.Error("not marked timed out")
	}
	waitGone(t, groupedPid)
	if err := syscall.Kill(detachedPid, 0); err != nil {
		t.Errorf("the detached descendant is gone: kill(0) = %v", err)
	}
}

// The relay tests run the test binary as TestHelperRunsSyncAndReports, signal it, and look at
// how it ended and at what its RunSync child left behind.
const (
	// relayDirEnv names the directory of the helper's child script, "child", which is given
	// the directory as its argument; without it the helper is skipped.
	relayDirEnv = "ORCH_PROCS_TEST_RELAY_DIR"
	// relayOwnNotifyEnv, when set, makes the helper catch SIGTERM itself before RunSync.
	relayOwnNotifyEnv = "ORCH_PROCS_TEST_RELAY_OWN_NOTIFY"
)

// TestHelperRunsSyncAndReports is not a test: a relay test runs the test binary as this helper,
// which runs <dir>/child through RunSync and writes the status it returned, and what its own
// SIGTERM handler caught, to <dir>/result.
func TestHelperRunsSyncAndReports(t *testing.T) {
	dir := os.Getenv(relayDirEnv)
	if dir == "" {
		t.Skip("run only as a relay test's helper")
	}
	var own chan os.Signal
	if os.Getenv(relayOwnNotifyEnv) != "" {
		own = make(chan os.Signal, 1)
		signal.Notify(own, syscall.SIGTERM)
	}
	got := RunSync(SyncSpec{Name: filepath.Join(dir, "child"), Args: []string{dir}, Grace: killGrace})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	result := "status " + show(got.Status) + "\n"
	if own != nil {
		select {
		case sig := <-own:
			result += "caught " + sig.String() + "\n"
		case <-time.After(slack):
			result += "caught nothing\n"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "result"), []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
}

// relayChild is the helper's child: it starts a grandchild, records both pids, then waits for
// the grandchild, which (as a background job of a shell) ignores SIGINT. The grandchild reads
// the test's lifeline, so both end with the test whatever happened to the helper.
const relayChild = `/bin/cat "$1/lifeline" >/dev/null 2>&1 &
echo $! > "$1/grandchild.tmp"; mv "$1/grandchild.tmp" "$1/grandchild.pid"
echo $$ > "$1/child.tmp"; mv "$1/child.tmp" "$1/child.pid"
wait`

// relayHelper is a running TestHelperRunsSyncAndReports and the pids of its RunSync child and
// grandchild.
type relayHelper struct {
	dir               string
	pid               int
	exited            chan struct{} // closed once the helper is reaped
	state             *os.ProcessState
	output            bytes.Buffer
	child, grandchild int
	release           func() // ends a child's read of "$1/release"
}

// startRelayHelper starts the helper on a child running childBody, through wrapper (an argv
// that execs its arguments) when one is given, and returns once the child has recorded its
// pids. The helper, the test's own child, is killed at cleanup should it still run; its child
// and grandchild are never signalled by pid, since the helper may have reaped them and freed
// their pids, but end once the test cuts "$1/lifeline" and "$1/release" (proctest.Lifeline).
func startRelayHelper(t *testing.T, childBody string, ownNotify bool, wrapper ...string) *relayHelper {
	t.Helper()
	h := &relayHelper{dir: t.TempDir(), exited: make(chan struct{})}
	proctest.Lifeline(t, filepath.Join(h.dir, "lifeline"))
	h.release = proctest.Lifeline(t, filepath.Join(h.dir, "release"))
	writeExecutable(t, filepath.Join(h.dir, "child"), "#!/bin/sh\n"+childBody+"\n", 0o755)
	argv := slices.Concat(wrapper, []string{os.Args[0], "-test.run=^TestHelperRunsSyncAndReports$"})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), relayDirEnv+"="+h.dir)
	if ownNotify {
		cmd.Env = append(cmd.Env, relayOwnNotifyEnv+"=1")
	}
	cmd.Stdout, cmd.Stderr = &h.output, &h.output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.pid = cmd.Process.Pid
	go func() {
		_ = cmd.Wait() // the status is read from ProcessState
		h.state = cmd.ProcessState
		close(h.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-h.exited:
		default:
			_ = cmd.Process.Kill()
			<-h.exited
		}
	})
	h.grandchild = waitForPid(t, filepath.Join(h.dir, "grandchild.pid"))
	h.child = waitForPid(t, filepath.Join(h.dir, "child.pid"))
	return h
}

func (h *relayHelper) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(h.pid, sig); err != nil {
		t.Fatal(err)
	}
}

// wait returns how the helper ended, failing when it runs on past a relay's Grace and slack.
func (h *relayHelper) wait(t *testing.T) syscall.WaitStatus {
	t.Helper()
	select {
	case <-h.exited:
	case <-time.After(killGrace + slack):
		t.Fatalf("the helper still runs %v after the signal", killGrace+slack)
	}
	return h.state.Sys().(syscall.WaitStatus)
}

func (h *relayHelper) result(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(h.dir, "result"))
	if err != nil {
		t.Fatalf("the helper wrote no result (%v); output %q", err, h.output.String())
	}
	return string(content)
}

func wantSignalled(t *testing.T, status syscall.WaitStatus, sig syscall.Signal) {
	t.Helper()
	if !status.Signaled() || status.Signal() != sig {
		t.Errorf("the helper ended with wait status %#x, want killed by %v", status, sig)
	}
}

func wantExit0(t *testing.T, h *relayHelper, status syscall.WaitStatus) {
	t.Helper()
	if !status.Exited() || status.ExitStatus() != 0 {
		t.Errorf("the helper ended with wait status %#x, want exit 0; output %q", status, h.output.String())
	}
}

// Sol's sol-5 R1: Ctrl-C stops the child and its grandchild (which ignores SIGINT, so the
// leftover kill ends it), then orch ends by SIGINT, as node does.
func TestRunSyncRelaysSIGINTAndThenEndsByIt(t *testing.T) {
	t.Parallel()
	if signal.Ignored(syscall.SIGINT) {
		t.Skip("SIGINT is ignored in this test process, so the helper would inherit that")
	}
	h := startRelayHelper(t, relayChild, false)
	h.signal(t, syscall.SIGINT)
	wantSignalled(t, h.wait(t), syscall.SIGINT)
	waitGone(t, h.child)
	waitGone(t, h.grandchild)
}

// A child that ignores the relayed SIGTERM is killed one Grace later, and only then does orch
// end by SIGTERM.
func TestRunSyncKillsAChildIgnoringARelayedSIGTERMBeforeEndingByIt(t *testing.T) {
	t.Parallel()
	h := startRelayHelper(t, "trap '' TERM\n"+relayChild, false)
	sent := time.Now()
	h.signal(t, syscall.SIGTERM)
	status := h.wait(t)
	if elapsed := time.Since(sent); elapsed < killGrace {
		t.Errorf("the helper ended %v after SIGTERM, before the %v Grace", elapsed, killGrace)
	}
	wantSignalled(t, status, syscall.SIGTERM)
	waitGone(t, h.child)
	waitGone(t, h.grandchild)
}

// An orch that ignores SIGHUP (nohup) keeps ignoring it: nothing is relayed, so no escalation
// kills the child after a Grace, and the child ends on its own.
func TestRunSyncLeavesAnIgnoredSIGHUPIgnored(t *testing.T) {
	t.Parallel()
	untilReleased := `/bin/cat "$1/lifeline" >/dev/null 2>&1 &
echo $! > "$1/grandchild.tmp"; mv "$1/grandchild.tmp" "$1/grandchild.pid"
echo $$ > "$1/child.tmp"; mv "$1/child.tmp" "$1/child.pid"
/bin/cat "$1/release" >/dev/null
kill $!`
	h := startRelayHelper(t, untilReleased, false, "/bin/sh", "-c", `trap "" HUP; exec "$0" "$@"`)
	h.signal(t, syscall.SIGHUP)
	select {
	case <-h.exited:
		t.Fatalf("the helper ended on an ignored SIGHUP; output %q", h.output.String())
	case <-time.After(killGrace + 500*time.Millisecond):
	}
	h.release()
	wantExit0(t, h, h.wait(t))
	if got := h.result(t); got != "status 0\n" {
		t.Errorf("the helper's RunSync gave %q, want the child's own exit", got)
	}
	waitGone(t, h.grandchild)
}

// A process with its own handler for the signal survives: the child is stopped, RunSync
// returns, the handler has the signal, and the process goes on to its own exit.
func TestRunSyncLeavesTheSignalToAHandlerOfTheProcess(t *testing.T) {
	t.Parallel()
	h := startRelayHelper(t, relayChild, true)
	h.signal(t, syscall.SIGTERM)
	wantExit0(t, h, h.wait(t))
	if got := h.result(t); got != "status null\ncaught terminated\n" {
		t.Errorf("the helper reported %q, want a stopped child and a caught SIGTERM", got)
	}
	waitGone(t, h.child)
	waitGone(t, h.grandchild)
}
