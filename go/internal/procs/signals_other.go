//go:build !linux && !darwin

package procs

import (
	"os"
	"syscall"
)

// signalThisThread sends sig to orch's own pid, for want of a thread-directed signal that this
// package reaches here. The kernel may hand it to another thread, so raise can return before
// orch dies of it. Signalling its own pid cannot fail, so the error is dropped.
func signalThisThread(sig syscall.Signal) {
	_ = syscall.Kill(os.Getpid(), sig)
}
