package procs

import (
	"syscall"
	_ "unsafe" // for go:linkname
)

// signalThisThread sends sig to the calling thread with libSystem's raise(3), which POSIX
// defines as pthread_kill(pthread_self(), sig) and which does not return until the handler
// has; Go's runtime raises its own signals on darwin the same way. A kill(2) of orch's own pid
// is process-directed instead: darwin hands it to a thread of its choosing, which may run the
// handler only after orch has run on, and even exited 0 (the macOS flake of the relay tests).
// raise fails only for an invalid signal, so its result is dropped.
func signalThisThread(sig syscall.Signal) {
	_, _, _ = rawSyscall(libcRaiseTrampolineAddr, uintptr(sig), 0, 0)
}

// libcRaiseTrampolineAddr is the address of the jump to raise(3) in signals_darwin.s. There is
// no cgo, so the linker binds raise in libSystem as it binds package syscall's own libc calls.
var libcRaiseTrampolineAddr uintptr

//go:cgo_import_dynamic libc_raise raise "/usr/lib/libSystem.B.dylib"

// rawSyscall calls the libSystem function at fn, as package syscall and golang.org/x/sys/unix
// do (runtime/sys_darwin.go), without entering the scheduler's syscall state: raise(3) does
// not block.
//
//go:linkname rawSyscall syscall.rawSyscall
func rawSyscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)
