package procs

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
// fires: a fresh script can take a few hundred milliseconds to start on a loaded machine.
const trapTimeout = 1500 * time.Millisecond

// TestRunSyncTimeoutKeepsTheStatusOfAChildThatHandlesTerm: Node keeps the exit status of a
// child that exits on its own after the timeout's SIGTERM (probed: status 3, ETIMEDOUT).
func TestRunSyncTimeoutKeepsTheStatusOfAChildThatHandlesTerm(t *testing.T) {
	got := RunSync(SyncSpec{Name: script(t, "trap 'exit 3' TERM; /bin/sleep 5 & wait"), Timeout: trapTimeout, Grace: killGrace})
	wantCode(t, got.Status, intp(3))
	if !got.TimedOut {
		t.Error("not marked timed out")
	}
}

// TestRunSyncKillsAChildThatIgnoresTerm: PE-10 ext., SIGKILL one Grace after the SIGTERM.
func TestRunSyncKillsAChildThatIgnoresTerm(t *testing.T) {
	started := time.Now()
	got := RunSync(SyncSpec{Name: script(t, "trap '' TERM; while :; do /bin/sleep 1; done"), Timeout: trapTimeout, Grace: killGrace})
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
	if reported := strings.TrimSpace(waitForFile(t, report)); reported != strconv.Itoa(pid) {
		t.Fatalf("child reported %q, want %d", reported, pid)
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
