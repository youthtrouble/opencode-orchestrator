package supervisor

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// A live holder of a lock in these tests is a helper process: the test binary re-executed with
// SUPERVISOR_HELPER set (CA2). A holder ("hold") takes the kernel lock of SUPERVISOR_LOCK with
// its own pid, prints "locked" and keeps it until its stdin closes. A server ("serve") runs
// Serve over the project at SUPERVISOR_ROOT with its own pid, prints "serving" at its first
// wait, creates the stop file once its stdin closes and exits with Serve's code. Helpers talk
// only over pipes; deadlines only fail a stuck test.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("SUPERVISOR_HELPER") {
	case "":
		return
	case "hold":
		lock, err := ownlock.Acquire(os.Getenv("SUPERVISOR_LOCK"), os.Getpid())
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
	case "serve":
		os.Exit(serveHelper(os.Getenv("SUPERVISOR_ROOT")))
	default:
		fmt.Fprintln(os.Stderr, "unknown helper")
		os.Exit(1)
	}
}

// serveHelper serves an empty project: no lane and no flip file, so no tick runs a child,
// posts or records anything.
func serveHelper(root string) int {
	p := NewProject(root)
	sup := New(p, Deps{
		Now:      time.Now,
		Pid:      os.Getpid(),
		Self:     "/nonexistent/orch",
		Alive:    func(any) bool { return false },
		Mailbox:  func() *mailbox.Mailbox { return mailbox.New(root) },
		Incident: func(incident.Draft) {},
		Sleep: func(time.Duration) {
			fmt.Println("serving")
			_, _ = io.Copy(io.Discard, os.Stdin)
			if err := os.WriteFile(p.StopPath, nil, 0o600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
	})
	code, err := sup.Serve(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return code
}

// helper is one helper process started by the test, and the only kind of process these tests
// signal.
type helper struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	done   chan struct{}
	stderr strings.Builder
}

func (h *helper) pid() int { return h.cmd.Process.Pid }

// startHelper starts the test binary in the helper mode with env added to the test's own. A
// helper still running when the test ends has its stdin closed and is then killed.
func startHelper(t *testing.T, mode string, env ...string) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(append(os.Environ(), "SUPERVISOR_HELPER="+mode), env...)
	h := &helper{cmd: cmd, lines: make(chan string, 16), done: make(chan struct{})}
	cmd.Stderr = &h.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			h.lines <- scanner.Text()
		}
		_ = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() {
		_ = h.stdin.Close()
		_ = h.cmd.Process.Kill()
		<-h.done
	})
	return h
}

// holdLock starts a holder of path, creating its directory, and returns it once the kernel
// lock is taken.
func holdLock(t *testing.T, path string) *helper {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	h := startHelper(t, "hold", "SUPERVISOR_LOCK="+path)
	h.expectLine(t, "locked")
	return h
}

// holdLock has a helper process hold the lock at rel under .orchestrator/.
func (f *fixture) holdLock(rel string) *helper { return holdLock(f.t, f.path(rel)) }

func (h *helper) expectLine(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-h.lines:
		if got != want {
			t.Fatalf("helper %d printed %q, want %q; stderr: %s", h.pid(), got, want, h.stderr.String())
		}
	case <-h.done:
		t.Fatalf("helper %d exited before %q; stderr: %s", h.pid(), want, h.stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("helper %d did not print %q", h.pid(), want)
	}
}

// exitCode waits for the helper to exit and returns its exit code.
func (h *helper) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("helper %d did not exit", h.pid())
	}
	status := h.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Exited() {
		t.Fatalf("helper %d ended with %v", h.pid(), status)
	}
	return status.ExitStatus()
}
