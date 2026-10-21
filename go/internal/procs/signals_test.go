package procs

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
)

// raiseThenExitEnv, when set, makes TestHelperRaisesThenExits run; without it the helper is
// skipped.
const raiseThenExitEnv = "ORCH_PROCS_TEST_RAISE_THEN_EXIT"

// raiseReturned is the line TestHelperRaisesThenExits prints once raise has returned.
const raiseReturned = "raise returned"

// TestHelperRaisesThenExits is not a test: TestRaiseEndsTheProcessBeforeItReturns runs the test
// binary as this helper, which raises SIGINT, then prints raiseReturned and exits 0 at once, as
// a caller of RunSync that returns from a relayed signal may. Like such a caller, it raises
// from a thread other than its own: this goroutine keeps its thread while another one raises.
// With darwin's process-directed kill(2), a raise from this goroutine's thread passed in every
// run tried, and one from another thread failed in every one.
func TestHelperRaisesThenExits(t *testing.T) {
	if os.Getenv(raiseThenExitEnv) == "" {
		t.Skip("run only as TestRaiseEndsTheProcessBeforeItReturns's helper")
	}
	runtime.LockOSThread()
	raised := make(chan struct{})
	go func() {
		raise(syscall.SIGINT)
		_, _ = os.Stdout.WriteString(raiseReturned + "\n") // should the write fail, the exit 0 still tells
		close(raised)
	}()
	<-raised
	os.Exit(0)
}

// A process with no handler of its own for the raised signal dies of it before raise returns,
// so whatever it would do next (here a line on stdout and an exit 0) never happens. A
// process-directed kill(2) of its own pid lets the kernel deliver the signal to another thread
// later, by which time the process may have exited 0 (the ubuntu flake of
// TestRunSyncRelaysSIGINTAndThenEndsByIt, and the macOS one of both relay tests that end by a
// signal). The line shows such a raise even in the runs where the signal still wins the race
// to the exit.
func TestRaiseEndsTheProcessBeforeItReturns(t *testing.T) {
	t.Parallel()
	if signal.Ignored(syscall.SIGINT) {
		t.Skip("SIGINT is ignored in this test process, so the helper would inherit that")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRaisesThenExits$")
	cmd.Env = append(os.Environ(), raiseThenExitEnv+"=1")
	output, _ := cmd.CombinedOutput() // the status is read from ProcessState
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGINT || bytes.Contains(output, []byte(raiseReturned)) {
		t.Errorf("the helper ended with wait status %#x, want killed by interrupt before raise returned; output %q", status, output)
	}
}
