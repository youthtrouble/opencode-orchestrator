//go:build parity

package parity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Bounds of one serve run: the lock must appear within serveLockWait, the serve then ticks
// for serveTickTime before the stop file is written, and must exit within serveExitWait.
const (
	serveLockWait = 10 * time.Second
	serveTickTime = time.Second
	serveExitWait = 20 * time.Second
)

// runDaemon runs argv (a supervisor serve) in the sandbox: it waits until the lock names the
// started process, lets it tick once, writes the stop file and waits for it to exit. A serve
// that does not exit in time is killed by the pid this test started, and the case fails.
func (h *harness) runDaemon(t *testing.T, sb sandbox, argv []string, stdin []byte, env []string) outcome {
	t.Helper()
	cmd := h.command(sb, argv, stdin, env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", argv[0], err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	dir := filepath.Join(sb.project, ".orchestrator")
	if runErr, done := waitForLock(filepath.Join(dir, "supervisor.lock"), cmd.Process.Pid, exited); done {
		return outcome{exitCode: exitCode(t, argv[0], runErr), stdout: stdout.String(), stderr: stderr.String(), files: sb.files(t)}
	}
	select {
	case runErr := <-exited: // it died while ticking (a crash, or PE-17's JS)
		return outcome{exitCode: exitCode(t, argv[0], runErr), stdout: stdout.String(), stderr: stderr.String(), files: sb.files(t)}
	case <-time.After(serveTickTime):
	}
	if err := os.WriteFile(filepath.Join(dir, "supervisor.stop"), nil, 0o644); err != nil {
		t.Error(err)
	}
	select {
	case runErr := <-exited:
		return outcome{exitCode: exitCode(t, argv[0], runErr), stdout: stdout.String(), stderr: stderr.String(), files: sb.files(t)}
	case <-time.After(serveExitWait):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatalf("%s did not stop within %s; killed %d", argv[0], serveExitWait, cmd.Process.Pid)
		return outcome{}
	}
}

// waitForLock polls until the lock holds pid or the process exits (done, with its error).
// Past serveLockWait it returns not done, and the caller stops the serve.
func waitForLock(lock string, pid int, exited <-chan error) (runErr error, done bool) {
	want := strconv.Itoa(pid)
	deadline := time.Now().Add(serveLockWait)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(lock); err == nil && strings.TrimSpace(string(data)) == want {
			return nil, false
		}
		select {
		case runErr := <-exited:
			return runErr, true
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil, false
}

var (
	// superISO is a timestamp a tick or a stop writes into the state.
	superISO = regexp.MustCompile(`("(?:checked_at|created_at|stopped_at)": ")(` + isoMillis.String() + `)(")`)
	// superNextISO is a lane's next check as a timestamp: the tick's time plus a cadence.
	superNextISO = regexp.MustCompile(`("next_check_at": ")(` + isoMillis.String() + `)(")`)
	// superLock is the text of a lock a serve claimed: its pid.
	superLock = regexp.MustCompile(`^\d+$`)
	// incidentProject is the project name (the sandbox directory's base name) an incident records.
	incidentProject = regexp.MustCompile(`("project":")\d{3}(")`)
	// superEpoch is a lane's next check, an event's retry time or a flip evaluation's tick
	// time: now plus an interval (zero for the flip fields).
	superEpoch = regexp.MustCompile(`("(?:next_check|retry_after|last_flip_check|last_relaunch_at)": )(\d+)`)
	// superPid is the serving process's pid, recorded in the state at every tick.
	superPid = regexp.MustCompile(`("pid": )\d+`)
	// superLogLine is a supervisor log line's timestamp.
	superLogLine = regexp.MustCompile(`(?m)^(` + isoMillis.String() + `) `)
	// superMailboxID is the id of the wake message an event records.
	superMailboxID = regexp.MustCompile(`("mailbox_id": ")` + freshUUID.String() + `(")`)
	// incidentFresh is the at, version and project of an incident line written in the run.
	incidentFresh = regexp.MustCompile(`("at":")(` + isoMillis.String() + `)(".*"version":")[^"]*(")`)
)

// supervisorIntervals are the cadences a fresh epoch field adds to the tick's time: none for
// last_flip_check and last_relaunch_at, then the focused, standard and deep lane cadences.
var supervisorIntervals = []int64{0, 120, 300, 600}

// cadenceAfter reports the cadence by which value (epoch seconds) follows a tick inside
// [startSec, nowSec]. The run is far shorter than the gaps between cadences, so at most one
// matches.
func cadenceAfter(value, startSec, nowSec int64) (int64, bool) {
	for _, interval := range supervisorIntervals {
		if tick := value - interval; tick >= startSec && tick <= nowSec {
			return interval, true
		}
	}
	return 0, false
}

// normalizeServe rewrites the fresh values of a serve that starts after since, each only
// when its value lies in the run's time window: seeded values are compared verbatim. An
// epoch value is fresh only as tick time plus one of the cadences.
func normalizeServe(since time.Time) func(string) string {
	startSec := since.Unix()
	start := time.Unix(startSec, 0).UTC().Format("2006-01-02T15:04:05.000Z")
	return func(s string) string {
		now := time.Now()
		collected := now.UTC().Format("2006-01-02T15:04:05.000Z")
		fresh := func(at string) bool { return at >= start && at <= collected }
		s = superISO.ReplaceAllStringFunc(s, func(field string) string {
			parts := superISO.FindStringSubmatch(field)
			if !fresh(parts[2]) {
				return field
			}
			return parts[1] + "<iso>" + parts[3]
		})
		s = superNextISO.ReplaceAllStringFunc(s, func(field string) string {
			parts := superNextISO.FindStringSubmatch(field)
			at, err := time.Parse(time.RFC3339Nano, parts[2])
			if err != nil {
				return field
			}
			if interval, ok := cadenceAfter(at.Unix(), startSec, now.Unix()); ok {
				return parts[1] + "<iso+" + strconv.FormatInt(interval, 10) + ">" + parts[3]
			}
			return field
		})
		s = superLogLine.ReplaceAllStringFunc(s, func(line string) string {
			if !fresh(strings.TrimSpace(line)) {
				return line
			}
			return "<iso> "
		})
		s = superEpoch.ReplaceAllStringFunc(s, func(field string) string {
			parts := superEpoch.FindStringSubmatch(field)
			value, _ := strconv.ParseInt(parts[2], 10, 64)
			if interval, ok := cadenceAfter(value, startSec, now.Unix()); ok {
				return parts[1] + "<now+" + strconv.FormatInt(interval, 10) + ">"
			}
			return field
		})
		s = superPid.ReplaceAllString(s, "${1}<pid>")
		s = superLock.ReplaceAllString(s, "<pid>")
		s = incidentProject.ReplaceAllString(s, "${1}<project>${2}")
		s = superMailboxID.ReplaceAllString(s, "${1}<uuid>${2}")
		s = incidentFresh.ReplaceAllStringFunc(s, func(line string) string {
			parts := incidentFresh.FindStringSubmatch(line)
			if !fresh(parts[2]) {
				return line
			}
			return parts[1] + "<iso>" + parts[3] + "<version>" + parts[4]
		})
		return workerMailbox.ReplaceAllString(s, "${1}<uuid>${2}<iso>${3}")
	}
}

type serveCase struct {
	name      string
	seed      seed
	crash     crashClass
	errorLine string
}

// serveInvocation runs `supervisor serve --cwd <sandbox project>` in each runtime as a daemon.
func (h *harness) serveInvocation(c serveCase) invocation {
	args := []string{"serve", "--cwd", "."}
	return invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, args...),
		orch:         append([]string{h.orchBin, "supervisor"}, args...),
		env:          []string{noPostRunRefresh},
		seed:         c.seed,
		crash:        c.crash,
		errorLine:    c.errorLine,
		projectPaths: true,
		normalize:    normalizeServe(time.Now()),
		daemon:       true,
	}
}

func seedSupervisorConfig(content string) seed {
	return seedProjectFile(".orchestrator/supervisor.json", content)
}

// seedWake configures the given wake channels (a JSON array) and creates logs/, as start
// does before a serve.
func seedWake(channels string) seed {
	return seeds(seedSupervisorConfig(`{"wake": `+channels+`}`), seedProjectDir(".orchestrator/logs"), seedHomeDir("fake-log"))
}

func seedHomeDir(rel string) seed {
	return func(t *testing.T, sb sandbox) {
		if err := os.MkdirAll(filepath.Join(sb.home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// commandChannel logs the wake text to $HOME/fake-log/command.txt and exits with code.
func commandChannel(code int) string {
	script := `printf '%s\n' "$1" >> "$HOME/fake-log/command.txt"; exit ` + strconv.Itoa(code)
	argv, _ := json.Marshal([]string{"/bin/sh", "-c", script, "sh"})
	return `{"type": "command", "argv": ` + string(argv) + `}`
}

// seedCodexQueue scripts the fake codex's queue mode to exit with code.
func seedCodexQueue(code int) seed {
	return seedFile("fake-codex-queue/exit", strconv.Itoa(code)+"\n")
}

// finishedLane is a lane whose runner finished: its event is due at once.
func finishedLane(lane string) seed {
	return seedWorkerState(lane, `{"lane": "`+lane+`", "status": "finished", "run_id": "run-`+lane+`", "pid": 0, "harness": "claude", "breadth": "standard"}`)
}

// ownPid is this test process: a pid both runtimes see alive.
var ownPid = strconv.Itoa(os.Getpid())

// stopFile is present before the first loop: serve stops without ticking.
var stopFile = seedProjectFile(".orchestrator/supervisor.stop", "")

func seedConductor(content string) seed { return seedWorkerState("conductor", content) }

// testSupervisorServe compares one tick of `orch supervisor serve` with bin/supervisor.mjs.
func (h *harness) testSupervisorServe(t *testing.T) {
	command := func(code int) seed { return seedWake("[" + commandChannel(code) + "]") }
	cases := []serveCase{
		{name: "no-workers"},
		{name: "no-workers-realistic-state", seed: seedSupervisorState(realisticSupervisorState)},
		{name: "stale-lock", seed: seedProjectFile(".orchestrator/supervisor.lock", "999999999\n")},
		{name: "stopped-state-restart", seed: seedSupervisorState(`{"status":"stopped","stopped_at":"2026-10-18T09:00:00.123Z","storage_error":"EACCES","storage_failures":4}`)},
		{name: "lane/finished-command-0", seed: seeds(finishedLane("a"), command(0))},
		{name: "lane/finished-command-3", seed: seeds(finishedLane("a"), command(3))},
		{name: "lane/failed", seed: seeds(seedWorkerState("b", `{"status": "failed", "run_id": "rb", "breadth": "focused"}`), command(0))},
		{name: "lane/interrupted", seed: seeds(seedWorkerState("c", `{"status": "interrupted", "run_id": "rc", "breadth": "deep"}`), command(0))},
		{name: "lane/deferred-not-yet", seed: seeds(seedWorkerState("d", `{"status": "deferred", "run_id": "rd", "resume_after": "2999-01-01T00:00:00.000Z"}`), command(0))},
		{name: "lane/unknown-status", seed: seeds(seedWorkerState("e", `{"status": "weird", "run_id": "re"}`), command(0))},
		{name: "lane/stale-lock", seed: seeds(seedWorkerState("f", `{"status": "finished", "run_id": "rf", "pid": 999999999}`), seedWorkerLock("f"), command(0))},
		{name: "lane/process-exited", seed: seeds(seedWorkerState("g", `{"status": "running", "run_id": "rg", "pid": 999999999}`), command(0))},
		{name: "lane/primitive-file", seed: seeds(seedWorkerState("h", `5`), command(0))},
		{name: "lane/primitive-lanes", seed: seeds(finishedLane("a"), seedSupervisorState(`{"lanes": 5}`), command(0))},
		{name: "event/acked", seed: seeds(finishedLane("a"), command(0), seedAcks(`{"run-a:finished": {"by": "x", "at": "2026-01-01T00:00:00.000Z"}}`))},
		{name: "event/retry-past", seed: seeds(finishedLane("a"), command(0), seedSupervisorState(`{"events": {"run-a:finished": {"status": "delivery_failed", "reason": "finished", "lane": "a", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1}}}`))},
		{name: "event/queued-not-retried", seed: seeds(finishedLane("a"), command(0), seedSupervisorState(`{"events": {"run-a:finished": {"status": "queued", "reason": "finished", "lane": "a", "created_at": "2999-10-19T10:00:00.000Z", "retry_after": 1}}}`))},
		{name: "event/logs-absent", seed: seeds(finishedLane("a"), seedSupervisorConfig(`{"wake": [`+commandChannel(0)+`]}`), seedHomeDir("fake-log"))},
		{name: "event/mailbox-dir-a-file", seed: seeds(finishedLane("a"), command(0), seedProjectFile(".orchestrator/mailbox", ""))},
		{name: "channel/codex-queue-0", seed: seeds(finishedLane("a"), seedWake(`[{"type": "codex-queue", "thread": "th-1"}]`), seedCodexQueue(0))},
		{name: "channel/codex-queue-1", seed: seeds(finishedLane("a"), seedWake(`[{"type": "codex-queue", "thread": "th-1"}]`), seedCodexQueue(1))},
		{name: "channel/codex-queue-unscripted", seed: seeds(finishedLane("a"), seedWake(`[{"type": "codex-queue", "thread": "th-1"}]`))},
		{name: "channel/opencode-run", seed: seeds(finishedLane("a"), seedWake(`[{"type": "opencode-run", "session": "ses-1"}]`), seedFile("fake-opencode-run/case", "ok\n"))},
		{name: "channel/unknown-type", seed: seeds(finishedLane("a"), seedWake(`[{"type": "pager"}]`))},
		{name: "channel/no-type", seed: seeds(finishedLane("a"), seedWake(`[{}]`))},
		{name: "channel/null", seed: seeds(finishedLane("a"), seedWake(`[null]`))},
		{name: "channel/two", seed: seeds(finishedLane("a"), seedWake(`[{"type": "pager"}, `+commandChannel(0)+`]`))},
		{name: "redispatch/disabled", seed: seeds(seedWorkerState("d", `{"status": "deferred", "run_id": "rd", "resume_after": "2020-01-01T00:00:00.000Z", "brief": "b.md", "harness": "claude"}`), seedSupervisorConfig(`{"auto_redispatch_deferred": false}`))},
		{name: "redispatch/already", seed: seeds(seedWorkerState("d", `{"status": "deferred", "run_id": "rd", "resume_after": "2020-01-01T00:00:00.000Z", "redispatched_at": "2020-01-02T00:00:00.000Z"}`))},
		{name: "state/null", seed: seedSupervisorState(`null`), crash: crashC1, errorLine: "TypeError: Cannot set properties of null (setting 'storage_error')"},
		{name: "state/number", seed: seedSupervisorState(`5`), crash: crashC1, errorLine: "TypeError: Cannot create property 'storage_error' on number '5'"},
		{name: "stop-first/null-state", seed: seeds(seedSupervisorState(`null`), stopFile), crash: crashC1, errorLine: "TypeError: Cannot set properties of null (setting 'status')"},
		{name: "stop-first/string-state", seed: seeds(seedSupervisorState(`"x"`), stopFile), crash: crashC1, errorLine: "TypeError: Cannot create property 'status' on string 'x'"},
		{name: "lane/finished-live-pid", seed: seeds(seedWorkerState("a", `{"lane": "a", "status": "finished", "run_id": "run-a", "pid": `+ownPid+`, "harness": "claude"}`), command(0))},
		{name: "lane/running-live-pid", seed: seeds(seedWorkerState("a", `{"lane": "a", "status": "running", "run_id": "run-a", "pid": `+ownPid+`, "harness": "claude"}`), command(0))},
		{name: "flip/awaiting-human", seed: seedFlip(`{"to": "claude", "at": "2026-10-19T10:00:00.000Z", "awaiting_human": {"reason": "push to main"}}`)},
		{name: "flip/awaiting-human-already-logged", seed: seeds(seedFlip(`{"awaiting_human": {"reason": "push"}}`), seedSupervisorState(`{"awaiting_human_logged": true}`))},
		{name: "flip/live-conductor", seed: seeds(seedConductor(`{"status": "running", "harness": "claude", "pid": `+ownPid+`, "started_at": "2026-10-19T10:00:00.000Z"}`), seedFlip(`{"to": "opencode", "paused": true}`))},
		{name: "flip/conductor-lock-held", seed: seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "redispatched_at": "2026-10-19T10:00:00.000Z"}`), seedWorkerLock("conductor"), seedFlip(`{"to": "opencode", "pending_launch": {"to": "opencode"}}`), seedSupervisorState(`{"last_relaunch_at": 4102444800}`))},
		{name: "flip/finished-early-in-cooldown", seed: seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T10:00:00.000Z"}`), seedSupervisorState(`{"last_relaunch_at": 4102444800}`))},
		{name: "flip/never-started-in-backoff", seed: seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T09:00:00.000Z", "redispatched_at": "2026-10-19T09:30:00.000Z"}`), seedFlip(`{"to": "opencode", "at": "2026-10-19T10:00:00.000Z"}`), seedSupervisorState(`{"last_relaunch_at": 4102444800}`))},
		{name: "flip/checked-this-minute", seed: seeds(seedFlip(`{"paused": true}`), seedSupervisorState(`{"last_flip_check": 4102444800}`))},
		{name: "config/null-deferred-lane", seed: seeds(seedSupervisorConfig(`null`), seedWorkerState("d", `{"status": "deferred", "run_id": "rd", "resume_after": "2999-01-01T00:00:00.000Z"}`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h.comparePair(t, h.serveInvocation(c))
		})
	}
	t.Run("live-lock", func(t *testing.T) {
		t.Parallel()
		h.comparePair(t, h.serveInvocation(serveCase{seed: seedProjectFile(".orchestrator/supervisor.lock", strconv.Itoa(os.Getpid()))}))
	})
	t.Run("channel/delivered", func(t *testing.T) {
		t.Parallel()
		h.testServeDelivers(t)
	})
	t.Run("PE-16-null-acks", func(t *testing.T) {
		t.Parallel()
		h.testServeNullAcks(t)
	})
	t.Run("PE-17-no-opencode", func(t *testing.T) {
		t.Parallel()
		h.testServeNoOpencode(t)
	})
	t.Run("PE-16-array-state", func(t *testing.T) {
		t.Parallel()
		h.testServeArrayState(t)
	})
}

// testServeArrayState asserts PE-16(1): with a `[]` state the JS keeps writing the array (its
// named properties are dropped), orch writes an object.
func (h *harness) testServeArrayState(t *testing.T) {
	const state = "project/supervisor-state.json"
	js, orch := h.runPair(t, h.serveInvocation(serveCase{seed: seedSupervisorState(`[]`)}))
	if js.files[state] != "[]\n" || !strings.HasPrefix(orch.files[state], "{\n  \"events\": {},") {
		t.Errorf("js state %q, orch state %q", js.files[state], orch.files[state])
	}
	delete(js.files, state)
	delete(orch.files, state)
	compareOutcomes(t, js, orch)
}

// testSupervisorStart compares `supervisor start` and the serve it launches, then stops that
// serve through the stop file and waits for it to exit.
func (h *harness) testSupervisorStart(t *testing.T) {
	cases := []struct {
		name string
		seed seed
	}{
		{"fresh", nil},
		{"stop-file-removed", seedProjectFile(".orchestrator/supervisor.stop", "")},
		{"stale-lock", seedProjectFile(".orchestrator/supervisor.lock", "999999999")},
		{"already-running", seedProjectFile(".orchestrator/supervisor.lock", strconv.Itoa(os.Getpid())+"\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			args := []string{"start", "--cwd", "."}
			js := h.runStart(t, c.seed, append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, args...))
			orch := h.runStart(t, c.seed, append([]string{h.orchBin, "supervisor"}, args...))
			compareOutcomes(t, js, orch)
		})
	}
	t.Run("keep-awake", func(t *testing.T) {
		t.Parallel()
		h.testStartKeepAwake(t)
	})
}

// testStartKeepAwake: with the fake which finding the wrapper, each runtime launches its own
// serve command through it (the JS runs node with supervisor.mjs, orch itself), so the
// wrapper's argv is checked per runtime and the rest compared.
func (h *harness) testStartKeepAwake(t *testing.T) {
	argvLog := "fakelog/" + keepAwakeWrapper() + "-argv.txt"
	s := seeds(seedFile("fake-which/"+keepAwakeWrapper(), ""), seedHomeDir("fake-log"))
	args := []string{"start", "--cwd", "."}
	js := h.runStart(t, s, append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, args...))
	orch := h.runStart(t, s, append([]string{h.orchBin, "supervisor"}, args...))
	wantJS := physicalLines(h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs"), "serve", "--cwd", "$PROJECT")
	wantOrch := physicalLines(h.orchBin, "supervisor", "serve", "--cwd", "$PROJECT")
	if !strings.Contains(physicalLines(strings.Split(js.files[argvLog], "\n")...), wantJS) ||
		!strings.Contains(physicalLines(strings.Split(orch.files[argvLog], "\n")...), wantOrch) {
		t.Errorf("wrapper argv: js %q, orch %q", js.files[argvLog], orch.files[argvLog])
	}
	delete(js.files, argvLog)
	delete(orch.files, argvLog)
	compareOutcomes(t, js, orch)
}

// physicalLines joins argv one element per line with every existing absolute path resolved
// through its symlinks: a runtime may log its own path resolved (orch logs os.Executable(),
// /private/var/... on macOS) while the harness was given the unresolved /var/... one.
func physicalLines(argv ...string) string {
	physical := make([]string, len(argv))
	for i, arg := range argv {
		physical[i] = arg
		if filepath.IsAbs(arg) {
			if resolved, err := filepath.EvalSymlinks(arg); err == nil {
				physical[i] = resolved
			}
		}
	}
	return strings.Join(physical, "\n")
}

// runStart runs `start` in a fresh sandbox, then stops the serve it launched through the
// stop file and waits for that pid (read from the output this test received) to exit,
// killing it and failing on timeout. The launched pid and the log path are normalised.
func (h *harness) runStart(t *testing.T, s seed, argv []string) outcome {
	t.Helper()
	since := time.Now()
	sb := h.newSandbox(t)
	if s != nil {
		s(t, sb)
	}
	o := h.run(t, sb, argv, nil, []string{noPostRunRefresh})
	var launched struct {
		Started bool `json:"started"`
		Pid     int  `json:"supervisor_pid"`
	}
	if err := json.Unmarshal([]byte(o.stdout), &launched); err != nil {
		t.Fatalf("start output %q: %v", o.stdout, err)
	}
	if launched.Started {
		if launched.Pid <= 0 {
			t.Fatalf("start launched no pid: %q", o.stdout)
		}
		h.stopLaunched(t, sb, launched.Pid)
		o.stdout = strings.Replace(o.stdout, `"supervisor_pid":`+strconv.Itoa(launched.Pid), `"supervisor_pid":<pid>`, 1)
	}
	o.files = sb.files(t)
	return o.withProjectPlaceholder(t, sb).normalized(normalizeServe(since))
}

// stopLaunched waits for the launched serve to hold its lock and tick, writes the stop file
// and waits for pid to exit; on timeout it kills pid and fails.
func (h *harness) stopLaunched(t *testing.T, sb sandbox, pid int) {
	t.Helper()
	dir := filepath.Join(sb.project, ".orchestrator")
	never := make(chan error)
	waitForLock(filepath.Join(dir, "supervisor.lock"), pid, never)
	time.Sleep(serveTickTime)
	if err := os.WriteFile(filepath.Join(dir, "supervisor.stop"), nil, 0o644); err != nil {
		t.Error(err)
	}
	deadline := time.Now().Add(serveExitWait)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("launched serve %d did not stop; killed", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// testServeNullAcks asserts PE-16(2): with acks holding null and a failed event due for a
// retry, the JS tick throws at the ack lookup (storage_error, the event stays failed), orch
// reads no acks and delivers it.
func (h *harness) testServeNullAcks(t *testing.T) {
	const state = "project/supervisor-state.json"
	failed := seedSupervisorState(`{"events": {"run-a:finished": {"status": "delivery_failed", "reason": "finished", "lane": "a", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1}}}`)
	js, orch := h.runPair(t, h.serveInvocation(serveCase{seed: seeds(finishedLane("a"), seedWake("["+commandChannel(0)+"]"), seedAcks("null"), failed)}))
	if !strings.Contains(js.files[state], `"storage_error": "TypeError: Cannot read properties of null`) || !strings.Contains(js.files[state], `"status": "delivery_failed"`) {
		t.Errorf("js state %s", js.files[state])
	}
	if strings.Contains(orch.files[state], "storage_error") || !strings.Contains(orch.files[state], `"run-a:finished": {
      "status": "queued"`) {
		t.Errorf("orch state %s", orch.files[state])
	}
	if js.exitCode != 0 || orch.exitCode != 0 {
		t.Errorf("exit js %d, orch %d", js.exitCode, orch.exitCode)
	}
}

// testServeNoOpencode asserts PE-17: with no opencode on PATH, the JS records the tick and
// dies of the spawn error (exit 1, lock left, state still running); orch records the same
// and serves until the stop. Everything else is compared.
func (h *harness) testServeNoOpencode(t *testing.T) {
	const (
		state = "project/supervisor-state.json"
		lock  = "project/supervisor.lock"
	)
	inv := h.serveInvocation(serveCase{seed: seeds(finishedLane("a"), seedWake(`[{"type": "opencode-run", "session": "ses-1"}]`))})
	inv.env = append(inv.env, "PATH="+h.pathWithout(t, "opencode"))
	js, orch := h.runPair(t, inv)
	if js.exitCode != 1 || !strings.Contains(js.stderr, "Error: spawn opencode ENOENT") || js.files[lock] != "<pid>" {
		t.Errorf("js: exit %d, stderr %q, lock %q", js.exitCode, js.stderr, js.files[lock])
	}
	if orch.exitCode != 0 || orch.stderr != "" || orch.files[lock] != "" {
		t.Errorf("orch: exit %d, stderr %q, lock %q", orch.exitCode, orch.stderr, orch.files[lock])
	}
	stopped := "\"status\": \"stopped\",\n  \"stopped_at\": \"<iso>\"\n"
	if !strings.Contains(orch.files[state], stopped) {
		t.Errorf("orch state %s", orch.files[state])
	}
	orch.files[state] = strings.Replace(orch.files[state], stopped, "\"status\": \"running\"\n", 1)
	delete(js.files, lock)
	delete(orch.files, "project/supervisor.stop") // the harness stops orch; the JS was dead by then
	compareOutcomes(t, outcome{exitCode: 0, files: js.files}, outcome{exitCode: orch.exitCode, stdout: orch.stdout, files: orch.files})
}

// testServeDelivers guards the one-tick method itself: the due event of a finished lane
// reaches the command channel, the mailbox and the log in both runtimes (so the compared
// cases above are not comparing two idle serves).
func (h *harness) testServeDelivers(t *testing.T) {
	js, orch := h.runPair(t, h.serveInvocation(serveCase{seed: seeds(finishedLane("a"), seedWake("["+commandChannel(0)+"]"))}))
	for name, o := range map[string]outcome{"js": js, "orch": orch} {
		if !strings.Contains(o.files["fakelog/command.txt"], "run-a:finished") || !strings.Contains(o.files["project/logs/supervisor.log"], "run-a:finished queued") || o.files["project/mailbox/messages.jsonl"] == "" {
			t.Errorf("%s did not deliver: %v", name, o.files)
		}
	}
	compareOutcomes(t, js, orch)
}
