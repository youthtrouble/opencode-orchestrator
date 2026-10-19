package cliargs

import (
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal mirrors Node's process.stdin.isTTY, which is isatty(3): a terminal-attributes
// ioctl succeeds only on a terminal. os.ModeCharDevice is not equivalent: /dev/null is a
// character device but not a TTY, and Node reads it as an empty prompt.
func IsTerminal(f *os.File) bool {
	var attrs syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlGetTermios, uintptr(unsafe.Pointer(&attrs)))
	return errno == 0
}
