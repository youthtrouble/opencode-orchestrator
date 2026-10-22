package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// TestLockHolderProcess is not a test: re-executed by holdLock with CLI_LOCK_HOLDER set, it
// holds the kernel lock of that path with its own pid, prints "locked" and keeps the lock until
// its stdin closes.
func TestLockHolderProcess(t *testing.T) {
	path := os.Getenv("CLI_LOCK_HOLDER")
	if path == "" {
		return
	}
	lock, err := ownlock.Acquire(path, os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// holdLock starts a holder of path and returns its pid once it holds the lock. The holder is the test's
// own child: its stdin is closed and it is killed and reaped when the test ends.
func holdLock(t *testing.T, path string) int {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderProcess$")
	cmd.Env = append(os.Environ(), "CLI_LOCK_HOLDER="+path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Scan()
		line <- scanner.Text()
		_, _ = io.Copy(io.Discard, stdout)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	select {
	case got := <-line:
		if got != "locked" {
			t.Fatalf("lock holder printed %q; stderr: %s", got, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("lock holder did not take %s", path)
	}
	return cmd.Process.Pid
}
