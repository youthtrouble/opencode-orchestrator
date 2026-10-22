package worker

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	ossignal "os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// The lane's live owners in these tests are helper processes: the test binary re-executed with
// WORKER_HELPER set. A lock holder ("hold") takes a real kernel lock on WORKER_LOCK with its own
// pid, prints "locked" and keeps it until its stdin closes. A runner ("run") is a foreground
// `worker start` over testdata/bridge.sh, with the arguments after "--" as a detached runner
// gets them (`worker start ...`), and exits with the worker's code. Helpers talk only over
// pipes; deadlines only fail a stuck test.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("WORKER_HELPER") {
	case "":
		return
	case "hold":
		lock, err := ownlock.Acquire(os.Getenv("WORKER_LOCK"), os.Getpid())
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
	case "run":
		os.Exit(runHelper())
	default:
		fmt.Fprintln(os.Stderr, "unknown helper")
		os.Exit(1)
	}
}

// runHelper is the runner helper: `worker start <args>` in the foreground, its project root
// the last --cwd.
func runHelper() int {
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if len(args) < 2 || args[0] != "worker" || args[1] != "start" {
		fmt.Fprintf(os.Stderr, "runner arguments %q\n", args)
		return 1
	}
	o := opts(args[2:]...)
	root, _ := o.Value("cwd")
	launch, err := NewProject(root).PrepareStart(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code, err := launch.Foreground(Runtime{
		Self:   os.Getenv("WORKER_BRIDGE"),
		Pid:    os.Getpid(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Now:    time.Now,
		NewID:  func() string { return "run-" + strconv.Itoa(os.Getpid()) },
		Notify: func(c chan<- os.Signal) { ossignal.Notify(c, syscall.SIGINT, syscall.SIGTERM) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
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

// startHelper starts the test binary as the helper mode, with env added to the test's own, in
// a session of its own when leader is set.
func startHelper(t *testing.T, mode string, leader bool, env []string, args ...string) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)...)
	cmd.Env = append(append(os.Environ(), "WORKER_HELPER="+mode), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: leader}
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
	t.Cleanup(func() { h.stop(t, mode) })
	return h
}

// stop ends a helper still running when its test ends. A runner gets SIGTERM, which
// Foreground forwards to its bridge's process group, also to a bridge that starts later, and
// then waits for that bridge: once the runner is reaped no fake bridge it started is left,
// wherever the bridge was blocked. A runner that does not end within the guard, and any other
// helper, is killed. Signalling an exited helper does nothing: it is the test's own child,
// not reaped until done closes.
func (h *helper) stop(t *testing.T, mode string) {
	t.Helper()
	_ = h.stdin.Close()
	if mode == "run" {
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-h.done:
			return
		case <-time.After(10 * time.Second):
			t.Errorf("runner %d did not end on SIGTERM; killing it, its bridge may be left", h.pid())
		}
	}
	_ = h.cmd.Process.Kill()
	h.waitExit(t)
}

// holdLock starts a lock holder of path, creating its directory, and waits until the kernel
// lock is taken.
func holdLock(t *testing.T, path string, leader bool) *helper {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	h := startHelper(t, "hold", leader, []string{"WORKER_LOCK=" + path})
	h.expectLine(t, "locked")
	return h
}

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

// waitExit waits for the helper to exit and returns how it ended.
func (h *helper) waitExit(t *testing.T) syscall.WaitStatus {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("helper %d did not exit", h.pid())
	}
	return h.cmd.ProcessState.Sys().(syscall.WaitStatus)
}

// killHard SIGKILLs the helper and waits for it, so it leaves its lock file without a kernel
// lock: what a runner killed with SIGKILL leaves.
func (h *helper) killHard(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if status := h.waitExit(t); !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("helper %d ended with %v, want SIGKILL", h.pid(), status)
	}
}

// gatedBridge makes testdata/bridge.sh print "gate" on stderr (its runner relays it to
// stdout), hold the FIFO $FAKE_LOG/go open, announce its start on the FIFO $FAKE_LOG/started
// and then wait for a line on go (FAKE_GATE). The cleanup writes that line for a bridge still
// holding go; a bridge blocked earlier is ended by its runner's stop (see helper.stop), which
// runs first.
func gatedBridge(t *testing.T, f *fakeRun) {
	t.Helper()
	for _, name := range []string{"started", "go"} {
		if err := syscall.Mkfifo(filepath.Join(f.log, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_GATE", "1")
	t.Cleanup(func() { openGate(t, f) })
}

// awaitBridgeStart blocks until the gated bridge has started: it has written to the started
// FIFO and holds the go FIFO open.
func awaitBridgeStart(t *testing.T, f *fakeRun) {
	t.Helper()
	started := make(chan error, 1)
	go func() {
		data, err := os.ReadFile(filepath.Join(f.log, "started"))
		if err == nil && string(data) != "started\n" {
			err = fmt.Errorf("started FIFO read %q", data)
		}
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge did not start")
	}
}

// openGate writes the line a gated bridge waits for. O_RDWR never blocks on a FIFO; the line
// reaches a bridge that holds go open and is dropped when none does.
func openGate(t *testing.T, f *fakeRun) {
	t.Helper()
	gate, err := os.OpenFile(filepath.Join(f.log, "go"), os.O_RDWR, 0)
	if err != nil {
		t.Error(err)
		return
	}
	_, _ = gate.WriteString("go\n")
	if err := gate.Close(); err != nil {
		t.Error(err)
	}
}

func bridgeScript(t *testing.T) string {
	t.Helper()
	self, err := filepath.Abs("testdata/bridge.sh")
	if err != nil {
		t.Fatal(err)
	}
	return self
}
