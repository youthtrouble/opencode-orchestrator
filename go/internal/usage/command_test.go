package usage

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/proctest"
)

// TestRunQuietFailsOnStdoutPastNodeMaxBuffer: execFileSync throws ENOBUFS once stdout passes
// 1 MiB, and the child is stopped rather than read forever (PE-20).
func TestRunQuietFailsOnStdoutPastNodeMaxBuffer(t *testing.T) {
	started := time.Now()
	out, err := runQuiet("/bin/sh", []string{"-c", "exec cat /dev/zero"}, time.Minute)
	if !errors.Is(err, errStdoutMaxBuffer) || out != "" {
		t.Errorf("runQuiet = %d bytes, %v; want the maxBuffer error", len(out), err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("runQuiet took %v", elapsed)
	}
}

func TestRunQuietReturnsExactlyNodeMaxBuffer(t *testing.T) {
	out, err := runQuiet("/bin/sh", []string{"-c", "head -c 1048576 /dev/zero"}, time.Minute)
	if err != nil || len(out) != procs.NodeMaxBuffer {
		t.Errorf("runQuiet = %d bytes, %v", len(out), err)
	}
}

// TestRunQuietCountsOnlyStdout: stderr is ignored, as with stdio ["ignore", "pipe", "ignore"].
func TestRunQuietCountsOnlyStdout(t *testing.T) {
	out, err := runQuiet("/bin/sh", []string{"-c", "head -c 2000000 /dev/zero >&2; echo ok"}, time.Minute)
	if err != nil || out != "ok\n" {
		t.Errorf("runQuiet = %q, %v", out, err)
	}
}

// runQuietRecordingAGrandchild runs body with /bin/sh -c, $0 a file in which body records the
// pid of a grandchild it starts and $1 the test's lifeline (proctest.Lifeline), and returns
// runQuiet's error with that pid. Every process body starts reads the lifeline instead of
// sleeping, so whatever runQuiet left running ends with the test, and no cleanup has to signal
// a pid that init may have reaped and handed on.
func runQuietRecordingAGrandchild(t *testing.T, body string, timeout time.Duration) (int, error) {
	t.Helper()
	dir := t.TempDir()
	pidFile, lifeline := filepath.Join(dir, "grandchild.pid"), filepath.Join(dir, "lifeline")
	proctest.Lifeline(t, lifeline)
	_, err := runQuiet("/bin/sh", []string{"-c", body, pidFile, lifeline}, timeout)
	content, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("the child recorded no grandchild: %v (runQuiet error %v)", readErr, err)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(content)))
	if convErr != nil {
		t.Fatalf("grandchild pid %q: %v", content, convErr)
	}
	return pid, err
}

// waitUntilGone polls until the grandchild has terminated: the leftover kill orphans it, and
// it counts as gone once dead, whether or not init has reaped it yet (proctest.Gone).
func waitUntilGone(t *testing.T, pid int) {
	t.Helper()
	proctest.WaitGone(t, pid, 5*time.Second)
}

// PE-22: the timeout stops the child's whole process group, not the child alone.
func TestRunQuietTimeoutStopsTheGrandchild(t *testing.T) {
	t.Parallel()
	pid, err := runQuietRecordingAGrandchild(t, `/bin/cat "$1" & echo $! > "$0"; /bin/cat "$1"`, 300*time.Millisecond)
	if !errors.Is(err, errTimedOut) {
		t.Errorf("runQuiet error %v, want the timeout", err)
	}
	waitUntilGone(t, pid)
}

func TestRunQuietOverflowStopsTheGrandchild(t *testing.T) {
	t.Parallel()
	pid, err := runQuietRecordingAGrandchild(t, `/bin/cat "$1" & echo $! > "$0"; exec cat /dev/zero`, time.Minute)
	if !errors.Is(err, errStdoutMaxBuffer) {
		t.Errorf("runQuiet error %v, want the maxBuffer error", err)
	}
	waitUntilGone(t, pid)
}

// A grandchild that ignores the timeout's SIGTERM is killed once the child has exited.
func TestRunQuietTimeoutStopsAGrandchildThatIgnoresTerm(t *testing.T) {
	t.Parallel()
	pid, err := runQuietRecordingAGrandchild(t, `(trap "" TERM; exec /bin/cat "$1") & echo $! > "$0"; /bin/cat "$1"`, 300*time.Millisecond)
	if !errors.Is(err, errTimedOut) {
		t.Errorf("runQuiet error %v, want the timeout", err)
	}
	waitUntilGone(t, pid)
}
