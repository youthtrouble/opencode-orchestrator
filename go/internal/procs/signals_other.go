//go:build !linux

package procs

import (
	"os"
	"syscall"
)

// signalThisThread sends sig to orch's own pid, there being no tgkill(2) outside Linux. On
// darwin the handler has run before kill(2) returns in every run of
// TestRaiseEndsTheProcessBeforeItReturns. Signalling its own pid cannot fail, so the error is
// dropped.
func signalThisThread(sig syscall.Signal) {
	_ = syscall.Kill(os.Getpid(), sig)
}
