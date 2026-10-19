package procs

import (
	"os/exec"
	"syscall"
)

// StartDetached starts the program at path with args in a session of its own, with stdin,
// stdout and stderr on /dev/null, and returns without waiting for it. Unlike a Start child it
// is outside the caller's process group, so a signal to that group does not reach it, and it
// keeps running when the caller exits. Nothing here reaps it: it is meant for a caller that
// exits soon after, leaving the child to init.
func StartDetached(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
