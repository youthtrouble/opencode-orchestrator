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
	writeExecutable(t, path, "#!/bin/sh\n"+body+"\n", 0o755)
	return path
}

// writeExecutable writes a file that a test is about to exec. It holds syscall.ForkLock for
// reading while the file is open for writing, so no concurrent fork (a parallel test starting
// its child) can inherit the write descriptor; an inherited one makes Linux refuse to exec the
// file with ETXTBSY until that child execs in turn (golang/go#22315).
func writeExecutable(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	syscall.ForkLock.RLock()
	err := os.WriteFile(path, []byte(content), mode)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
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

func TestStdinDeliversTheBytes(t *testing.T) {
	const prompt = "line one\nprompt é \U0001F600 without a final newline"
	var out strings.Builder
	child, err := Start(Spec{Name: script(t, "exec /bin/cat"), Stdin: strings.NewReader(prompt), Stdout: &out, Grace: testGrace})
	if err != nil {
		t.Fatal(err)
	}
	code, err := child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, intp(0))
	if out.String() != prompt {
		t.Errorf("child read %q, want %q", out.String(), prompt)
	}
}

// A child that exits without reading its stdin (codex rejecting its argv, say) closes the pipe
// under a pending write: the EPIPE is dropped and the child's own exit code reported (PE-13).
func TestStdinUnreadByAnExitingChildIsNotAnError(t *testing.T) {
	prompt := strings.Repeat("p", 1<<20) // far beyond any pipe buffer, so the write must fail
	child, err := Start(Spec{Name: script(t, "exit 2"), Stdin: strings.NewReader(prompt), Grace: testGrace})
	if err != nil {
		t.Fatal(err)
	}
	code, err := child.Wait()
	if err != nil {
		t.Fatalf("Wait error = %v; an unread stdin is not an error", err)
	}
	wantCode(t, code, intp(2))
}

// TestStdinCopyIsBoundedByGrace: a grandchild that inherited stdin and never reads it would
// block the stdin copy for as long as it lives; Wait gives up on it after Grace.
func TestStdinCopyIsBoundedByGrace(t *testing.T) {
	var out strings.Builder
	body := "exec 3<&0; /bin/sleep 30 <&3 >/dev/null 2>&1 & echo $!; exit 0"
	child, err := Start(Spec{Name: script(t, body), Stdin: strings.NewReader(strings.Repeat("p", 1<<20)), Stdout: &out, Grace: killGrace})
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
		t.Fatalf("Wait error = %v; the bounded stdin copy is not an error", err)
	}
	wantCode(t, code, intp(0))
	if elapsed > killGrace+slack {
		t.Errorf("Wait took %v with a grandchild holding stdin, want at most %v", elapsed, killGrace+slack)
	}
}

// startSession starts body in a session of its own and returns once it has printed "ready";
// the lines it printed before are returned too (a grandchild's pid, say).
func startSession(t *testing.T, body string, grace time.Duration) (*Child, []string) {
	t.Helper()
	stdout, w := io.Pipe()
	child, err := Start(Spec{Name: script(t, body), Stdout: w, Grace: grace, NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	var before []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("child ended before ready (%v), printed %q", err, before)
		}
		if line == "ready\n" {
			break
		}
		before = append(before, strings.TrimSpace(line))
	}
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	t.Cleanup(func() { _ = w.Close() })
	return child, before
}

// grandchildPid parses the pid a test script printed for its background child, which the test
// kills when it ends.
func grandchildPid(t *testing.T, printed []string) int {
	t.Helper()
	if len(printed) != 1 {
		t.Fatalf("child printed %q, want one pid", printed)
	}
	pid, err := strconv.Atoi(printed[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// waitGone polls until pid no longer exists (an orphan is reaped by init), or fails.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(slack)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still exists %v after its group was signalled", pid, slack)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSignalGroupReachesTheSessionsGrandchild(t *testing.T) {
	child, printed := startSession(t, "/bin/sleep 60 & echo $!; echo ready; wait", testGrace)
	grandchild := grandchildPid(t, printed)
	child.SignalGroup(syscall.SIGTERM)
	code, err := child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, nil)
	waitGone(t, grandchild)
}

// Without a session of its own the child leads no group: kill(-pid) fails and the child alone
// is signalled, as the JS fallback child.kill(sig) does.
func TestSignalGroupFallsBackToTheChild(t *testing.T) {
	child := startReady(t, "trap 'exit 6' INT; echo ready; while :; do /bin/sleep 0.05; done", testGrace)
	child.SignalGroup(syscall.SIGINT)
	code, err := child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, intp(6))
}

// The worker waits for its bridge as the JS waits for "close": a group signal is never
// followed by SIGKILL, however long the child takes after it.
func TestSignalGroupNeverEscalates(t *testing.T) {
	body := "trap 'echo term' TERM; echo ready; i=0; while [ $i -lt 10 ]; do /bin/sleep 0.05; i=$((i + 1)); done; exit 7"
	child, _ := startSession(t, body, killGrace/3)
	child.SignalGroup(syscall.SIGTERM)
	code, err := child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, code, intp(7))
	if child.killTimer != nil {
		t.Error("SignalGroup armed the SIGKILL escalation")
	}
}

// After Wait the group may still hold a grandchild; SignalGroup leaves it alone, since the
// child's pid is no longer ours.
func TestSignalGroupAfterWaitDoesNothing(t *testing.T) {
	child, printed := startSession(t, "/bin/sleep 60 >/dev/null 2>&1 & echo $!; echo ready; exit 0", testGrace)
	grandchild := grandchildPid(t, printed)
	if _, err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	child.SignalGroup(syscall.SIGTERM)
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(grandchild, 0); err != nil {
		t.Errorf("grandchild %d was signalled after Wait: kill(0) = %v", grandchild, err)
	}
	if child.Pid() <= 0 {
		t.Errorf("Pid() = %d", child.Pid())
	}
}
