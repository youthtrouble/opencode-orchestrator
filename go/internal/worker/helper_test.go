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
	"sync"
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
	case "signal-child":
		os.Exit(runSignalChild())
	default:
		fmt.Fprintln(os.Stderr, "unknown helper")
		os.Exit(1)
	}
}

// signalChildFailed distinguishes fixture failures from a gate release or acknowledged signal.
const signalChildFailed = 3

// runSignalChild catches signals before announcing itself. A shell trap can remain pending
// behind read(2) when the signal arrives just before the read; Go queues it independently.
func runSignalChild() int {
	// The test injects only one signal. Two slots leave room to spare without dropping it.
	signals := make(chan os.Signal, 2)
	ossignal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer ossignal.Stop(signals)
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "signal child:", err)
		return signalChildFailed
	}
	log := os.Getenv("FAKE_LOG")
	if err := os.WriteFile(filepath.Join(log, "child-pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return fail(err)
	}
	events, err := os.OpenFile(filepath.Join(log, "events"), os.O_WRONLY, 0)
	if err != nil {
		return fail(err)
	}
	defer events.Close()
	writeEvent := func(event string) error {
		_, err := events.WriteString(event + "\n")
		return err
	}
	if err := writeEvent("child-started"); err != nil {
		return fail(err)
	}
	received, err := waitSignalChildGate(log, "ready-go", signals)
	if err != nil {
		return fail(err)
	}
	if received == nil {
		if os.Getenv("FAKE_WAIT") == "child-exit" {
			if err := writeEvent("child-exit"); err != nil {
				return fail(err)
			}
			return 0
		}
		if err := writeEvent("ready"); err != nil {
			return fail(err)
		}
		received, err = waitSignalChildGate(log, "child-go", signals)
		if err != nil {
			return fail(err)
		}
	}
	if received != nil {
		ack := "child INT"
		if received == syscall.SIGTERM {
			ack = "child TERM"
		}
		if err := writeEvent(ack); err != nil {
			return fail(err)
		}
	}
	return 0
}

// The fixture holds both ends open, so opening a gate cannot block. Read one line, not EOF:
// the fixture keeps its writer open until teardown. A signal can interrupt either gate wait.
func waitSignalChildGate(log, name string, signals <-chan os.Signal) (os.Signal, error) {
	gate, err := os.Open(filepath.Join(log, name))
	if err != nil {
		return nil, err
	}
	defer gate.Close()
	read := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(gate).ReadString('\n')
		read <- err
	}()
	select {
	case received := <-signals:
		return received, nil
	case err := <-read:
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return nil, nil
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
			t.Fatalf("helper %d printed %q, want %q", h.pid(), got, want)
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
	file, err := os.OpenFile(filepath.Join(f.log, "started"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	readerDone := make(chan struct{})
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
		select {
		case <-readerDone:
		case <-time.After(10 * time.Second):
			t.Error("started FIFO reader did not stop")
		}
	})
	go func() {
		defer close(readerDone)
		data, err := bufio.NewReader(file).ReadString('\n')
		if err == nil && data != "started\n" {
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

// signalFixture keeps both ends of each FIFO open before the shell starts. A release is
// retained even if cleanup runs before the shell opens its gate. There are no open goroutines
// to cancel; the sole event reader is explicitly stopped and joined after the run is reaped.
type signalFixture struct {
	files  map[string]*os.File
	once   map[string]*sync.Once
	events chan string
}

// bridge.sh emits at most child-started, ready, one child ACK, one bridge ACK and
// child-done for the single injected signal. child-exit replaces ready and the ACKs.
// Keep this bound in sync with the shell protocol: cleanup must drain its stop marker
// even when the test consumes no events. The stop marker itself is never enqueued.
const signalFixtureMaxEvents = 5

func newSignalFixture(t *testing.T, f *fakeRun) *signalFixture {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SIGNAL_CHILD", executable)
	s := &signalFixture{files: make(map[string]*os.File), once: make(map[string]*sync.Once), events: make(chan string, signalFixtureMaxEvents)}
	for _, name := range []string{"events", "ready-go", "child-go", "bridge-go"} {
		path := filepath.Join(f.log, name)
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		s.files[name] = file
		s.once[name] = new(sync.Once)
		// Register each endpoint as it is acquired, including partial setup failures.
		t.Cleanup(func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	readerDone := make(chan struct{})
	var readerErr error
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(s.files["events"])
		for scanner.Scan() {
			if scanner.Text() == "stop" {
				return
			}
			s.events <- scanner.Text()
		}
		readerErr = scanner.Err()
	}()
	t.Cleanup(func() {
		if _, err := s.files["events"].WriteString("stop\n"); err != nil {
			t.Error(err)
		}
		select {
		case <-readerDone:
			if readerErr != nil {
				t.Error(readerErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("fixture event reader did not stop")
		}
	})
	return s
}

func (s *signalFixture) release(t *testing.T, name string) {
	t.Helper()
	s.once[name].Do(func() {
		if _, err := s.files[name].WriteString("go\n"); err != nil {
			t.Error(err)
		}
	})
}

func (s *signalFixture) event(t *testing.T) string {
	t.Helper()
	select {
	case event := <-s.events:
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("fixture did not send an event")
		return ""
	}
}

type signalRun struct {
	done     chan struct{}
	notified chan chan<- os.Signal
	code     int
	err      error
}

// startSignalRun registers teardown before launching Foreground. Cleanup uses only gates,
// never a second signal that could hide a lost first signal in the early-Notify regression.
func startSignalRun(t *testing.T, p Project, s *signalFixture, rt Runtime) *signalRun {
	t.Helper()
	launch, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	r := &signalRun{done: make(chan struct{}), notified: make(chan chan<- os.Signal, 1)}
	var signals chan<- os.Signal
	notify := rt.Notify
	rt.Notify = func(c chan<- os.Signal) {
		notify(c)
		signals = c
		r.notified <- c
	}
	t.Cleanup(func() {
		for _, gate := range []string{"ready-go", "child-go", "bridge-go"} {
			s.release(t, gate)
		}
		awaitSignalRun(t, r.done)
		// Notify is synchronous and has returned before Foreground can finish. Tests are
		// the only other senders and have stopped sending before their cleanup runs.
		if signals != nil {
			close(signals)
		}
	})
	go func() {
		defer close(r.done)
		r.code, r.err = launch.Foreground(rt)
	}()
	return r
}

func awaitSignalRun(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture run did not exit within 10 seconds")
	}
}
