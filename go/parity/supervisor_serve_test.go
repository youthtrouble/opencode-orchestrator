//go:build parity

package parity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
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
	return h.runDaemonWithin(t, sb, argv, stdin, env, serveExitWait)
}

// runDaemonWithin is runDaemon with the serve given exitWait to stop.
func (h *harness) runDaemonWithin(t *testing.T, sb sandbox, argv []string, stdin []byte, env []string, exitWait time.Duration) outcome {
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
	case <-time.After(exitWait):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatalf("%s did not stop within %s; killed %d", argv[0], exitWait, cmd.Process.Pid)
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
	// evaluatesFlip: the tick must reach the flip evaluation and finish (both runtimes stamp
	// last_flip_check and record no storage error), so the case cannot pass on a tick that
	// threw before the branch it names.
	evaluatesFlip bool
}

func flipEvalCase(name string, s seed) serveCase {
	return serveCase{name: "flip/" + name, seed: s, evaluatesFlip: true}
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
		// A lock a helper holds names the helper's pid, which differs per sandbox.
		normalizeWrites: func(o outcome, _ map[string]string) outcome { return withLockHolderPids(o) },
		daemon:          true,
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

// seedConductor is a conductor lane in a project whose logs/ exists, as start leaves it:
// without logs/ the lane's own event throws at the log append and the tick never reaches the
// flip evaluation.
func seedConductor(content string) seed {
	return seeds(seedWorkerState("conductor", content), seedProjectDir(".orchestrator/logs"))
}

// runningLane is lane "a" running in this test process (alive in both runtimes) with the
// given breadth and last progress.
func runningLane(breadth, lastProgress string) seed {
	return seedWorkerState("a", `{"lane": "a", "status": "running", "run_id": "run-a", "pid": `+ownPid+`, "harness": "claude", "breadth": "`+breadth+`", "last_progress_at": "`+lastProgress+`"}`)
}

// progressAgo is the time minutes before the cases are built, as a worker records progress.
func progressAgo(minutes int) string {
	return time.Now().Add(-time.Duration(minutes) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
}

// inCooldown is a relaunch recorded in 2100: every relaunch cooldown and backoff is running.
var inCooldown = seedSupervisorState(`{"last_relaunch_at": 4102444800}`)

// handoffSince is a mailbox holding one handoff message posted at the given time.
func handoffSince(at string) seed {
	return seedProjectFile(".orchestrator/mailbox/messages.jsonl", `{"id":"m1","at":"`+at+`","from":"conductor-claude","to":"supervisor","kind":"handoff","subject":"s"}`+"\n")
}

// testSupervisorServe compares one tick of `orch supervisor serve` with bin/supervisor.mjs.
func (h *harness) testSupervisorServe(t *testing.T) {
	command := func(code int) seed { return seedWake("[" + commandChannel(code) + "]") }
	caches := newFlipCaches(t)
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
		{name: "lane/stale-lock", seed: seeds(seedWorkerState("f", `{"status": "finished", "run_id": "rf", "pid": 999999999}`), seedHeldLock(workerLockPath("f")), command(0))},
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
		// spawnSync's ENOBUFS (PE-20): both runtimes stop the channel at 1 MiB of output and
		// record a failed delivery, exit null.
		{name: "channel/command-overflow", seed: seeds(finishedLane("a"), seedWake(`[{"type": "command", "argv": ["/bin/sh", "-c", "exec cat /dev/zero"]}]`))},
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
		flipEvalCase("awaiting-human", seeds(seedFlip(`{"to": "claude", "at": "2026-10-19T10:00:00.000Z", "awaiting_human": {"reason": "push to main"}}`), seedProjectDir(".orchestrator/logs"))),
		flipEvalCase("awaiting-human-already-logged", seeds(seedFlip(`{"awaiting_human": {"reason": "push"}}`), seedSupervisorState(`{"awaiting_human_logged": true}`))),
		flipEvalCase("live-conductor", seeds(seedConductor(`{"status": "running", "harness": "claude", "pid": `+ownPid+`, "started_at": "2026-10-19T10:00:00.000Z"}`), seedFlip(`{"to": "opencode", "paused": true}`))),
		flipEvalCase("conductor-lock-held", seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "redispatched_at": "2026-10-19T10:00:00.000Z"}`), seedHeldLock(workerLockPath("conductor")), seedFlip(`{"to": "opencode", "pending_launch": {"to": "opencode"}}`), inCooldown, caches.green)),
		// decide reads a green cache and stays, so only the cooldown keeps the finished-early
		// lane from a relaunch (without a cache decide pauses and the cooldown is never reached).
		flipEvalCase("finished-early-in-cooldown", seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T10:00:00.000Z"}`), inCooldown, caches.green)),
		flipEvalCase("never-started-in-backoff", seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T09:00:00.000Z", "redispatched_at": "2026-10-19T09:30:00.000Z"}`), seedFlip(`{"to": "opencode", "at": "2026-10-19T10:00:00.000Z"}`), inCooldown, caches.green)),
		{name: "flip/checked-this-minute", seed: seeds(seedFlip(`{"paused": true}`), seedSupervisorState(`{"last_flip_check": 4102444800}`))},
		// A paused project relaunches its door once decide stays; the cooldown holds it back.
		flipEvalCase("paused-in-cooldown", seeds(seedFlip(`{"paused": true}`), inCooldown, caches.green)),
		// decide still pauses (both vendors red): no relaunch target, cooldown or not.
		flipEvalCase("paused-both-red", seeds(seedFlip(`{"paused": true}`), caches.bothRed)),
		// decide flips the idle project to claude; the cooldown holds the launch back.
		flipEvalCase("decide-flip-in-cooldown", seeds(seedFlip(`{"to": "opencode", "at": "2026-10-19T10:00:00.000Z"}`), inCooldown, caches.openaiRed)),
		// The finished conductor handed off (a handoff message since it started): decide stays
		// and nothing needs a restart, so no relaunch even out of the cooldown.
		flipEvalCase("handed-off", seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T10:00:00.000Z"}`), handoffSince("2026-10-19T10:00:00.000Z"), caches.green)),
		// A handoff before the lane started does not count: the lane finished early, in cooldown.
		flipEvalCase("handoff-before-start", seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T10:00:00.000Z"}`), handoffSince("2026-10-19T09:00:00.000Z"), inCooldown, caches.green)),
		// The flip names a door flip run refuses: the never-started launch fails (exit 2, logged)
		// and still stamps last_relaunch_at.
		flipEvalCase("never-started-to-gemini", seeds(seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T09:00:00.000Z"}`), seedFlip(`{"to": "gemini", "at": "2026-10-19T10:00:00.000Z"}`))),
		// A pending launch to gemini: flip run refuses it, the refusal is logged.
		flipEvalCase("pending-launch-to-gemini", seeds(seedFlip(`{"pending_launch": {"to": "gemini", "from": "claude", "reason": "R"}}`), seedProjectDir(".orchestrator/logs"))),
		// Running lanes (pid alive), progress minutes ago: margins of at least ten minutes keep
		// each on its side of the quiet threshold (max(600 s, 3 cadences)) and its 30-min bucket.
		{name: "running/quiet-standard", seed: seeds(runningLane("standard", progressAgo(45)), command(0))},
		{name: "running/quiet-focused", seed: seeds(runningLane("focused", progressAgo(20)), command(0))},
		{name: "running/not-quiet-check-due", seed: seeds(runningLane("standard", progressAgo(5)), command(0), seedSupervisorState(`{"lanes": {"a": {"run": "run-a", "next_check": 1}}}`))},
		{name: "running/not-quiet-check-not-due", seed: seeds(runningLane("standard", progressAgo(5)), command(0))},
		{name: "running/quiet-already-delivered", seed: seeds(runningLane("broad", progressAgo(60)), command(0), seedSupervisorState(`{"events": {"run-a:check:1": {"status": "queued", "reason": "check", "lane": "a", "created_at": "2999-01-01T00:00:00.000Z"}}}`))},
		{name: "config/null-deferred-lane", seed: seeds(seedSupervisorConfig(`null`), seedWorkerState("d", `{"status": "deferred", "run_id": "rd", "resume_after": "2999-01-01T00:00:00.000Z"}`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if !c.evaluatesFlip {
				h.comparePair(t, h.serveInvocation(c))
				return
			}
			js, orch := h.runPair(t, h.serveInvocation(c))
			requireFlipEvaluated(t, "js", js)
			requireFlipEvaluated(t, "orch", orch)
			compareOutcomes(t, js, orch)
		})
	}
	t.Run("launch", func(t *testing.T) {
		t.Parallel()
		h.testServeLaunches(t, caches)
	})
	t.Run("channel/claude-resume", func(t *testing.T) {
		t.Parallel()
		h.testServeClaudeResume(t)
	})
	t.Run("restart", h.testServeRestart)
	t.Run("slow/paused-uncached-decide", func(t *testing.T) {
		t.Parallel()
		h.testServePausedUncachedDecide(t)
	})
	t.Run("live-lock", func(t *testing.T) {
		t.Parallel()
		h.comparePair(t, h.serveInvocation(serveCase{seed: seedHeldLock(".orchestrator/supervisor.lock")}))
	})
	t.Run("PE-30-unheld-live-pid-lock", func(t *testing.T) {
		t.Parallel()
		h.testServePE30UnheldSupervisorLock(t)
	})
	t.Run("PE-30-unheld-lane-lock", func(t *testing.T) {
		t.Parallel()
		h.testServePE30UnheldLaneLock(t)
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

// testServePE30UnheldSupervisorLock asserts PE-30 for serve: a supervisor.lock naming a live
// pid (this test's) that no process holds is refused by the JS (pinned) and claimed by orch,
// which ticks and stops.
func (h *harness) testServePE30UnheldSupervisorLock(t *testing.T) {
	js, orch := h.runPair(t, h.serveInvocation(serveCase{seed: seedProjectFile(".orchestrator/supervisor.lock", ownPid)}))
	// The lock's pid is normalised to <pid>; its presence shows the JS left the seeded lock.
	if _, locked := js.files["project/supervisor.lock"]; js.exitCode != 1 || js.stderr != "a supervisor already owns this project\n" || !locked {
		t.Errorf("js: exit %d, stderr %q, lock left %v; want the pinned refusal", js.exitCode, js.stderr, locked)
	}
	_, locked := orch.files["project/supervisor.lock"]
	if orch.exitCode != 0 || orch.stderr != "" || locked || !strings.Contains(orch.files["project/supervisor-state.json"], `"status": "stopped"`) {
		t.Errorf("orch: exit %d, stderr %q, lock left %v, state %q; want a claimed, ticked and stopped serve", orch.exitCode, orch.stderr, locked, orch.files["project/supervisor-state.json"])
	}
}

// testServePE30UnheldLaneLock asserts PE-30 for a lane: a finished lane whose lock file no
// runner holds (pid text only, a runner killed with SIGKILL) is stale_lock in the JS (pinned)
// and finished in orch.
func (h *harness) testServePE30UnheldLaneLock(t *testing.T) {
	const state = "project/supervisor-state.json"
	c := serveCase{seed: seeds(seedWorkerState("f", `{"status": "finished", "run_id": "rf", "pid": 999999999}`), seedWorkerLock("f"), seedWake("["+commandChannel(0)+"]"))}
	js, orch := h.runPair(t, h.serveInvocation(c))
	if !strings.Contains(js.files[state], `"phase": "stale_lock"`) {
		t.Errorf("js state %q, want the pinned stale_lock phase", js.files[state])
	}
	if !strings.Contains(orch.files[state], `"phase": "finished"`) || strings.Contains(orch.files[state], "stale_lock") {
		t.Errorf("orch state %q, want the finished phase", orch.files[state])
	}
}

// requireFlipEvaluated fails the case unless the run's tick reached the flip evaluation and
// completed: last_flip_check is the tick's time and no storage error was recorded.
func requireFlipEvaluated(t *testing.T, side string, o outcome) {
	t.Helper()
	state := o.files["project/supervisor-state.json"]
	if !strings.Contains(state, `"last_flip_check": <now+0>`) || strings.Contains(state, "storage_error") {
		t.Errorf("%s: the tick did not complete a flip evaluation: %s", side, state)
	}
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
		{"already-running", seedHeldLock(".orchestrator/supervisor.lock")},
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
	t.Run("PE-30-unheld-live-pid-lock", func(t *testing.T) {
		t.Parallel()
		h.testStartPE30UnheldLock(t)
	})
}

// testStartPE30UnheldLock asserts PE-30 for start: a supervisor.lock naming a live pid (this
// test's) that no process holds is "already running" in the JS (pinned) and a gone supervisor
// in orch, which launches a serve (stopped by runStart).
func (h *harness) testStartPE30UnheldLock(t *testing.T) {
	s := seedProjectFile(".orchestrator/supervisor.lock", ownPid+"\n")
	args := []string{"start", "--cwd", "."}
	js := h.runStart(t, s, append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, args...))
	orch := h.runStart(t, s, append([]string{h.orchBin, "supervisor"}, args...))
	if want := `{"started":false,"reason":"already running","pid":` + ownPid + "}\n"; js.stdout != want {
		t.Errorf("js stdout %q, want the pinned %q", js.stdout, want)
	}
	if !strings.HasPrefix(orch.stdout, `{"started":true,"supervisor_pid":<pid>,`) {
		t.Errorf("orch stdout %q, want a launched serve", orch.stdout)
	}
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
		if launched.Pid < 2 {
			t.Fatalf("start launched no usable pid: %q", o.stdout)
		}
		h.stopLaunched(t, sb, launched.Pid)
		o.stdout = strings.Replace(o.stdout, `"supervisor_pid":`+strconv.Itoa(launched.Pid), `"supervisor_pid":<pid>`, 1)
	}
	o.stdout = withSupervisorHolder(o.stdout)
	o.files = sb.files(t)
	return withLockHolderPids(o.withProjectPlaceholder(t, sb)).normalized(normalizeServe(since))
}

// stopLaunched waits for the launched serve to hold its lock and tick, writes the stop file
// and waits for pid to exit; on timeout it kills pid and fails. It refuses pids below 2, which
// would signal init or every process the user owns (S3/R13).
func (h *harness) stopLaunched(t *testing.T, sb sandbox, pid int) {
	t.Helper()
	if pid < 2 {
		t.Fatalf("refusing to stop pid %d", pid)
	}
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

// testServeLaunches compares serves that launch a detached worker and settles its runner
// before the files are compared: flip evaluations that launch the conductor (`flip run` over
// the opencode bridge) and the re-dispatch of a deferred lane (`worker start` over claude).
func (h *harness) testServeLaunches(t *testing.T, caches flipCaches) {
	conductorOn := func(b bridge, run bridgeCase, s seed) func(*testing.T) invocation {
		return func(t *testing.T) invocation {
			inv := h.flipLaunchInvocation(t, flipLaunchCase{bridge: b, run: run})
			inv.seed = seeds(inv.seed, s)
			return inv
		}
	}
	conductor := func(s seed) func(*testing.T) invocation {
		return conductorOn(opencodeBridge, bridgeCase{script: opencodeRunOK}, s)
	}
	claudeConductor := func(s seed) func(*testing.T) invocation {
		return conductorOn(claudeBridge, bridgeCase{fixture: "ok"}, s)
	}
	redispatch := func(t *testing.T) invocation {
		inv := h.workerStartInvocation(t, workerStartCase{run: bridgeCase{fixture: "ok"}})
		inv.seed = seeds(inv.seed, seedWorkerState("d", `{"status": "deferred", "run_id": "rd", "resume_after": "2020-01-01T00:00:00.000Z", "brief": "b.md", "harness": "claude"}`))
		return inv
	}
	cases := []struct {
		name, lane string
		inv        func(*testing.T) invocation
		want       []string // in the supervisor log, the mailbox or the fake claude's argv
	}{
		// decide stays and the project is paused: its own door is relaunched.
		{"conductor/paused-relaunch", "conductor", conductor(seeds(seedFlip(`{"paused": true}`), caches.green)), []string{"relaunch opencode->opencode (stay, was paused)"}},
		// A hand-off recorded while the previous conductor held the lane: the target starts.
		{"conductor/pending-launch", "conductor", conductor(seedFlip(`{"to": "opencode", "pending_launch": {"to": "opencode", "from": "claude", "reason": "handback: R"}}`)), []string{"pending launch claude->opencode"}},
		// A human gate cleared after the conductor stopped at it (its lane finished with the
		// gate's hand-off, so no restart is due): the pending launch `gate --clear` queued
		// relaunches the gated door.
		{"conductor/gate-cleared", "conductor", conductor(seeds(
			seedConductor(`{"status": "finished", "harness": "opencode", "pid": 0, "started_at": "2026-10-19T09:00:00.000Z"}`),
			seedMailbox(lines(`{"id":"gggggggg-0000-0000-0000-000000000001","at":"2026-10-19T09:30:00.000Z","from":"conductor-opencode","to":"human","kind":"handoff","subject":"awaiting human: push","body":"","refs":[],"thread":null,"acked_by":[]}`), ""),
			seedFlip(`{"to": "opencode", "at": "2026-10-19T08:00:00.000Z", "pending_launch": {"to": "opencode", "from": "opencode", "reason": "human gate cleared", "resume": null, "at": "2026-10-19T10:00:00.000Z"}}`), caches.green)),
			[]string{"pending launch opencode->opencode"}},
		// Timing assumption: the serve's write of its stale in-memory lane lands before the
		// runner's first state write, because the runner boots far slower, so the runner's own
		// final state replaces the redispatched_at the serve wrote. Under heavy load the order
		// can flip, in both runtimes alike.
		// decide flips the idle opencode project (flipped there long ago) to claude, openai
		// being red: claude is launched.
		{"conductor/decide-flip-to-claude", "conductor", claudeConductor(seeds(seedFlip(`{"to": "opencode", "at": "2026-10-19T09:00:00.000Z"}`), caches.openaiRed)), []string{"relaunch opencode->claude (flip)"}},
		// The claude conductor finished without a hand-off; decide stays on claude, so the lane
		// is restarted on its own door with --resume of its session.
		{"conductor/finished-early-resume", "conductor", claudeConductor(seeds(
			seedConductor(`{"status": "finished", "harness": "claude", "pid": 0, "started_at": "2026-10-19T10:00:00.000Z", "session_id": "00000000-0000-4000-8000-0000000000aa"}`),
			seedFlip(`{"to": "claude", "at": "2026-10-19T09:00:00.000Z"}`), caches.green)),
			[]string{"relaunch claude->claude (stay)", "--resume\n00000000-0000-4000-8000-0000000000aa\n"}},
		// The flip to claude was recorded after the opencode conductor started, and that lane
		// finished: the target never started, so it is launched.
		{"conductor/never-started", "conductor", claudeConductor(seeds(
			seedConductor(`{"status": "finished", "harness": "opencode", "pid": 0, "started_at": "2026-10-19T09:00:00.000Z"}`),
			seedFlip(`{"to": "claude", "at": "2026-10-19T10:00:00.000Z"}`))),
			[]string{"flip target never started: launching claude"}},
		{"redispatch/deferred-due", "d", redispatch, []string{"d re-dispatched after budget reset"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inv := c.inv(t)
			inv.seed = seeds(inv.seed, seedProjectDir(".orchestrator/logs"))
			inv.env = append(inv.env, noPostRunRefresh)
			launched, serve := inv.normalize, normalizeServe(time.Now())
			inv.normalize = func(s string) string { return serve(launched(s)) }
			serveArgs := []string{"serve", "--cwd", "."}
			runners := strings.NewReplacer(h.runnerPrefixes(t)...)
			js, jsQuoted := h.runServeLaunch(t, inv, append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, serveArgs...), c.lane, runners)
			orch, orchQuoted := h.runServeLaunch(t, inv, append([]string{h.orchBin, "supervisor"}, serveArgs...), c.lane, runners)
			for side, o := range map[string]outcome{"js": js, "orch": orch} {
				seen := o.files["project/logs/supervisor.log"] + o.files["project/mailbox/messages.jsonl"] + o.files["fakelog/claude-argv.txt"]
				for _, want := range c.want {
					if !strings.Contains(seen, want) {
						t.Errorf("%s: no %q in the log, mailbox or claude argv", side, want)
					}
				}
			}
			if c.lane == "conductor" {
				requireFlipEvaluated(t, "js", js)
				requireFlipEvaluated(t, "orch", orch)
				if len(jsQuoted) == 0 {
					t.Error("js: the supervisor log quotes no launch answer")
				}
			}
			// Only conductor launches quote an answer in the log, behind logCut's mask; a
			// re-dispatch quotes `worker start` in its mailbox body, which no mask hides from
			// compareOutcomes, so the other lanes need no non-empty guard.
			if !reflect.DeepEqual(jsQuoted, orchQuoted) {
				t.Errorf("the serves quote launch answers of different lengths: js %v, orch %v", jsQuoted, orchQuoted)
			}
			js, orch = js.normalized(cutLogPaths), orch.normalized(cutLogPaths)
			compareOutcomes(t, js, orch)
		})
	}
}

// quotedAnswerLengths lists, in UTF-16 units, how much of each launch answer the supervisor log
// quotes, so a difference in where the serves cut the answer cannot hide behind logCut's mask.
// It must see the raw log: the runner's pid is among the units kept before the log path, so
// the masked tails alone differ whenever the two runners' pids differ in digit count
// (incident 067f540da276).
func quotedAnswerLengths(rawLog string) []int {
	var lengths []int
	for _, m := range quotedAnswer.FindAllStringSubmatch(rawLog, -1) {
		lengths = append(lengths, jsstr.Len(m[1]))
	}
	return lengths
}

// quotedAnswer is a raw supervisor log line quoting a conductor launch; group 1 is `flip
// run`'s answer as the serve kept it.
var quotedAnswer = regexp.MustCompile(`(?m)^\S+ (?:relaunch|pending launch|flip target never started:) [^\n]*?: (\{[^\n]*)$`)

func cutLogPaths(s string) string { return logCut.ReplaceAllString(s, "${1}<cut>") }

// logCut is the end of a supervisor log line quoting a conductor launch: the serve keeps the
// first 200 characters of `flip run`'s answer, which cut the sandbox's log path where its
// length puts it. Runs after the serve normaliser has rewritten the line's timestamp.
var logCut = regexp.MustCompile(`(?m)^(<iso> (?:relaunch|pending launch|flip target never started:) [^\n]*\\"log_path\\":\\")([^\n]*)$`)

// runServeLaunch runs one runtime's serve in a fresh sandbox, waits for the lane it launched
// to name its runner, settles that runner and collects every file; the launch's fresh values
// are rewritten as for `worker start --detach`. It also returns the quotedAnswerLengths of the
// raw supervisor log.
func (h *harness) runServeLaunch(t *testing.T, inv invocation, argv []string, lane string, runners *strings.Replacer) (outcome, []int) {
	t.Helper()
	sb := h.newSandbox(t)
	inv.seed(t, sb)
	before := map[string]string{}
	collectFiles(t, filepath.Join(sb.project, ".orchestrator"), "project", before)
	o := h.runDaemon(t, sb, argv, nil, inv.env)
	l := launchedRunner(t, argv[0], sb, lane)
	settle(t, l.RunnerPid)
	o.files = sb.files(t)
	requireLaunchRecorded(t, argv[0], l, o.files, true)
	quoted := quotedAnswerLengths(o.files["project/logs/supervisor.log"])
	collectExtraFiles(t, sb, inv, o.files)
	o = o.withLaunch(l, runners)
	o = o.withProjectPlaceholder(t, sb)
	o = h.withPE19(t, argv, o, inv.jsHelp)
	o = inv.normalizeWrites(o, before)
	return o.normalized(inv.normalize), quoted
}

// launchedRunner polls the lane's state until it names its runner, run id and log.
func launchedRunner(t *testing.T, side string, sb sandbox, lane string) launch {
	t.Helper()
	state := filepath.Join(sb.project, ".orchestrator", "workers", lane+".json")
	deadline := time.Now().Add(settleTimeout)
	for {
		var recorded struct {
			Pid     int    `json:"pid"`
			RunID   string `json:"run_id"`
			LogPath string `json:"log_path"`
		}
		data, err := os.ReadFile(state)
		if err == nil && json.Unmarshal(data, &recorded) == nil && recorded.Pid > 1 && recorded.LogPath != "" {
			return launch{RunnerPid: recorded.Pid, RunID: recorded.RunID, LogPath: recorded.LogPath}
		}
		if time.Now().After(deadline) {
			serveLog, _ := os.ReadFile(filepath.Join(sb.project, ".orchestrator", "logs", "supervisor.log"))
			t.Fatalf("%s: the serve launched no %s runner; state %q (%v); supervisor log %q", side, lane, data, err, serveLog)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// testServeClaudeResume compares the claude-resume wake channel end to end: the serve starts
// claude-exec detached with --resume, which runs the fake claude scripted to finish. Its run
// record has no pid to settle on, so the files are collected once the record exists and the
// sandbox has stopped changing.
func (h *harness) testServeClaudeResume(t *testing.T) {
	inv := h.bridgeInvocation(t, claudeBridge, bridgeCase{fixture: "ok"})
	inv.seed = seeds(inv.seed, finishedLane("a"), seedWake(`[{"type": "claude-resume", "session": "00000000-0000-4000-8000-0000000000aa"}]`))
	bridge, serve := inv.normalize, normalizeServe(time.Now())
	inv.normalize = func(s string) string { return serve(bridge(s)) }
	run := func(argv []string) outcome {
		sb := h.newSandbox(t)
		inv.seed(t, sb)
		before := map[string]string{}
		collectFiles(t, filepath.Join(sb.project, ".orchestrator"), "project", before)
		o := h.runDaemon(t, sb, argv, nil, inv.env)
		o.files = quietFilesWithRunRecord(t, argv[0], sb)
		reapClaudeExec(t, argv[0], sb)
		withWakePromptDigest(t, argv[0], o.files)
		o = o.withProjectPlaceholder(t, sb)
		o = h.withPE19(t, argv, o, inv.jsHelp)
		o = inv.normalizeWrites(o, before)
		return o.normalized(inv.normalize)
	}
	serveArgs := []string{"serve", "--cwd", "."}
	js := run(append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, serveArgs...))
	orch := run(append([]string{h.orchBin, "supervisor"}, serveArgs...))
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if !strings.Contains(o.files["fakelog/claude-argv.txt"], "--resume\n00000000-0000-4000-8000-0000000000aa\n") {
			t.Errorf("%s: the fake claude was not resumed: %q", side, o.files["fakelog/claude-argv.txt"])
		}
	}
	compareOutcomes(t, js, orch)
}

// quietFilesWithRunRecord waits until the project holds a run record and the sandbox's files
// are the same over quietPeriod, then returns them; past settleTimeout it fails the case.
func quietFilesWithRunRecord(t *testing.T, side string, sb sandbox) map[string]string {
	t.Helper()
	const quietPeriod = 300 * time.Millisecond
	deadline := time.Now().Add(settleTimeout)
	previous := sb.files(t)
	for {
		time.Sleep(quietPeriod)
		current := sb.files(t)
		if hasRunRecord(current) && reflect.DeepEqual(previous, current) {
			return current
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: no settled run record after %v: %v", side, settleTimeout, current)
		}
		previous = current
	}
}

// reapClaudeExec waits for the detached claude-exec the serve started in the sandbox to exit,
// so it cannot outlive the sandbox; past claudeExecExitTimeout it kills its process group (it
// was spawned detached, so it leads one) and fails the case.
func reapClaudeExec(t *testing.T, side string, sb sandbox) {
	t.Helper()
	const claudeExecExitTimeout = 5 * time.Second
	deadline := time.Now().Add(claudeExecExitTimeout)
	for {
		pids := claudeExecPids(t, sb)
		if len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			for _, pid := range pids {
				if pid < 2 { // -1 or 0 would signal far beyond this sandbox (S3/R13)
					t.Errorf("%s: refusing to kill claude-exec pid %d", side, pid)
					continue
				}
				_ = syscall.Kill(-pid, syscall.SIGKILL) // ESRCH when pid leads no group
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			t.Errorf("%s: claude-exec %v still running after %v; it and its group were killed", side, pids, claudeExecExitTimeout)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// claudeExecPids lists the claude-exec processes whose argv names the sandbox project; the
// JS runs `claude-exec.mjs --cwd <project>`, orch `orch claude-exec --cwd <project>` (macOS may
// show the project under /private, which still contains the sandbox path).
func claudeExecPids(t *testing.T, sb sandbox) []int {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		pid, command, _ := strings.Cut(strings.TrimSpace(line), " ")
		if !strings.Contains(command, "claude-exec") || !strings.Contains(command, sb.project) {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// withWakePromptDigest checks that the run record's prompt_sha256 is the digest of the wake
// text the serve posted to the mailbox, then rewrites it: the text names the sandbox project,
// so the digest differs between the runtimes' sandboxes.
func withWakePromptDigest(t *testing.T, side string, files map[string]string) {
	t.Helper()
	var wake struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(files["project/mailbox/messages.jsonl"], "\n", 2)[0]), &wake); err != nil {
		t.Fatalf("%s: wake message: %v", side, err)
	}
	digest := sha256.Sum256([]byte(wake.Body))
	field := `"prompt_sha256": "` + hex.EncodeToString(digest[:]) + `"`
	for name, content := range files {
		if strings.HasPrefix(name, "project/runs/") {
			if !strings.Contains(content, field) {
				t.Errorf("%s: %s does not record the wake text's digest (%s)", side, name, field)
			}
			files[name] = strings.Replace(content, field, `"prompt_sha256": "<wake text digest>"`, 1)
		}
	}
}

func hasRunRecord(files map[string]string) bool {
	for name := range files {
		if strings.HasPrefix(name, "project/runs/") && strings.HasSuffix(name, ".json") {
			return true
		}
	}
	return false
}

// testServeRestart compares a restart across runtimes: in one sandbox a serve ticks and stops,
// then a second serve starts on the state, acks and logs the first left. A second serve of the
// other runtime must leave exactly what a second serve of the same runtime leaves.
func (h *harness) testServeRestart(t *testing.T) {
	serveArgs := []string{"serve", "--cwd", "."}
	runtimes := map[string][]string{
		"js":   append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, serveArgs...),
		"orch": append([]string{h.orchBin, "supervisor"}, serveArgs...),
	}
	other := map[string]string{"js": "orch", "orch": "js"}
	// The first serve delivers the finished lane's event; the second must not deliver it again.
	s := seeds(finishedLane("a"), seedWake("["+commandChannel(0)+"]"))
	for first := range runtimes {
		t.Run("after-"+first, func(t *testing.T) {
			t.Parallel()
			same := h.runServes(t, s, runtimes[first], runtimes[first])
			switched := h.runServes(t, s, runtimes[first], runtimes[other[first]])
			compareOutcomes(t, same, switched)
		})
	}
}

// runServes runs one serve after the other in a fresh sandbox, each ticking and stopped, with
// the stop file removed in between as start does. It returns the second's outcome, after
// checking that the second serve ticked (its checked_at is not the first's).
func (h *harness) runServes(t *testing.T, s seed, first, second []string) outcome {
	t.Helper()
	since := time.Now()
	sb := h.newSandbox(t)
	s(t, sb)
	dir := filepath.Join(sb.project, ".orchestrator")
	h.runDaemon(t, sb, first, nil, []string{noPostRunRefresh})
	firstTick := checkedAt(t, dir)
	if err := os.Remove(filepath.Join(dir, "supervisor.stop")); err != nil {
		t.Fatal(err)
	}
	o := h.runDaemon(t, sb, second, nil, []string{noPostRunRefresh})
	if secondTick := checkedAt(t, dir); secondTick == firstTick {
		t.Errorf("%s after %s: the second serve did not tick (checked_at %s)", second[0], first[0], secondTick)
	}
	return o.withProjectPlaceholder(t, sb).normalized(normalizeServe(since))
}

// checkedAt is the state's checked_at, the time of the last tick.
func checkedAt(t *testing.T, dir string) string {
	t.Helper()
	var state struct {
		CheckedAt string `json:"checked_at"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "supervisor-state.json"))
	if err != nil || json.Unmarshal(data, &state) != nil || state.CheckedAt == "" {
		t.Fatalf("state after a serve: %q (%v)", data, err)
	}
	return state.CheckedAt
}

// collectedAt is when a usage collection wrote usage.json.
var collectedAt = regexp.MustCompile(`("generated_at": ")` + isoMillis.String() + `(")`)

// slowParity opts into the cases that take most of a minute (ORCH_PARITY_SLOW=1).
var slowParity = os.Getenv("ORCH_PARITY_SLOW") == "1"

// testServePausedUncachedDecide (slow, opt-in) compares a paused project's tick when decide must
// collect usage: the unscripted fake codex app-server exits at once, but both runtimes' usage
// readers wait out their 20 s app-server timeout (incident 5f5f6ff3449b), so the serve holds
// the stop file for about 25 s. decide then pauses (usage unknown): no relaunch.
func (h *harness) testServePausedUncachedDecide(t *testing.T) {
	if !slowParity {
		t.Skip("slow: set ORCH_PARITY_SLOW=1")
	}
	const exitWait = 45 * time.Second
	inv := h.serveInvocation(serveCase{seed: seeds(seedFlip(`{"paused": true}`), seedProjectDir(".orchestrator/logs"))})
	run := func(argv []string) outcome {
		sb := h.newSandbox(t)
		inv.seed(t, sb)
		o := h.runDaemonWithin(t, sb, argv, nil, inv.env, exitWait)
		return o.withProjectPlaceholder(t, sb).normalized(func(s string) string {
			return collectedAt.ReplaceAllString(inv.normalize(s), "${1}<iso>${2}") // no cache was seeded
		})
	}
	js, orch := run(inv.js), run(inv.orch)
	requireFlipEvaluated(t, "js", js)
	requireFlipEvaluated(t, "orch", orch)
	compareOutcomes(t, js, orch)
}
