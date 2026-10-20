package procs

import (
	"errors"
	"syscall"
	"unsafe"
)

// pPID is waitid's P_PID: wait for the one child whose pid is given.
const pPID = 1

// waitExited blocks until the child pid has exited, without reaping it (WNOWAIT), so it keeps
// its pid, and with it its process group id, until Wait reaps it. This is os's own
// blockUntilWaitable (os/wait_waitid.go).
func waitExited(pid int) error {
	// The siginfo_t waitid fills in is 128 bytes on every Linux; nothing is read from it.
	var info [128]byte
	err := retryEINTR(func() error {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info)), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno != 0 {
			return errno
		}
		return nil
	})
	if errors.Is(err, syscall.ENOSYS) {
		// waitid is missing under some emulations (golang/go#16610).
		return errNoExitWatch
	}
	return err
}
