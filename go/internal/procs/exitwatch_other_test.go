//go:build !linux

package procs

import (
	"errors"
	"syscall"
)

// hasExited reports whether pid, a child that only the test reaps, has exited. darwin answers
// kill(2) for such a child until it is reaped, but getpgid(2), like waitExited's kqueue, no
// longer finds it once it exits.
func hasExited(pid int) (bool, error) {
	_, err := syscall.Getpgid(pid)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
