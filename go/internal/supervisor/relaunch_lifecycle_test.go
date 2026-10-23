package supervisor

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// Sol review R1/R2 (plans/go-s13o-clarification.md section 6): one owner for conductor
// launches, and conductor retries owned by the slot across the real `flip run` writer, which
// replaces flip.json (dropping paused and pending_launch) before it launches. Every test runs
// the full Tick on real files with a fake clock.

// lifecycle is a project whose `flip decide` answers decision and whose `flip run` is the real
// writer, flipcmd.Project.Run, with its `worker start` launcher faked by launcher.
type lifecycle struct {
	*fixture
	decision  string
	launcher  func(args []string) (*int, string, string)
	launched  [][]string // the launcher's argv, one per real launch
	onDecide  func()
	workerRun int // `worker start` calls the supervisor made itself (the lane-loop path)
}

func newLifecycle(t *testing.T) *lifecycle {
	l := &lifecycle{fixture: newFixture(t), decision: `{"action":"stay","reason":"r"}`}
	l.mkdir("logs")
	l.launcher = func([]string) (*int, string, string) { return status(1), "", "launch failed" }
	l.respond = func(spec SyncSpec) SyncResult {
		switch {
		case spec.Args[0] == "worker":
			l.workerRun++
			return SyncResult{Status: status(1), Stderr: "lane-loop launch"}
		case spec.Args[1] == "decide":
			if l.onDecide != nil {
				l.onDecide()
			}
			return SyncResult{Status: status(0), Stdout: l.decision}
		case spec.Args[1] == "run":
			return l.realRun(spec.Args[2:])
		}
		t.Fatalf("unexpected child %v", spec.Args)
		return SyncResult{}
	}
	return l
}

// realRun is `orch flip run <args>` in process: the CLI's parse, the real writer and its
// printed answer.
func (l *lifecycle) realRun(args []string) SyncResult {
	opts, _ := cliargs.ParseArgs(args,
		[]string{"active", "to", "from", "cwd", "reason", "resume", "question", "brief"},
		[]string{"force", "clear", "no-relaunch", "unless-gated"})
	project := flipcmd.NewProject(l.root)
	project.Now = func() time.Time { return l.now }
	answer, code, err := project.Run(opts, func(argv []string) (*int, string, string) {
		l.launched = append(l.launched, argv)
		return l.launcher(argv)
	})
	if err != nil {
		l.t.Fatalf("flip run: %v", err)
	}
	return SyncResult{Status: status(code), Stdout: compact(l.t, answer) + "\n"}
}

// tickEvery runs a full Tick every step seconds over [from, to] and returns the times a
// supervisor `flip run` was made.
func (l *lifecycle) tickEvery(state *string, from, to, step int) []int {
	l.t.Helper()
	var at []int
	for now := from; now <= to; now += step {
		l.at(float64(now))
		before := len(l.launches())
		*state = compact(l.t, l.mustTick(*state))
		if len(l.launches()) > before {
			at = append(at, now)
		}
	}
	return at
}

// deferredConductorAtFive is a deferred conductor whose slot record has five failed attempts,
// the last 1800 s ago: due for both the old lane-loop path and a slow retry.
func deferredConductorAtFive(l *lifecycle) string {
	l.write("workers/conductor.json", `{"run_id": "K", "harness": "opencode", "status": "deferred", "pid": 9, "session_id": "ses-o", "brief": "b.md", "breadth": "broad", "resume_after": "`+iso(-3600)+`", "started_at": "`+iso(-7200)+`"}`)
	return `{"redispatch": {"K": {"attempts": 5, "last_attempt_at": "` + iso(-1800) + `", "confirmed_run_id": null, "blocked_noted_at": "` + iso(-1800) + `"}}}`
}

// R1-a: a deferred conductor is never launched by the lane loop; only evaluateFlip, behind
// its gate, unreadable-state and pause handling, launches it.
func TestDeferredConductorIsLaunchedOnlyByTheFlipEvaluation(t *testing.T) {
	for _, c := range []struct {
		name, flip, decision string
		wantFlipRuns         int
	}{
		{"human gate", `{"awaiting_human": {"reason": "push"}}`, `{"action":"stay"}`, 0},
		{"unreadable flip state", `{bad`, `{"action":"stay"}`, 0},
		{"paused, decide pauses", `{"paused": {"at": "x"}}`, `{"action":"pause","reason":"both red"}`, 0},
		{"paused, decide stays", `{"paused": {"at": "x"}}`, `{"action":"stay","reason":"r"}`, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLifecycle(t)
			l.write("flip.json", c.flip)
			l.decision = c.decision
			state := deferredConductorAtFive(l)
			l.at(0)
			l.mustTick(state)
			if l.workerRun != 0 {
				t.Fatalf("the lane loop launched the conductor %d times", l.workerRun)
			}
			launches := l.launches()
			if len(launches) != c.wantFlipRuns || len(l.sent("blocked")) != 0 {
				t.Fatalf("flip runs %v, blocked notes %v", launches, l.sent("blocked"))
			}
			if c.wantFlipRuns == 1 && (launches[0].Args[2] != "--unless-gated" || launches[0].Args[4] != "opencode") {
				t.Fatalf("launch %v", launches[0].Args)
			}
		})
	}
}

// R1-b: a deferred worker lane keeps the lane-loop redispatch and its argv.
func TestDeferredWorkerLaneKeepsTheLaneLoopRedispatch(t *testing.T) {
	f := deferredFixture(t)
	f.runResult = SyncResult{Status: status(0), Stdout: laneLaunched}
	f.mustTick(`{}`)
	want := []string{"worker", "start", "--brief", "b.md", "--harness", "claude", "--lane", "b1", "--breadth", "standard", "--cwd", f.root, "--detach"}
	if len(f.runs) != 1 || !reflect.DeepEqual(f.runs[0].Args, want) {
		t.Fatalf("runs %+v, want %q", f.runs, want)
	}
}

// R1-c: a gate installed after the evaluation read flip state is caught by the launch-time
// recheck: no launcher runs and no attempt is recorded.
func TestGateInstalledBeforeTheRunIsANonAttempt(t *testing.T) {
	l := newLifecycle(t)
	l.write("workers/conductor.json", conductorLane("failed"))
	l.write("flip.json", `{}`)
	l.onDecide = func() { l.write("flip.json", `{"awaiting_human": {"reason": "late gate"}}`) }
	state := l.mustTick(`{}`)
	if len(l.launches()) != 1 || len(l.launched) != 0 {
		t.Fatalf("flip runs %d, launcher runs %d", len(l.launches()), len(l.launched))
	}
	if jsjson.Path(state, "redispatch") != nil || jsjson.Path(state, "last_relaunch_at") != nil {
		t.Fatalf("a refused run was recorded: %s", compact(t, state))
	}
}

// R2-a: an empty, paused slot whose real launch fails keeps being retried after `flip run`
// removed paused: fast backoff under the cooldown, one blocked note, then every 1800 s.
func TestEmptySlotRetriesAfterTheRealWriterClearsPaused(t *testing.T) {
	l := newLifecycle(t)
	l.write("flip.json", `{"paused": {"at": "x", "door": "opencode"}}`)
	state := `{}`
	launchedAt := l.tickEvery(&state, 0, 0, 60)
	if strings.Contains(l.read("flip.json"), "paused") || len(l.launched) != 1 {
		t.Fatalf("the real writer kept paused or did not launch: %s", l.read("flip.json"))
	}
	launchedAt = append(launchedAt, l.tickEvery(&state, 60, 6000, 60)...)
	if want := []int{0, 600, 1200, 1800, 2400, 4200, 6000}; !reflect.DeepEqual(launchedAt, want) {
		t.Fatalf("launched at %v, want %v", launchedAt, want)
	}
	if reason := l.launches()[1].Args[8]; !strings.HasSuffix(reason, " (previous supervisor launch did not start)") {
		t.Fatalf("retry reason %q", reason)
	}
	if blocked := l.sent("blocked"); len(blocked) != 1 || jsjson.Path(blocked[0], "to") != "human" {
		t.Fatalf("blocked notes %v", blocked)
	}
	if notes := l.sent("note"); len(notes) != 1 || jsjson.Path(notes[0], "subject") != "opencode conductor relaunched by the supervisor: previous supervisor launch did not start" {
		t.Fatalf("notes %v", notes)
	}
}

// R2-b: the real writer's launch is confirmed but its runner never publishes a lane: the
// confirmation is cleared at 600 s (D1d) and the slot relaunches after attempt 2's backoff.
func TestEmptySlotStaleConfirmationAfterTheRealWriter(t *testing.T) {
	l := newLifecycle(t)
	l.write("flip.json", `{"paused": {"at": "x", "door": "opencode"}}`)
	l.launcher = func([]string) (*int, string, string) {
		return status(0), `{"run_id":"N","runner_pid":123}` + "\n", ""
	}
	state := `{}`
	launchedAt := l.tickEvery(&state, 0, 540, 60)
	if !reflect.DeepEqual(launchedAt, []int{0}) || jsjson.Path(mustParse(t, state), "redispatch", "conductor:none", "confirmed_run_id") != "N" {
		t.Fatalf("launched at %v; state %s", launchedAt, state)
	}
	launchedAt = l.tickEvery(&state, 600, 600, 60)
	if record := jsjson.Path(mustParse(t, state), "redispatch", "conductor:none"); len(launchedAt) != 0 ||
		jsjson.Path(record, "confirmed_run_id") != nil || jsjson.Path(record, "attempts") != float64(2) {
		t.Fatalf("at 600 launched %v; state %s", launchedAt, state)
	}
	if launchedAt = l.tickEvery(&state, 660, 720, 60); !reflect.DeepEqual(launchedAt, []int{720}) {
		t.Fatalf("relaunched at %v, want [720]", launchedAt)
	}
}

// R2-c: a pending launch into an empty slot records under the slot key; after the real writer
// removed pending_launch, the slot retries the same door.
func TestPendingLaunchIntoAnEmptySlotIsOwnedByTheSlot(t *testing.T) {
	l := newLifecycle(t)
	l.write("flip.json", `{"to": "claude", "at": "`+iso(-60)+`", "pending_launch": {"at": "p", "to": "claude", "from": "opencode", "reason": "handback"}}`)
	state := `{}`
	launchedAt := l.tickEvery(&state, 0, 600, 60)
	if !reflect.DeepEqual(launchedAt, []int{0, 600}) {
		t.Fatalf("launched at %v, want [0 600]", launchedAt)
	}
	if strings.Contains(state, `"pending:`) || jsjson.Path(mustParse(t, state), "redispatch", "conductor:none", "attempts") != float64(2) {
		t.Fatalf("state %s", state)
	}
	if args := l.launches()[1].Args; args[4] != "claude" {
		t.Fatalf("retry %v", args)
	}
}

// R2-d: flip state without a lane, pause, pending launch or supervisor record launches nothing.
func TestNoRecordNoLaneLaunchesNothing(t *testing.T) {
	l := newLifecycle(t)
	l.write("flip.json", `{"to": "opencode", "at": "`+iso(-60)+`"}`)
	state := `{}`
	if launchedAt := l.tickEvery(&state, 0, 3600, 60); len(launchedAt) != 0 {
		t.Fatalf("launched at %v", launchedAt)
	}
}

// R2-e: a pending:<at> record left by an earlier supervisor is pruned on the first Tick.
func TestLeftoverPendingKeyIsPruned(t *testing.T) {
	l := newLifecycle(t)
	l.write("workers/conductor.json", `{"run_id": "K", "harness": "opencode", "status": "running", "pid": 9}`)
	l.live[9] = true
	l.write("flip.json", `{"pending_launch": {"at": "x", "to": "claude"}}`)
	state := l.mustTick(`{"redispatch": {"pending:x": {"attempts": 1, "last_attempt_at": "` + iso(-60) + `", "confirmed_run_id": null}}}`)
	if jsjson.Path(state, "redispatch", "pending:x") != nil {
		t.Fatalf("pending key kept: %s", compact(t, state))
	}
}
