package procs

import (
	"errors"
	"syscall"
)

// waitExited blocks until the child pid has exited, without reaping it, so it keeps its pid,
// and with it its process group id, until Wait reaps it. darwin's waitid also returns for a
// stopped child (golang/go#19314, why os does not use it here), so this waits for kqueue's
// NOTE_EXIT instead. The kernel posts NOTE_EXIT just before it makes the child a zombie, so a
// reap right after it may block for that moment.
func waitExited(pid int) error {
	kq, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(kq)
	var exit syscall.Kevent_t
	syscall.SetKevent(&exit, pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	exit.Fflags = syscall.NOTE_EXIT
	// Registered without an event list, so a failure is returned rather than delivered as an
	// EV_ERROR event.
	err = retryEINTR(func() error {
		_, err := syscall.Kevent(kq, []syscall.Kevent_t{exit}, nil, nil)
		return err
	})
	if errors.Is(err, syscall.ESRCH) {
		// The kernel stops handing out the process before it posts NOTE_EXIT, and drains the
		// references taken before that (a registration holds one), so a watch that found the
		// child is sure to fire and one that did not means the child has exited or is exiting.
		return nil
	}
	if err != nil {
		return err
	}
	events := make([]syscall.Kevent_t, 1)
	return retryEINTR(func() error {
		_, err := syscall.Kevent(kq, nil, events, nil)
		return err
	})
}
