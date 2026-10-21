package cliargs

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
)

// fakeTerminal records the attributes WithoutEcho sets and the signal plumbing it uses.
type fakeTerminal struct {
	mu       sync.Mutex
	attrs    syscall.Termios
	notified chan<- os.Signal
	stopped  bool
	exited   chan int
	stderr   bytes.Buffer
}

func newFakeTerminal() *fakeTerminal {
	t := &fakeTerminal{exited: make(chan int, 1)}
	t.attrs.Lflag = syscall.ECHO | syscall.ICANON
	return t
}

func (t *fakeTerminal) echoOn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attrs.Lflag&syscall.ECHO != 0
}

func (t *fakeTerminal) switcher() echoSwitch {
	return echoSwitch{
		get: func(attrs *syscall.Termios) error {
			t.mu.Lock()
			defer t.mu.Unlock()
			*attrs = t.attrs
			return nil
		},
		set: func(attrs *syscall.Termios) error {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.attrs = *attrs
			return nil
		},
		notify: func(c chan<- os.Signal, _ ...os.Signal) { t.notified = c },
		stop:   func(chan<- os.Signal) { t.stopped = true },
		exit:   func(code int) { t.exited <- code },
		stderr: &t.stderr,
	}
}

func TestWithoutEchoTurnsEchoOffOnlyWhileReading(t *testing.T) {
	term := newFakeTerminal()
	readErr := errors.New("read failed")
	err := term.switcher().run(func() error {
		if term.echoOn() {
			t.Error("echo is on while reading")
		}
		return readErr
	})
	if !errors.Is(err, readErr) || !term.echoOn() || !term.stopped {
		t.Errorf("err %v, echo on %v, signals stopped %v", err, term.echoOn(), term.stopped)
	}
}

func TestWithoutEchoRestoresTheTerminalOnAPanic(t *testing.T) {
	term := newFakeTerminal()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate")
			}
		}()
		_ = term.switcher().run(func() error { panic("boom") })
	}()
	if !term.echoOn() || !term.stopped {
		t.Errorf("after a panic: echo on %v, signals stopped %v", term.echoOn(), term.stopped)
	}
}

func TestWithoutEchoRestoresTheTerminalAndExitsOnASignal(t *testing.T) {
	for sig, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143, syscall.SIGHUP: 129} {
		term := newFakeTerminal()
		var code int
		_ = term.switcher().run(func() error {
			term.notified <- sig
			code = <-term.exited // the real exit never returns; the fake lets read finish
			if !term.echoOn() {
				t.Errorf("%v: echo still off when orch exits", sig)
			}
			return nil
		})
		if code != want {
			t.Errorf("%v: exit %d, want %d", sig, code, want)
		}
	}
}
