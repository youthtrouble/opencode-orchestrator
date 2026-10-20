package procs

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
)

// raiseThenExitEnv, when set, makes TestHelperRaisesThenExits run; without it the helper is
// skipped.
const raiseThenExitEnv = "ORCH_PROCS_TEST_RAISE_THEN_EXIT"

// TestHelperRaisesThenExits is not a test: TestRaiseEndsTheProcessBeforeItReturns runs the test
// binary as this helper, which raises SIGINT and then exits 0 at once, as a caller of RunSync
// that returns from a relayed signal may.
func TestHelperRaisesThenExits(t *testing.T) {
	if os.Getenv(raiseThenExitEnv) == "" {
		t.Skip("run only as TestRaiseEndsTheProcessBeforeItReturns's helper")
	}
	raise(syscall.SIGINT)
	os.Exit(0)
}

// A process with no handler of its own for the raised signal dies of it before raise returns,
// so whatever it would do next (here an exit 0) never happens. A process-directed kill(2) of
// its own pid lets Linux deliver the signal to another thread later, by which time the process
// may have exited 0 (the ubuntu flake of TestRunSyncRelaysSIGINTAndThenEndsByIt).
func TestRaiseEndsTheProcessBeforeItReturns(t *testing.T) {
	t.Parallel()
	if signal.Ignored(syscall.SIGINT) {
		t.Skip("SIGINT is ignored in this test process, so the helper would inherit that")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRaisesThenExits$")
	cmd.Env = append(os.Environ(), raiseThenExitEnv+"=1")
	output, _ := cmd.CombinedOutput() // the status is read from ProcessState
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Errorf("the helper ended with wait status %#x, want killed by interrupt; output %q", status, output)
	}
}
