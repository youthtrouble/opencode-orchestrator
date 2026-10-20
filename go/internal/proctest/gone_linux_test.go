package proctest

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// sol-5 C1-F3: a killed process that nobody has reaped yet (an orphan under an init that does
// not reap) has terminated, though kill(pid, 0) still finds it.
func TestGoneCountsAZombie(t *testing.T) {
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	defer func() { _ = child.Wait() }() // the test's own child: reaped only once checked
	deadline := time.Now().Add(5 * time.Second)
	for {
		gone, err := Gone(pid)
		if err != nil {
			t.Fatal(err)
		}
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d not gone 5s after it exited", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("kill(%d, 0) = %v: the zombie was reaped, so the test proves nothing", pid, err)
	}
}
