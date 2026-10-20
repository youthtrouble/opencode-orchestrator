package procs

import (
	"runtime"
	"syscall"
)

// signalThisThread sends sig to the calling thread, so the kernel runs its handler on the way
// out of tgkill(2). A kill(2) of orch's own pid is process-directed instead: Linux may hand it
// to another thread, which the Go runtime wakes only later, so orch could run on, and even
// exit 0, before it dies of the signal. Signalling its own thread cannot fail, so the error is
// dropped.
func signalThisThread(sig syscall.Signal) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = syscall.Tgkill(syscall.Getpid(), syscall.Gettid(), sig)
}
