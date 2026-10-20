package proctest

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startReader starts `/bin/cat lifeline` as the test's own child, so the test can reap it and
// read how it ended.
func startReader(t *testing.T, lifeline string) *exec.Cmd {
	t.Helper()
	reader := exec.Command("/bin/cat", lifeline)
	if err := reader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if reader.ProcessState == nil { // still the test's unreaped child, so its pid is its own
			_ = reader.Process.Kill()
			_ = reader.Wait()
		}
	})
	return reader
}

// waitExit0 waits for reader and requires an exit 0: it read EOF, and no signal ended it.
func waitExit0(t *testing.T, reader *exec.Cmd) {
	t.Helper()
	exited := make(chan error, 1)
	go func() { exited <- reader.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the reader ended with %v, want exit 0 at EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reader still runs 5s after the lifeline was cut")
	}
}

func TestLifelineHoldsAReaderUntilCut(t *testing.T) {
	lifeline := filepath.Join(t.TempDir(), "lifeline")
	cut := Lifeline(t, lifeline)
	reader := startReader(t, lifeline)
	time.Sleep(200 * time.Millisecond)
	if gone, err := Gone(reader.Process.Pid); err != nil || gone {
		t.Fatalf("the reader ended before the cut (gone %v, %v)", gone, err)
	}
	cut()
	waitExit0(t, reader)
	cut() // a second cut, as the cleanup makes, is harmless
}

// sol-5 C1-F1: the test's end alone ends a descendant on a lifeline, with no signal sent.
func TestLifelineEndsItsReaderWithTheTest(t *testing.T) {
	var reader *exec.Cmd
	t.Run("holder", func(t *testing.T) {
		lifeline := filepath.Join(t.TempDir(), "lifeline")
		Lifeline(t, lifeline)
		reader = exec.Command("/bin/cat", lifeline)
		if err := reader.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond) // for cat to open it
	})
	waitExit0(t, reader)
}

// A descendant that comes to read the lifeline only after the cut ends instead of waiting.
func TestALateReaderOfACutLifelineEnds(t *testing.T) {
	lifeline := filepath.Join(t.TempDir(), "lifeline")
	Lifeline(t, lifeline)()
	reader := startReader(t, lifeline)
	exited := make(chan error, 1)
	go func() { exited <- reader.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			t.Error("cat read a cut lifeline without failing to open it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a reader that came after the cut still waits 5s later")
	}
}

func TestGoneTellsARunningProcessFromAReapedOne(t *testing.T) {
	sleeper := exec.Command("/bin/sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	pid := sleeper.Process.Pid
	if gone, err := Gone(pid); err != nil || gone {
		t.Errorf("Gone(running %d) = %v, %v", pid, gone, err)
	}
	_ = sleeper.Process.Signal(syscall.SIGKILL) // the test's unreaped child
	_ = sleeper.Wait()
	if gone, err := Gone(pid); err != nil || !gone {
		t.Errorf("Gone(reaped %d) = %v, %v", pid, gone, err)
	}
}
