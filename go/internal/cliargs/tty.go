package cliargs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// IsTerminal mirrors Node's process.stdin.isTTY, which is isatty(3): a terminal-attributes
// ioctl succeeds only on a terminal. os.ModeCharDevice is not equivalent: /dev/null is a
// character device but not a TTY, and Node reads it as an empty prompt.
func IsTerminal(f *os.File) bool {
	var attrs syscall.Termios
	return termios(f, ioctlGetTermios, &attrs) == nil
}

// WithoutEcho runs read with the terminal f's echo turned off, so a secret typed at a prompt
// is not shown. The saved attributes come back however read ends: a return, a panic, or a
// SIGINT (Ctrl-C), SIGTERM or SIGHUP, after which orch exits 128+signal as a shell would.
func WithoutEcho(f *os.File, read func() error) error {
	return echoSwitch{
		get:    func(attrs *syscall.Termios) error { return termios(f, ioctlGetTermios, attrs) },
		set:    func(attrs *syscall.Termios) error { return termios(f, ioctlSetTermios, attrs) },
		notify: signal.Notify,
		stop:   signal.Stop,
		exit:   os.Exit,
		stderr: os.Stderr,
	}.run(read)
}

// echoSwitch is the terminal and process plumbing WithoutEcho needs, injectable so tests can
// drive a panic or a signal without a real terminal.
type echoSwitch struct {
	get, set func(*syscall.Termios) error
	notify   func(chan<- os.Signal, ...os.Signal)
	stop     func(chan<- os.Signal)
	exit     func(code int)
	stderr   io.Writer
}

// endingSignals are the signals that end orch by default while it waits at a prompt.
var endingSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

func (s echoSwitch) run(read func() error) (err error) {
	var saved syscall.Termios
	if err := s.get(&saved); err != nil {
		return err
	}
	quiet := saved
	quiet.Lflag &^= syscall.ECHO

	// Catch the signals before echo goes off, so none can end orch in between.
	signals := make(chan os.Signal, 1)
	s.notify(signals, endingSignals...)
	defer s.stop(signals)
	if err := s.set(&quiet); err != nil {
		return err
	}
	readDone := make(chan struct{})
	defer close(readDone)
	go s.restoreOnSignal(signals, readDone, &saved)
	defer func() {
		if restoreErr := s.set(&saved); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()
	return read()
}

// restoreOnSignal waits for a signal or the end of read. On a signal it puts the saved
// attributes back and exits with the shell's status for that signal.
func (s echoSwitch) restoreOnSignal(signals <-chan os.Signal, readDone <-chan struct{}, saved *syscall.Termios) {
	select {
	case sig := <-signals:
		if err := s.set(saved); err != nil {
			fmt.Fprintf(s.stderr, "orch: could not turn terminal echo back on (%v); run stty echo\n", err)
		}
		s.exit(128 + int(sig.(syscall.Signal)))
	case <-readDone:
	}
}

func termios(f *os.File, request uintptr, attrs *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), request, uintptr(unsafe.Pointer(attrs))); errno != 0 {
		return errno
	}
	return nil
}
