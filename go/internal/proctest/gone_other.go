//go:build !linux

package proctest

import (
	"errors"
	"syscall"
)

// Gone reports whether pid has terminated. Off Linux it asks only whether a process has the
// pid: launchd reaps orphans at once, so a killed descendant does not linger as a zombie.
func Gone(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
