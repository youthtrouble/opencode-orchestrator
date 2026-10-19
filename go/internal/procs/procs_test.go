package procs

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testGrace is the Grace of children that are expected to finish on their own.
const testGrace = 10 * time.Second

// killGrace is the Grace of tests that wait for the escalation; slack bounds how late after
// it a stopped child may be reaped on a loaded machine.
const (
	killGrace = 300 * time.Millisecond
	slack     = 5 * time.Second
)

// script writes an executable /bin/sh script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// startReady starts script body, which must print "ready" once its signal handling is in
// place, and returns after that line: signals sent from then on meet the installed
// disposition, not a shell that is still starting.
func startReady(t *testing.T, body string, grace time.Duration) *Child {
	t.Helper()
	stdout, ready := io.Pipe()
	child, err := Start(Spec{Name: script(t, body), Stdout: ready, Grace: grace})
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if line != "ready\n" {
		t.Fatalf("child said %q (%v), want ready", line, err)
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }() // keep later output from blocking the child
	t.Cleanup(func() { _ = ready.Close() })
	return child
}

func wantCode(t *testing.T, got *int, want *int) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil || *got != *want:
		t.Errorf("code = %s, want %s", show(got), show(want))
	}
}

func show(code *int) string {
	if code == nil {
		return "null"
	}
	return strconv.Itoa(*code)
}

func intp(n int) *int { return &n }

// assertGone checks that pid was reaped: kill(pid, 0) finds no such process.
func assertGone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill(%d, 0) = %v, want ESRCH", pid, err)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestWaitMapsExitsToCloseCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *int
	}{
		{"exit 0", "exit 0", intp(0)},
		{"exit 3", "exit 3", intp(3)},
		{"killed by its own SIGTERM", "kill -TERM $$", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			child, err := Start(Spec{Name: script(t, tt.body), Grace: testGrace})
			if err != nil {
				t.Fatal(err)
			}
			code, err := child.Wait()
			if err != nil {
				t.Fatal(err)
			}
			wantCode(t, code, tt.want)
		})
	}
}

func TestSignalledChildClosesWithNull(t *testing.T) {
	child := startReady(t, "echo ready; exec sleep 30", testGrace)
	started := time.Now()
	child.Signal(syscall.SIGTERM)
	code, err := child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, nil)
	if elapsed := time.Since(started); elapsed >= testGrace/2 {
		t.Errorf("a TERM-honouring child took %v, as if it waited for the %v grace", elapsed, testGrace)
	}
	assertGone(t, child.cmd.Process.Pid)
}

func TestChildThatHandlesTermKeepsItsExitCode(t *testing.T) {
	child := startReady(t, `trap 'kill $!; exit 5' TERM; echo ready; sleep 30 >/dev/null 2>&1 & wait $!`, testGrace)
	child.Signal(syscall.SIGTERM)
	code, err := child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, intp(5))
}

func TestSignalEscalatesToKillAfterGrace(t *testing.T) {
	child := startReady(t, "trap '' TERM; echo ready; exec sleep 30", killGrace)
	pid := child.cmd.Process.Pid
	started := time.Now()
	child.Signal(syscall.SIGTERM)
	child.Signal(syscall.SIGTERM) // a second signal neither panics nor extends the grace
	code, err := child.Wait()
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, nil)
	if elapsed < killGrace || elapsed > killGrace+slack {
		t.Errorf("a TERM-ignoring child died after %v, want between %v and %v", elapsed, killGrace, killGrace+slack)
	}
	assertGone(t, pid)
}

func TestSignalAfterExitDoesNothing(t *testing.T) {
	child, err := Start(Spec{Name: script(t, "exit 0"), Grace: killGrace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	child.Signal(syscall.SIGTERM)
	child.Signal(syscall.SIGKILL)
	if child.killTimer != nil {
		t.Error("Signal after Wait armed the SIGKILL escalation for a reaped pid")
	}
}

// TestWaitBoundsThePipeWait: a grandchild that inherited stdout keeps the pipe open after the
// child exits; Wait gives up on it after Grace instead of waiting as long as it lives.
func TestWaitBoundsThePipeWait(t *testing.T) {
	var out strings.Builder
	child, err := Start(Spec{Name: script(t, "sleep 30 & echo $!; exit 0"), Stdout: &out, Grace: killGrace})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	code, err := child.Wait()
	elapsed := time.Since(started)
	if grandchild, convErr := strconv.Atoi(strings.TrimSpace(out.String())); convErr == nil {
		t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })
	} else {
		t.Errorf("child printed %q, not the grandchild's pid", out.String())
	}
	if err != nil {
		t.Fatalf("Wait error = %v; the bounded pipe wait is not an error", err)
	}
	wantCode(t, code, intp(0))
	if elapsed > killGrace+slack {
		t.Errorf("Wait took %v with a grandchild holding stdout, want at most %v", elapsed, killGrace+slack)
	}
}

func TestStartRequiresAGrace(t *testing.T) {
	if _, err := Start(Spec{Name: "/bin/sh"}); err == nil {
		t.Error("Start accepted a zero Grace, which would leave the pipe wait unbounded")
	}
}
