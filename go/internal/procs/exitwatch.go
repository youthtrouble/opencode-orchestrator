package procs

import (
	"errors"
	"syscall"
)

// errNoExitWatch reports that this system cannot wait for a child's exit without reaping it.
var errNoExitWatch = errors.New("procs: no way to wait for an exit without reaping")

// retryEINTR calls call again for as long as a signal handler interrupts it.
func retryEINTR(call func() error) error {
	for {
		if err := call(); !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
