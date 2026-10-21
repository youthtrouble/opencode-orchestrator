package procs

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// unreapedChild is a child that only the test reaps, so waitExited can be seen not to.
type unreapedChild struct {
	pid    int
	reaped bool
}

// startUnreaped starts script body outside Child, so nothing in this package reaps it. Should
// the test end before reaping it, the cleanup kills and reaps it: until then its pid is still
// its own.
func startUnreaped(t *testing.T, body string) *unreapedChild {
	t.Helper()
	cmd := exec.Command(script(t, body))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &unreapedChild{pid: cmd.Process.Pid}
	t.Cleanup(func() {
		if !child.reaped {
			killAndReap(child.pid)
		}
	})
	return child
}

// awaitExit returns once the child has exited, however long a loaded machine takes to start and
// end it, failing after slack. Only the test reaps the child, so from then on it is a zombie
// (on darwin, possibly from a moment later: see reap).
func (c *unreapedChild) awaitExit(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(slack)
	for {
		exited, err := hasExited(c.pid)
		if err != nil {
			t.Fatalf("look for child %d: %v", c.pid, err)
		}
		if exited {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d had not exited within %v", c.pid, slack)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reap collects the exit status of a child that waitExited reported exited. WNOHANG proves that
// the exit was left to collect: a child already reaped fails with ECHILD. darwin posts the exit
// just before the child becomes a zombie, so a 0 (not yet waitable) is retried briefly.
func (c *unreapedChild) reap(t *testing.T) syscall.WaitStatus {
	t.Helper()
	deadline := time.Now().Add(slack)
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(c.pid, &status, syscall.WNOHANG, nil)
		if err != nil {
			t.Fatalf("wait4(%d, WNOHANG) = %v: the child was reaped already", c.pid, err)
		}
		if pid == c.pid {
			c.reaped = true
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d was not waitable %v after waitExited returned", c.pid, slack)
		}
		time.Sleep(time.Millisecond)
	}
}

// watchExit runs waitExited in the background; the channel receives its result.
func watchExit(pid int) <-chan error {
	exited := make(chan error, 1)
	go func() { exited <- waitExited(pid) }()
	return exited
}

func waitWatched(t *testing.T, exited <-chan error) {
	t.Helper()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("waitExited = %v", err)
		}
	case <-time.After(slack):
		t.Fatalf("waitExited did not return within %v of the exit", slack)
	}
}

func TestWaitExitedLeavesTheExitedChildUnreaped(t *testing.T) {
	child := startUnreaped(t, "/bin/sleep 0.1; exit 7")
	waitWatched(t, watchExit(child.pid))
	if status := child.reap(t); !status.Exited() || status.ExitStatus() != 7 {
		t.Errorf("wait status %#x, want exit 7", status)
	}
}

// A stopped child has not exited (darwin's waitid says otherwise, golang/go#19314).
func TestWaitExitedIgnoresAStoppedChild(t *testing.T) {
	child := startUnreaped(t, "exec /bin/sleep 30")
	exited := watchExit(child.pid)
	if err := syscall.Kill(child.pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		t.Fatalf("waitExited returned %v for a stopped child", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := syscall.Kill(child.pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitWatched(t, exited)
	if status := child.reap(t); !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Errorf("wait status %#x, want killed by SIGKILL", status)
	}
}

// A child that is a zombie already is reported at once (darwin finds no process to watch then).
// Only the watch is timed: a loaded machine took over a second to start and end the child.
func TestWaitExitedReturnsAtOnceForAZombie(t *testing.T) {
	child := startUnreaped(t, "exit 3")
	child.awaitExit(t)
	started := time.Now()
	waitWatched(t, watchExit(child.pid))
	if elapsed := time.Since(started); elapsed > slack/5 {
		t.Errorf("waitExited took %v for a zombie", elapsed)
	}
	if status := child.reap(t); !status.Exited() || status.ExitStatus() != 3 {
		t.Errorf("wait status %#x, want exit 3", status)
	}
}
