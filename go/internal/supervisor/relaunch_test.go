package supervisor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// Owner defect ee50690d, owner rules 5b18cc1c and 779604e2: an idle conductor is relaunched
// unless a gate or a pause holds it, the human hears of it once, and a failing relaunch is
// retried slowly forever.

// conductorLane is a conductor lane with run id K on opencode, its runner gone.
func conductorLane(status string) string {
	return `{"run_id": "K", "harness": "opencode", "status": "` + status + `", "pid": 9, "session_id": "ses-o", "started_at": "2027-01-15T08:00:00.000Z"}`
}

// relaunchScript is what a scripted project's `flip decide` and `flip run` answer.
type relaunchScript struct {
	decision string
	run      func() SyncResult
}

func launchFails() SyncResult { return SyncResult{Status: status(1), Stderr: "launch failed"} }

// scriptedRelaunch is an idle project whose decide stays and whose every flip run fails until
// the script is changed.
func scriptedRelaunch(t *testing.T, lane, flip string) (*fixture, *relaunchScript) {
	f := relaunchFixture(t, lane, flip, `{}`)
	script := &relaunchScript{decision: `{"action":"stay","reason":"r"}`, run: launchFails}
	f.respond = func(spec SyncSpec) SyncResult {
		if spec.Args[1] == "decide" {
			return SyncResult{Status: status(0), Stdout: script.decision}
		}
		return script.run()
	}
	return f, script
}

// launchPublishes is a launch whose runner starts and publishes a live replacement lane.
func (f *fixture) launchPublishes() SyncResult {
	f.write("workers/conductor.json", `{"run_id": "replacement", "harness": "opencode", "status": "running", "pid": 123}`)
	f.live[123] = true
	return SyncResult{Status: status(0), Stdout: conductorLaunched}
}

// sent lists the mailbox messages of one kind.
func (f *fixture) sent(kind string) []any {
	var out []any
	for _, line := range f.messages() {
		if line == "" {
			continue
		}
		if msg := mustParse(f.t, line); jsjson.Path(msg, "kind") == kind {
			out = append(out, msg)
		}
	}
	return out
}

// evaluateEvery runs a flip evaluation every step seconds from `from` to `to` (inclusive),
// calling each before it, and returns the times a flip run was launched.
func (f *fixture) evaluateEvery(state *string, from, to, step int, each func(at int)) []int {
	f.t.Helper()
	var launchedAt []int
	for at := from; at <= to; at += step {
		if each != nil {
			each(at)
		}
		f.at(float64(at))
		before := len(f.launches())
		*state = compact(f.t, f.mustEvaluate(*state))
		if len(f.launches()) > before {
			launchedAt = append(launchedAt, at)
		}
	}
	return launchedAt
}

// Regression for ee50690d: a runner that died before its final state write left
// conductor.json saying running (or starting); it is relaunched on the same door.
func TestRelaunchConductorThatExitedWithoutAFinalStatus(t *testing.T) {
	for _, laneStatus := range []string{"running", "starting"} {
		t.Run(laneStatus, func(t *testing.T) {
			f, script := scriptedRelaunch(t, conductorLane(laneStatus), `{}`)
			script.run = f.launchPublishes
			state := `{}`
			f.evaluateEvery(&state, 0, 1200, 60, nil)
			want := [][]string{{"flip", "run", "--unless-gated", "--to", "opencode", "--from", "opencode",
				"--reason", "stay: r (previous conductor exited without a final status)", "--cwd", f.root, "--resume", "ses-o"}}
			var got [][]string
			for _, launch := range f.launches() {
				got = append(got, launch.Args)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("launches %q, want %q", got, want)
			}
			notes := f.sent("note")
			if len(notes) != 1 || jsjson.Path(notes[0], "to") != "human" ||
				jsjson.Path(notes[0], "subject") != "opencode conductor relaunched by the supervisor: exited without a final status" ||
				!strings.Contains(jsjson.Path(notes[0], "body").(string), "Previous run K exited without a final status.\nRelaunch: started replacement.") ||
				!strings.Contains(jsjson.Path(notes[0], "body").(string), "orch flip gate") {
				t.Fatalf("notes %v", notes)
			}
		})
	}
}

func TestRelaunchNeedsADeadRunnerAFreeLockAndNoGate(t *testing.T) {
	t.Run("live pid", func(t *testing.T) {
		f, _ := scriptedRelaunch(t, conductorLane("running"), `{}`)
		f.live[9] = true
		f.mustEvaluate(`{}`)
		if len(f.runs) != 0 {
			t.Fatalf("runs %v", f.runs)
		}
	})
	t.Run("held lock", func(t *testing.T) {
		f, _ := scriptedRelaunch(t, conductorLane("running"), `{}`)
		f.holdLock("workers/conductor.lock")
		f.mustEvaluate(`{}`)
		if len(f.launches()) != 0 || len(f.messages()) != 0 {
			t.Fatalf("launches %v, messages %v", f.launches(), f.messages())
		}
	})
	t.Run("gate", func(t *testing.T) {
		f, _ := scriptedRelaunch(t, conductorLane("running"), `{"awaiting_human": {"reason": "push"}}`)
		f.mustEvaluate(`{}`)
		if len(f.runs) != 0 || len(f.messages()) != 0 {
			t.Fatalf("runs %v, messages %v", f.runs, f.messages())
		}
	})
}

// Attempts 1-5 keep S13e's backoff under the decided path's 600 s cooldown; the human gets
// one informational note and one blocked note; a success then ends the relaunches.
func TestRelaunchFailuresAreRetriedWithOneNoteOfEachKind(t *testing.T) {
	f, script := scriptedRelaunch(t, conductorLane("running"), `{}`)
	state := `{}`
	launchedAt := f.evaluateEvery(&state, 0, 4140, 60, nil)
	if want := []int{0, 600, 1200, 1800, 2400}; !reflect.DeepEqual(launchedAt, want) {
		t.Fatalf("launched at %v, want %v", launchedAt, want)
	}
	if notes := f.sent("note"); len(notes) != 1 || !strings.Contains(jsjson.Path(notes[0], "body").(string), "Relaunch: failed, retrying with backoff.") {
		t.Fatalf("informational notes %v", notes)
	}
	if blocked := f.sent("blocked"); len(blocked) != 1 || jsjson.Path(blocked[0], "subject") != "conductor relaunch stopped after 5 failed attempts" {
		t.Fatalf("blocked notes %v", blocked)
	}
	script.run = f.launchPublishes
	launchedAt = f.evaluateEvery(&state, 4200, 9000, 60, nil)
	if !reflect.DeepEqual(launchedAt, []int{4200}) || len(f.messages()) != 2 {
		t.Fatalf("launched at %v after a success; messages %v", launchedAt, f.messages())
	}
}

// Regression for the app lane of 2026-10-22 20:26Z: a finished conductor that posted a bare
// handoff is relaunched on its own door, once, with one note.
func TestRelaunchAfterABareHandoff(t *testing.T) {
	f, script := scriptedRelaunch(t, conductorLane("finished"), `{}`)
	f.mailbox(`{"id":"m1","at":"2027-01-15T08:30:00.000Z","from":"conductor-opencode","to":"coordinator","kind":"handoff","subject":"ending for owner G4 cutover"}`)
	script.run = f.launchPublishes
	state := `{}`
	f.evaluateEvery(&state, 0, 1800, 60, nil)
	launches := f.launches()
	if len(launches) != 1 || !reflect.DeepEqual(launches[0].Args[3:9], []string{"--to", "opencode", "--from", "opencode",
		"--reason", "stay: r (previous conductor handed off without flip run, pause or gate)"}) {
		t.Fatalf("launches %v", launches)
	}
	notes := f.sent("note")
	if len(notes) != 1 || jsjson.Path(notes[0], "subject") != "opencode conductor relaunched by the supervisor: handed off without flip run, pause or gate" {
		t.Fatalf("notes %v", notes)
	}
}

func TestRelaunchAfterAHandoffIsHeldOnlyByAGateOrAPause(t *testing.T) {
	handoff := `{"id":"m1","at":"2027-01-15T08:30:00.000Z","from":"conductor-opencode","to":"coordinator","kind":"handoff","subject":"s"}`
	t.Run("paused, decide pauses", func(t *testing.T) {
		f, script := scriptedRelaunch(t, conductorLane("finished"), `{"paused": {"at": "x"}}`)
		f.mailbox(handoff)
		script.decision = `{"action":"pause","reason":"both red"}`
		f.mustEvaluate(`{}`)
		if len(f.launches()) != 0 || len(f.sent("note")) != 0 {
			t.Fatalf("launches %v", f.launches())
		}
	})
	t.Run("gate", func(t *testing.T) {
		f, _ := scriptedRelaunch(t, conductorLane("finished"), `{"awaiting_human": {"reason": "push"}}`)
		f.mailbox(handoff)
		f.mustEvaluate(`{}`)
		if len(f.runs) != 0 || len(f.sent("note")) != 0 {
			t.Fatalf("runs %v", f.runs)
		}
	})
	t.Run("pending launch", func(t *testing.T) {
		f, _ := scriptedRelaunch(t, conductorLane("finished"), `{"pending_launch": {"at": "a", "to": "claude", "from": "opencode"}}`)
		f.mailbox(handoff)
		f.mustEvaluate(`{}`)
		launches := f.launches()
		if len(f.runs) != 1 || len(launches) != 1 || launches[0].Args[8] != "pending launch" || len(f.sent("note")) != 0 {
			t.Fatalf("runs %v", f.runs)
		}
	})
}

func TestRelaunchNotesNameEachCauseAndThePreviousRun(t *testing.T) {
	handoff := `{"id":"m1","at":"2027-01-15T08:30:00.000Z","from":"c","to":"coordinator","kind":"handoff","subject":"s"}`
	for _, c := range []struct {
		lane, mailbox, cause string
	}{
		{conductorLane("failed"), "", "failed"},
		{conductorLane("interrupted"), "", "interrupted"},
		{conductorLane("deferred"), "", "deferred"},
		{conductorLane("finished"), "", "exited without handoff"},
		{conductorLane("finished"), handoff, "handed off without flip run, pause or gate"},
		{conductorLane("running"), "", "exited without a final status"},
	} {
		t.Run(c.cause, func(t *testing.T) {
			f, _ := scriptedRelaunch(t, c.lane, `{}`)
			if c.mailbox != "" {
				f.mailbox(c.mailbox)
			}
			state := `{}`
			f.evaluateEvery(&state, 0, 1200, 60, nil)
			notes := f.sent("note")
			if len(f.launches()) != 3 || len(notes) != 1 ||
				jsjson.Path(notes[0], "subject") != "opencode conductor relaunched by the supervisor: "+c.cause ||
				!strings.HasPrefix(jsjson.Path(notes[0], "body").(string), "Previous run K "+c.cause+".\n") {
				t.Fatalf("launches %d, notes %v", len(f.launches()), notes)
			}
			if jsjson.Path(mustParse(t, state), "redispatch", "K", "relaunch_noted") != true {
				t.Fatalf("state %s", state)
			}
		})
	}
}

// Owner rule 779604e2 over about 13 h: after five fast failures a conductor key is retried
// every 30 min, its blocked note repeated at most every 6 h, until a launch is confirmed.
func TestRelaunchSlowRetryOfTheConductorSlot(t *testing.T) {
	for _, c := range []struct{ name, lane, flip string }{
		{"conductor lane", conductorLane("failed"), `{}`},
		{"no conductor.json", "", `{"paused": true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, script := scriptedRelaunch(t, c.lane, c.flip)
			state := `{}`
			launchedAt := f.evaluateEvery(&state, 0, 46800, 60, nil)
			want := []int{0, 600, 1200, 1800, 2400}
			for at := 2400 + 1800; at <= 46800; at += 1800 {
				want = append(want, at)
			}
			if !reflect.DeepEqual(launchedAt, want) {
				t.Fatalf("launched at %v, want %v", launchedAt, want)
			}
			var subjects []any
			for _, note := range f.sent("blocked") {
				subjects = append(subjects, jsjson.Path(note, "subject"))
			}
			wantSubjects := []any{"conductor relaunch stopped after 5 failed attempts",
				"conductor relaunch still failing after 17 attempts", // 2400 + 12 × 1800 = 24000: 6 h after 2400
				"conductor relaunch still failing after 29 attempts"} // 45600: 6 h after 24000
			if !reflect.DeepEqual(subjects, wantSubjects) {
				t.Fatalf("blocked subjects %v, want %v", subjects, wantSubjects)
			}
			for _, note := range f.sent("blocked") {
				if jsjson.Path(note, "to") != "human" {
					t.Fatalf("blocked note to %v", jsjson.Path(note, "to"))
				}
			}
			script.run = f.launchPublishes
			launchedAt = f.evaluateEvery(&state, 46860, 60000, 60, nil)
			if !reflect.DeepEqual(launchedAt, []int{47400}) {
				t.Fatalf("after a success launched at %v", launchedAt)
			}
		})
	}
}

func TestRelaunchSlowRetryOfAPendingLaunch(t *testing.T) {
	f, _ := scriptedRelaunch(t, "", `{"pending_launch": {"at": "p", "to": "claude", "from": "opencode"}}`)
	state := `{}`
	launchedAt := f.evaluateEvery(&state, 0, 6300, 60, nil)
	if want := []int{0, 60, 180, 420, 900, 2700, 4500, 6300}; !reflect.DeepEqual(launchedAt, want) {
		t.Fatalf("launched at %v, want %v", launchedAt, want)
	}
	if blocked := f.sent("blocked"); len(blocked) != 1 || jsjson.Path(blocked[0], "to") != "human" {
		t.Fatalf("blocked notes %v", blocked)
	}
}

// A worker lane keeps S13e's stop, whatever its name.
func TestRelaunchWorkerLanesStopAfterFiveFailures(t *testing.T) {
	for _, lane := range []string{"b1", "conductor:none"} {
		t.Run(lane, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			f.write("workers/"+lane+".json", `{"run_id": "run-x", "status": "deferred", "brief": "b.md", "harness": "claude", "resume_after": "`+iso(-60)+`"}`)
			// The lane's own deferred wake is acknowledged so only redispatch posts.
			f.write("supervisor-acks.json", `{"run-x:deferred": true}`)
			f.runResult = launchFails()
			state := `{}`
			for at := 0; at <= 900+24*3600; at += 60 {
				f.at(float64(at))
				state = compact(t, f.mustTick(state))
			}
			if len(f.runs) != 5 {
				t.Fatalf("%d launches, want 5", len(f.runs))
			}
			blocked := f.sent("blocked")
			if len(blocked) != 1 || jsjson.Path(blocked[0], "to") != "coordinator" {
				t.Fatalf("blocked notes %v", blocked)
			}
		})
	}
}

func TestRelaunchSlowRetryIsHeldByAGateAndByAPause(t *testing.T) {
	f, script := scriptedRelaunch(t, conductorLane("failed"), `{}`)
	state := `{}`
	f.evaluateEvery(&state, 0, 3*3600-60, 60, nil)
	launches, messages := len(f.launches()), len(f.messages())
	f.write("flip.json", `{"awaiting_human": {"reason": "push"}}`)
	if launchedAt := f.evaluateEvery(&state, 3*3600, 9*3600, 60, nil); len(launchedAt) != 0 || len(f.messages()) != messages {
		t.Fatalf("gated: launched at %v, messages %v", launchedAt, f.messages()[messages:])
	}
	f.write("flip.json", `{"paused": {"at": "x"}}`)
	script.decision = `{"action":"pause"}`
	if launchedAt := f.evaluateEvery(&state, 9*3600+60, 12*3600, 60, nil); len(launchedAt) != 0 || len(f.messages()) != messages {
		t.Fatalf("paused: launched at %v", launchedAt)
	}
	script.decision = `{"action":"stay"}`
	if launchedAt := f.evaluateEvery(&state, 12*3600+60, 12*3600+60, 60, nil); len(launchedAt) != 1 || len(f.launches()) != launches+1 {
		t.Fatalf("paused, decide stays: launched at %v", launchedAt)
	}
}

// D1d: a launch its launcher confirmed whose runner never published a lane leaves the slot on
// the old key. 600 s later the confirmation counts as a failed attempt and backoff applies.
func TestRelaunchStaleConfirmationBecomesAFailedAttempt(t *testing.T) {
	f, script := scriptedRelaunch(t, conductorLane("failed"), `{}`)
	script.run = func() SyncResult { return SyncResult{Status: status(0), Stdout: conductorLaunched} }
	state := `{}`
	if launchedAt := f.evaluateEvery(&state, 0, 540, 60, nil); !reflect.DeepEqual(launchedAt, []int{0}) {
		t.Fatalf("launched at %v", launchedAt)
	}
	if jsjson.Path(mustParse(t, state), "redispatch", "K", "confirmed_run_id") != "replacement" {
		t.Fatalf("not confirmed: %s", state)
	}
	f.evaluateEvery(&state, 600, 600, 60, nil)
	record := jsjson.Path(mustParse(t, state), "redispatch", "K")
	if jsjson.Path(record, "confirmed_run_id") != nil || jsjson.Path(record, "attempts") != float64(2) || jsjson.Path(record, "last_attempt_at") != iso(600) {
		t.Fatalf("stale confirmation kept: %s", state)
	}
	// Attempt 2's backoff (120 s) runs from the expiry.
	if launchedAt := f.evaluateEvery(&state, 660, 720, 60, nil); !reflect.DeepEqual(launchedAt, []int{720}) {
		t.Fatalf("relaunched at %v, want [720]", launchedAt)
	}
	if !strings.Contains(f.logText(), "conductor launch replacement never published a lane; counted as a failed attempt") || len(f.sent("note")) != 1 {
		t.Fatalf("log %q, notes %v", f.logText(), f.sent("note"))
	}
}

// PE-59: an ended or exited lane shows no next check; a running lane's is unchanged.
func TestTickLaneEntryHasNoNextCheckOnceEnded(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.worker("done", `"status": "finished", "pid": 7`)
	f.worker("gone", `"status": "running", "pid": 8`)
	f.worker("live", `"status": "running", "pid": 9, "last_progress_at": "`+iso(0)+`"`)
	f.live[9] = true
	state := `{}`
	for _, at := range []float64{0, 300, 900, 4000} {
		f.at(at)
		object := f.mustTick(state)
		for _, lane := range []string{"done", "gone"} {
			entry := jsjson.Path(object, "lanes", lane)
			if v, ok := jsjson.Lookup(entry, "next_check"); !ok || v != nil {
				t.Fatalf("at %v %s next_check %v", at, lane, v)
			}
			if v, ok := jsjson.Lookup(entry, "next_check_at"); !ok || v != nil {
				t.Fatalf("at %v %s next_check_at %v", at, lane, v)
			}
		}
		state = compact(t, object)
	}
	if got := jsjson.Path(mustParse(t, state), "lanes", "live", "next_check"); got == nil {
		t.Fatalf("running lane lost its next_check: %s", state)
	}
}
