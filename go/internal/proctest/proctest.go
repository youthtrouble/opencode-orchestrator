// Package proctest holds test helpers for descendants that a test starts but cannot reap, such
// as the grandchildren of a child under test. Once such a process has ended, init may reap it
// and hand its pid to an unrelated process, so a test never signals one by pid after the fact:
// it ties the descendant's life to a Lifeline instead. Only tests import it.
package proctest

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Lifeline makes a FIFO at path and holds it open until cut is called or the test ends. A
// descendant that reads it (`/bin/cat <path>`) blocks until then and ends on its own at EOF,
// however the test ended, so no cleanup has to signal it. The cut is the only EOF: nothing is
// ever written, and the test holds the only write end (close-on-exec, so no other child of
// the test inherits it). The cut removes the FIFO before it closes that end, so a descendant
// that only comes to read it afterwards fails to open it and ends too, rather than wait for a
// writer that will never come.
func Lifeline(t testing.TB, path string) (cut func()) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// O_RDWR opens a FIFO at once, where O_WRONLY would wait for a reader (Linux and darwin
	// both allow it; POSIX leaves it undefined).
	fifo, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	cut = func() {
		once.Do(func() {
			if err := errors.Join(os.Remove(path), fifo.Close()); err != nil {
				t.Errorf("cut the lifeline %s: %v", path, err)
			}
		})
	}
	t.Cleanup(cut)
	return cut
}

// WaitGone polls until pid has terminated (see Gone), failing the test after within.
func WaitGone(t testing.TB, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		gone, err := Gone(pid)
		if err != nil {
			t.Fatalf("look for process %d: %v", pid, err)
		}
		if gone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d still runs after %v", pid, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
