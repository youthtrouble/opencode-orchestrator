package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

func TestIdleVerdictEvidenceCombinations(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for _, active := range []bool{false, true} {
		for _, ledgerKnown := range []bool{false, true} {
			for _, tool := range []bool{false, true} {
				for _, probeKnown := range []bool{false, true} {
					for _, live := range []bool{false, true} {
						e := idleEvidence{now: start.Add(20 * time.Minute), started: start, interval: 20 * time.Minute, ledger: activityLedger{known: ledgerKnown, at: start, inFlight: tool}, probeKnown: probeKnown, live: live}
						want := "stop"
						switch {
						case active:
							e.stdoutAt = e.now
							want = "active"
						case !ledgerKnown:
							want = "ledger_unknown"
						case tool:
							want = "tools_in_flight"
						case !probeKnown:
							want = "probe_unknown"
						case live:
							want = "descendants"
						}
						if got := idleVerdict(e); got != want {
							t.Errorf("%+v: %s, want %s", e, got, want)
						}
					}
				}
			}
		}
	}
	for _, source := range []string{"stdout", "ledger", "started"} {
		e := idleEvidence{now: start.Add(time.Hour), started: start, interval: time.Minute, ledger: activityLedger{known: true, at: start}, probeKnown: true}
		switch source {
		case "stdout":
			e.stdoutAt = e.now.Add(time.Hour)
		case "ledger":
			e.ledger.at = e.now.Add(time.Hour)
		case "started":
			e.started = e.now.Add(time.Hour)
		}
		if got := idleVerdict(e); got != "active" {
			t.Errorf("future %s: %s", source, got)
		}
	}
}

func putLedger(t *testing.T, root, name string, alive time.Time, activity *time.Time, tool bool) {
	t.Helper()
	dir := filepath.Join(root, ".orchestrator", "activity", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	inFlight := 0
	if tool {
		inFlight = 1
	}
	data, err := json.Marshal(map[string]any{"schema": 1, "alive_at": alive, "last_activity_at": activity, "tools_in_flight": inFlight})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	write(t, path+".tmp", string(data))
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func TestReadActivityLedger(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for _, kind := range []string{"absent", "null", "stale", "malformed", "schema", "future", "over-limit", "oversize", "two-writers"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			switch kind {
			case "absent":
			case "null":
				putLedger(t, root, "a", now, nil, false)
			case "stale":
				putLedger(t, root, "a", now.Add(-181*time.Second), &now, false)
			case "malformed", "schema", "oversize":
				putLedger(t, root, "a", now, &now, false)
				data := "{"
				if kind == "schema" {
					data = `{"schema":2}`
				}
				if kind == "oversize" {
					data = strings.Repeat("x", (64<<10)+1)
				}
				write(t, filepath.Join(root, ".orchestrator/activity/opencode/a.json"), data)
			case "future":
				future := now.Add(time.Hour)
				putLedger(t, root, "a", future, &future, false)
			case "over-limit":
				for i := 0; i < 65; i++ {
					putLedger(t, root, fmt.Sprint(i), now, &now, false)
				}
			case "two-writers":
				old := now.Add(-time.Hour)
				putLedger(t, root, "a", now, &old, true)
				putLedger(t, root, "b", now, &now, false)
			}
			got := readActivityLedger(root, now)
			if got.known != (kind == "future" || kind == "two-writers") {
				t.Errorf("ledger %+v", got)
			}
			if kind == "future" && !got.at.Equal(now) {
				t.Errorf("future not clamped: %s", got.at)
			}
			if kind == "two-writers" && (!got.inFlight || !got.at.Equal(now)) {
				t.Errorf("lost writer evidence: %+v", got)
			}
		})
	}
}

func TestActivityLedgerRecheckSharesReadBudget(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 64; i++ {
		putLedger(t, root, fmt.Sprint(i), now, &now, false)
	}
	reader := newActivityLedgerReader(root)
	defer reader.close()
	if got := reader.read(now); !got.known || reader.remaining != 0 {
		t.Fatalf("first snapshot %+v, remaining %d", got, reader.remaining)
	}
	if got := reader.read(now); !got.known || reader.remaining != 0 {
		t.Fatalf("unchanged snapshot %+v, remaining %d", got, reader.remaining)
	}
	putLedger(t, root, "0", now, &now, true)
	if got := reader.read(now); got.known || reader.remaining != 0 {
		t.Fatalf("changed writer must fail open at bound: %+v, remaining %d", got, reader.remaining)
	}
}

func TestLedgerFreshnessDoesNotTrustMtime(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	putLedger(t, root, "a", now, &now, true)
	old := now.Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, ".orchestrator/activity/opencode/a.json"), old, old); err != nil {
		t.Fatal(err)
	}
	if got := readActivityLedger(root, now); !got.known || !got.inFlight {
		t.Errorf("old mtime discarded fresh evidence: %+v", got)
	}
}

type testIdleClock struct {
	mu      sync.Mutex
	at      time.Time
	ticks   chan time.Time
	created chan time.Duration
	checked chan struct{}
}

func (c *testIdleClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *testIdleClock) Ticks() <-chan time.Time { return c.ticks }
func (c *testIdleClock) Stop()                   {}
func (c *testIdleClock) advance(t *testing.T, at time.Time) {
	t.Helper()
	c.mu.Lock()
	c.at = at
	c.mu.Unlock()
	select {
	case c.ticks <- at:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not take tick")
	}
	select {
	case <-c.checked:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish check")
	}
}

type idleTestLog struct {
	mu sync.Mutex
	bytes.Buffer
	ready       chan struct{}
	once        sync.Once
	released    chan struct{}
	releaseOnce sync.Once
}

func (l *idleTestLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.Buffer.Write(p)
	if strings.Contains(string(p), "message: ready") {
		l.once.Do(func() { close(l.ready) })
	}
	if l.released != nil && strings.Contains(string(p), "message: released") {
		l.releaseOnce.Do(func() { close(l.released) })
	}
	return n, err
}
func (l *idleTestLog) text() string { l.mu.Lock(); defer l.mu.Unlock(); return l.Buffer.String() }

// The fake is the test binary as fakeOpencode, so it is quiet and (but for real-grandchild) has
// no live descendants. All clock changes and checks have channel handshakes; timeouts only bound
// test failures.
func TestRunOpencodeIdleWatchdog(t *testing.T) {
	for _, scenario := range []string{"idle", "advancing", "visibility-tool", "quiet-tool-195s", "live-descendant", "real-grandchild", "absent", "stale", "probe-error", "ledger-probe-race", "line-race", "stderr-race", "close-race", "cap-race", "ignores-int"} {
		t.Run(scenario, func(t *testing.T) {
			sb := newBridgeSandbox(t, "opencode")
			for _, assignment := range sb.command().Env {
				key, value, _ := strings.Cut(assignment, "=")
				t.Setenv(key, value)
			}
			t.Setenv(runBridgeEnv, "")
			t.Setenv("ORCH_IDLE_MIN", "1")
			fifo := filepath.Join(sb.home, "control")
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			control, err := os.OpenFile(fifo, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			var grandRelease *os.File
			if scenario == "real-grandchild" {
				for _, name := range []string{"grandcontrol", "grandready"} {
					if err := syscall.Mkfifo(filepath.Join(sb.home, name), 0600); err != nil {
						t.Fatal(err)
					}
				}
				grandRelease, err = os.OpenFile(filepath.Join(sb.home, "grandcontrol"), os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer grandRelease.Close()
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(executable, filepath.Join(sb.bin, "opencode")); err != nil {
				t.Fatal(err)
			}
			t.Setenv(fakeOpencodeEnv, scenario)
			start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
			clock := &testIdleClock{at: start, ticks: make(chan time.Time), created: make(chan time.Duration, 1), checked: make(chan struct{}, 1)}
			runtime := defaultIdleRuntime()
			runtime.now = clock.now
			runtime.ticker = func(d time.Duration) idleTicker { clock.created <- d; return clock }
			runtime.afterCheck = func() { clock.checked <- struct{}{} }
			var injected sync.Once
			if scenario == "line-race" {
				runtime.beforeStop = func(thread *jsThread, _ *procs.Child) {
					injected.Do(func() { thread.handleLine("unparsable output still counts as activity") })
				}
			}
			if scenario == "stderr-race" {
				runtime.beforeStop = func(thread *jsThread, _ *procs.Child) {
					injected.Do(func() { _, _ = (activityStderr{thread: thread, buffer: &lockedBuffer{}}).Write([]byte("x")) })
				}
			}
			if scenario == "close-race" {
				runtime.beforeStop = func(thread *jsThread, _ *procs.Child) {
					thread.mu.Lock()
					thread.closed = true
					thread.mu.Unlock()
					fmt.Fprintln(control, "exit")
				}
			}
			if scenario == "cap-race" {
				runtime.beforeStop = func(thread *jsThread, child *procs.Child) {
					if thread.stopAtTimeout("") {
						thread.mu.Lock()
						thread.signalLocked(child, "cap", syscall.SIGINT)
						thread.mu.Unlock()
					}
				}
			}
			probeLive := scenario == "live-descendant"
			probes := 0
			runtime.probe = func(pid int) ([]int, error) {
				probes++
				if scenario == "ledger-probe-race" {
					injected.Do(func() { now := clock.now(); putLedger(t, sb.project, "a", now, &now, true) })
				}
				if scenario == "probe-error" {
					return nil, errors.New("unknown")
				}
				if probeLive {
					return []int{pid + 1}, nil
				}
				return nil, nil
			}
			if scenario == "real-grandchild" {
				runtime.probe = defaultIdleRuntime().probe
			}
			var stdout bytes.Buffer
			log := &idleTestLog{ready: make(chan struct{}), released: make(chan struct{})}
			done := make(chan int, 1)
			go func() {
				done <- runOpencode([]string{"--cwd", sb.project, "--force", "--prompt", "p"}, nil, &stdout, log, runtime)
			}()
			t.Cleanup(func() { fmt.Fprintln(control, "exit") })
			select {
			case <-log.ready:
			case <-time.After(5 * time.Second):
				t.Fatalf("fake not ready: %s", log.text())
			}
			select {
			case d := <-clock.created:
				if d != 15*time.Second {
					t.Errorf("cadence %s", d)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no ticker")
			}
			now := start.Add(195 * time.Second)
			if scenario == "advancing" || scenario == "visibility-tool" {
				now = start.Add(6 * time.Minute)
			}
			switch scenario {
			case "absent":
			case "stale":
				putLedger(t, sb.project, "a", start, &start, false)
			case "advancing", "visibility-tool":
				putLedger(t, sb.project, "a", now, &now, scenario == "visibility-tool")
			default:
				putLedger(t, sb.project, "a", now, &start, scenario == "quiet-tool-195s")
			}
			clock.advance(t, now)
			if scenario == "visibility-tool" && !strings.Contains(log.text(), "still working: last session activity 0s ago, tool running\n") {
				t.Errorf("tool visibility: %s", log.text())
			}
			if scenario == "advancing" {
				if got := log.text(); !strings.Contains(got, "still working: last session activity 0s ago") || strings.Contains(got, "tool running") {
					t.Errorf("visibility line: %s", got)
				}
				next := now.Add(time.Second)
				putLedger(t, sb.project, "a", next, &next, false)
				clock.advance(t, next)
				if strings.Count(log.text(), "still working:") != 1 {
					t.Errorf("visibility not bounded: %s", log.text())
				}
			}
			if scenario != "idle" && scenario != "ignores-int" && scenario != "close-race" && scenario != "cap-race" {
				select {
				case code := <-done:
					t.Fatalf("healthy/unknown stopped: %d %s", code, log.text())
				default:
				}
				if _, err := os.Stat(filepath.Join(sb.home, "signal")); !os.IsNotExist(err) {
					t.Fatalf("unexpected signal: %v", err)
				}
				if (scenario == "advancing" || scenario == "quiet-tool-195s" || scenario == "absent" || scenario == "stale") && probes != 0 {
					t.Errorf("probed before other conditions allowed stop")
				}
				if scenario == "absent" || scenario == "stale" || scenario == "probe-error" {
					clock.advance(t, now.Add(time.Second))
					if strings.Count(log.text(), "idle watchdog inactive:") != 1 {
						t.Errorf("inactive log: %s", log.text())
					}
					fmt.Fprintln(control, "exit")
				} else {
					probeLive = false
					if scenario == "real-grandchild" {
						fmt.Fprintln(grandRelease, "release")
						fmt.Fprintln(control, "waitchild")
						select {
						case <-log.released:
						case <-time.After(5 * time.Second):
							t.Fatal("grandchild did not exit")
						}
					}
					// The injected line wins once; the following check can stop only after a new quiet interval.
					later := now.Add(61 * time.Second)
					putLedger(t, sb.project, "a", later, &start, false)
					clock.advance(t, later)
				}
			}
			var code int
			select {
			case code = <-done:
			case <-time.After(15 * time.Second):
				t.Fatalf("run did not close: %s", log.text())
			}
			if scenario == "absent" || scenario == "stale" || scenario == "probe-error" || scenario == "close-race" {
				if _, err := os.Stat(filepath.Join(sb.home, "signal")); !os.IsNotExist(err) {
					t.Errorf("closed/unknown child got signal: %v", err)
				}
				if code != 0 {
					t.Errorf("exit %d", code)
				}
				return
			}
			result := parseResult(t, stdout.String())
			if scenario == "cap-race" {
				if code != 1 || jsjson.Path(result, "stop_cause") != nil {
					t.Errorf("cap/idle result: %d %s", code, stdout.String())
				}
				if got := readSignalFile(t, sb, log, stdout.String()); got != "INT\n" {
					t.Errorf("stop signal %q", got)
				}
				if incidents := sb.incidents(t); strings.Count(incidents, "\n") != 1 || !strings.Contains(incidents, `"cause":"cap"`) {
					t.Errorf("cap/idle incident: %s", incidents)
				}
				return
			}
			if code != 1 || jsjson.Path(result, "stop_cause") != "idle" || jsjson.Path(result, "idle_min") != 1.0 {
				t.Errorf("exit %d result %s", code, stdout.String())
			}
			if scenario != "ignores-int" {
				if got := strings.TrimSpace(readSignalFile(t, sb, log, stdout.String())); got != "INT" {
					t.Errorf("signal %q", got)
				}
			}
			incidents := sb.incidents(t)
			for _, want := range []string{`"kind":"timeout"`, `"cause":"idle"`, `"idle_min":1`, `"descendants":0`} {
				if !strings.Contains(incidents, want) {
					t.Errorf("missing %s: %s", want, incidents)
				}
			}
			if scenario == "ignores-int" && (jsjson.Path(result, "exit_code") != nil || !strings.Contains(incidents, "its service session may still be running")) {
				t.Errorf("missing escalation warning: %s %s", stdout.String(), incidents)
			}
		})
	}
}

// readSignalFile reads the file the fake writes on SIGINT; when it is missing the failure carries
// the bridge log, incidents and stdout, which tell an unacknowledged signal from an unsent one.
func readSignalFile(t *testing.T, sb bridgeSandbox, log *idleTestLog, stdout string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sb.home, "signal"))
	if err != nil {
		t.Fatalf("%v\nlog:\n%s\nincidents:\n%s\nstdout:\n%s", err, log.text(), sb.incidents(t), stdout)
	}
	return string(data)
}

// fakeOpencodeEnv names the TestRunOpencodeIdleWatchdog scenario the test binary plays when it
// is started as `opencode`.
const fakeOpencodeEnv = "ORCH_BRIDGE_TEST_FAKE_OPENCODE"

// fakeOpencodeFailed is the fake's own failure exit, distinct from 0 (`exit`) and 130 (SIGINT).
const fakeOpencodeFailed = 3

// fakeOpencode is the quiet opencode of TestRunOpencodeIdleWatchdog. It catches SIGINT before it
// prints `ready`, and the Go runtime queues a caught signal whatever the main goroutine is doing,
// so a SIGINT at any instant after `ready` is acknowledged: it writes "INT\n" to $HOME/signal and
// exits 130. A shell fake cannot promise that: a trapped signal that lands just before the shell
// blocks in read(2) waits for a read that never returns. Lines on $HOME/control: `exit` exits 0;
// `waitchild` (real-grandchild) waits for the bash descendants, then prints `released`.
func fakeOpencode(scenario string) int {
	interrupts := make(chan os.Signal, 1)
	if scenario == "ignores-int" {
		signal.Ignore(syscall.SIGINT)
	} else {
		signal.Notify(interrupts, syscall.SIGINT)
	}
	home := os.Getenv("HOME")
	control, err := os.Open(filepath.Join(home, "control"))
	if err != nil {
		return fakeOpencodeFailure(err)
	}
	var descendants *exec.Cmd
	if scenario == "real-grandchild" {
		if descendants, err = startFakeOpencodeDescendants(home); err != nil {
			return fakeOpencodeFailure(err)
		}
	}
	commands := make(chan string)
	var readErr error
	go func() {
		scanner := bufio.NewScanner(control)
		for scanner.Scan() {
			commands <- scanner.Text()
		}
		readErr = scanner.Err()
		close(commands)
	}()
	if err := printFakeOpencodeText("ready"); err != nil {
		return fakeOpencodeFailure(err)
	}
	released := make(chan error, 1)
	for {
		select {
		case <-interrupts:
			if err := os.WriteFile(filepath.Join(home, "signal"), []byte("INT\n"), 0o600); err != nil {
				return fakeOpencodeFailure(err)
			}
			return 130
		case command, open := <-commands:
			switch {
			case !open && readErr != nil:
				return fakeOpencodeFailure(readErr)
			case !open || command == "exit":
				return 0
			case command == "waitchild" && descendants != nil:
				go func() { released <- descendants.Wait() }()
			}
		case err := <-released:
			if err == nil {
				err = printFakeOpencodeText("released")
			}
			if err != nil {
				return fakeOpencodeFailure(err)
			}
		}
	}
}

// startFakeOpencodeDescendants starts real bash descendants in the fake's process group, so the
// real procs.LiveDescendants probe sees them, and returns once the innermost one is ready. It
// exits when the test writes a line to $HOME/grandcontrol.
func startFakeOpencodeDescendants(home string) (*exec.Cmd, error) {
	release, err := os.Open(filepath.Join(home, "grandcontrol"))
	if err != nil {
		return nil, err
	}
	ready, err := os.OpenFile(filepath.Join(home, "grandready"), os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	descendants := exec.Command("/bin/bash", "-c", `/bin/bash -c "echo ready >&4; read -r release <&3" & wait`)
	descendants.Stdout, descendants.Stderr = os.Stdout, os.Stderr
	descendants.ExtraFiles = []*os.File{release, ready}
	if err := descendants.Start(); err != nil {
		return nil, err
	}
	if _, err := bufio.NewReader(ready).ReadString('\n'); err != nil {
		return nil, err
	}
	return descendants, nil
}

func printFakeOpencodeText(text string) error {
	_, err := fmt.Printf("{\"part\":{\"type\":\"text\",\"text\":%q}}\n", text)
	return err
}

func fakeOpencodeFailure(err error) int {
	fmt.Fprintln(os.Stderr, "fake opencode:", err)
	return fakeOpencodeFailed
}

func TestStopCauseFirstWins(t *testing.T) {
	for _, cause := range []string{"cap", "idle", "crash"} {
		result := newOpencodeResult(options{}, "/p", "now")
		events := newOpencodeEvents(&logger{w: &bytes.Buffer{}}, result)
		events.stopCause = cause
		events.stopSent = true
		if events.stopAtTimeout("must not log") {
			t.Errorf("cap replaced %s", cause)
		}
		for _, later := range []string{"cap", "idle", "crash"} {
			events.signalLocked(nil, later, syscall.SIGINT)
		}
		if cause != "crash" {
			events.handleLine("null")
			if events.crash != nil {
				t.Errorf("late crash replaced %s", cause)
			}
		}
		if events.stopCause != cause {
			t.Errorf("cause %s, want %s", events.stopCause, cause)
		}
	}
}

func TestRunOpencodeIdleValidationBeforeAnySideEffect(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	for _, assignment := range sb.command().Env {
		key, value, _ := strings.Cut(assignment, "=")
		t.Setenv(key, value)
	}
	t.Setenv(runBridgeEnv, "")
	for _, raw := range []string{"", "-1", ".5", "1.", "1e3", "NaN", "Inf", "+1", "10080.1", " 1", "true"} {
		t.Setenv("ORCH_IDLE_MIN", raw)
		var out, errOut bytes.Buffer
		if code := RunOpencode([]string{"--cwd", sb.project}, nil, &out, &errOut); code != 2 || out.Len() != 0 || !strings.HasPrefix(errOut.String(), "ORCH_IDLE_MIN must be minutes") {
			t.Errorf("env %q: exit %d out %s err %s", raw, code, out.String(), errOut.String())
		}
		errOut.Reset()
		if code := RunOpencode([]string{"--cwd", sb.project, "--idle-min", raw}, nil, &out, &errOut); code != 2 || !strings.HasPrefix(errOut.String(), "opencode-exec: --idle-min must be minutes") {
			t.Errorf("flag %q: exit %d err %s", raw, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := RunOpencode([]string{"--idle-min"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "got true") {
		t.Errorf("bare option exit %d err %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(sb.project, ".orchestrator")); !os.IsNotExist(err) {
		t.Errorf("invalid options wrote artifacts: %v", err)
	}
	sb.fakeCLI(t, "opencode", "", "", "exit 0")
	runtime := defaultIdleRuntime()
	runtime.ticker = func(time.Duration) idleTicker { panic("disabled watchdog created ticker") }
	runtime.probe = func(int) ([]int, error) { panic("disabled watchdog probed") }
	out.Reset()
	errOut.Reset()
	if code := runOpencode([]string{"--cwd", sb.project, "--idle-min", "0", "--force", "--prompt", "p"}, nil, &out, &errOut, runtime); code != 0 {
		t.Errorf("flag did not override invalid env: %d %s", code, errOut.String())
	}
}

func TestOpencodeIdleConfigurationPrecedence(t *testing.T) {
	t.Setenv("ORCH_IDLE_MIN", "")
	if err := os.Unsetenv("ORCH_IDLE_MIN"); err != nil {
		t.Fatal(err)
	}
	if got, err := opencodeIdleMinutes(options{}); got != 20 || err != nil {
		t.Errorf("default %v %v", got, err)
	}
	t.Setenv("ORCH_IDLE_MIN", "3.5")
	if got, err := opencodeIdleMinutes(options{}); got != 3.5 || err != nil {
		t.Errorf("env %v %v", got, err)
	}
	for _, raw := range []string{"0", "1.25"} {
		opts, _ := cliargs.ParseArgs([]string{"--idle-min", raw}, opencodeValueKeys, opencodeFlagKeys)
		want, _ := cliargs.IdleMinutes(raw, "test")
		if got, err := opencodeIdleMinutes(opts); got != want || err != nil {
			t.Errorf("flag %v %v", got, err)
		}
	}
}
