package supervise

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const goldenDir = "../../testdata/routing"

const t0 = 1_800_000_000

func isoAt(sec int64) string {
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05.000Z")
}

func parse(t *testing.T, text string) *jsjson.Object {
	t.Helper()
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatalf("fixture %s: %v", text, err)
	}
	return value.(*jsjson.Object)
}

// running is the JS test helper: a running standard-breadth worker that last progressed at
// t0, with the given status.
func running(t *testing.T, status string) *jsjson.Object {
	return parse(t, fmt.Sprintf(`{"lane": "B-01", "run_id": "run1", "status": %q, "pid": 1, "breadth": "standard", "started_at": %q, "last_progress_at": %q}`,
		status, isoAt(t0), isoAt(t0)))
}

var aliveWithLock = Liveness{Alive: true, LockPresent: true}

func expectEvent(t *testing.T, got *Event, want *Event) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("event = %+v, want none", *got)
	case want != nil && (got == nil || *got != *want):
		t.Errorf("event = %+v, want %+v", got, *want)
	}
}

// TestSuperviseJS ports orchestrator/tests/supervise.test.mjs; subtest names are the JS titles.
func TestSuperviseJS(t *testing.T) {
	standard := cadence["standard"]

	t.Run("first observation of a run never prompts; a check is due after one cadence", func(t *testing.T) {
		obs := Observe(running(t, "running"), t0+1, aliveWithLock)
		if obs.Phase != PhaseRunning {
			t.Fatalf("phase = %s, want running", obs.Phase)
		}
		expectEvent(t, ChooseEvent(obs, State{}, nil, t0+1), nil)
		state := State{Run: "run1", NextCheck: t0 + standard}
		expectEvent(t, ChooseEvent(obs, state, nil, t0+standard), &Event{ID: fmt.Sprintf("run1:check:%d", t0+standard), Reason: "check"})
	})

	t.Run("terminal phases prompt exactly once unless the event is retryable", func(t *testing.T) {
		obs := Observe(running(t, "finished"), t0+10, Liveness{})
		if obs.Phase != "finished" {
			t.Fatalf("phase = %s, want finished", obs.Phase)
		}
		finished := &Event{ID: "run1:finished", Reason: "finished"}
		expectEvent(t, ChooseEvent(obs, State{}, nil, t0+10), finished)
		expectEvent(t, ChooseEvent(obs, State{Events: parse(t, `{"run1:finished": {"status": "queued"}}`)}, nil, t0+10), nil)
		expectEvent(t, ChooseEvent(obs, State{Events: parse(t, `{"run1:finished": {"status": "intent_persisted"}}`)}, nil, t0+10), finished)
	})

	t.Run("F1: a definite delivery failure does not suppress the next due check", func(t *testing.T) {
		obs := Observe(running(t, "running"), t0+2*standard, aliveWithLock)
		events := parse(t, fmt.Sprintf(`{"run1:check:%d": {"status": "delivery_failed", "retry_after": %d}}`, t0+standard, t0+2*standard))
		if ChooseEvent(obs, State{Run: "run1", NextCheck: t0 + standard, Events: events}, nil, t0+2*standard) == nil {
			t.Error("a check should be emitted")
		}
	})

	t.Run("a delivered-but-unacknowledged progress reminder suppresses further checks", func(t *testing.T) {
		obs := Observe(running(t, "running"), t0+2*standard, aliveWithLock)
		events := parse(t, fmt.Sprintf(`{"run1:check:%d": {"status": "queued"}}`, t0+standard))
		expectEvent(t, ChooseEvent(obs, State{Run: "run1", NextCheck: t0 + standard, Events: events}, nil, t0+2*standard), nil)
	})

	t.Run("F4: quiet workers re-alert every 30 minutes with distinct ids", func(t *testing.T) {
		w := running(t, "running")
		q1 := Observe(w, t0+1000, aliveWithLock)
		if !q1.Quiet {
			t.Fatal("worker quiet for 1000 s should be quiet")
		}
		e1 := ChooseEvent(q1, State{Run: "run1"}, nil, t0+1000)
		if e1 == nil || e1.Reason != "quiet" {
			t.Fatalf("event = %+v, want a quiet event", e1)
		}
		q2 := Observe(w, t0+1000+QuietRealertSeconds, aliveWithLock)
		events, acks := jsjson.NewObject(), jsjson.NewObject()
		events.Set(e1.ID, parse(t, `{"status": "queued"}`))
		acks.Set(e1.ID, true)
		e2 := ChooseEvent(q2, State{Run: "run1", Events: events}, acks, t0+1000+QuietRealertSeconds)
		if e2 == nil || e2.ID == e1.ID {
			t.Errorf("second quiet event = %+v, want a new id distinct from %s", e2, e1.ID)
		}
	})

	t.Run("dead process while status says running -> process_exited; terminal with lock -> stale_lock", func(t *testing.T) {
		if phase := Observe(running(t, "running"), t0+5, Liveness{LockPresent: true}).Phase; phase != PhaseProcessExited {
			t.Errorf("phase = %s, want process_exited", phase)
		}
		if phase := Observe(running(t, "failed"), t0+5, Liveness{LockPresent: true}).Phase; phase != PhaseStaleLock {
			t.Errorf("phase = %s, want stale_lock", phase)
		}
	})

	t.Run("F8: events older than the TTL are pruned", func(t *testing.T) {
		events := parse(t, fmt.Sprintf(`{"old": {"created_at": %q}, "fresh": {"created_at": %q}}`, isoAt(t0-10*24*3600), isoAt(t0)))
		if keys := strings.Join(PruneEvents(events, t0).Keys(), ","); keys != "fresh" {
			t.Errorf("kept %q, want fresh", keys)
		}
	})
}

// TestGoldens compares observations, chosen events and reminder texts with the JS output for
// the supervise cases in testdata/routing/cases.json (supervise.golden).
func TestGoldens(t *testing.T) {
	data, err := os.ReadFile(goldenDir + "/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var results []any
	for _, c := range jsjson.Path(parse(t, string(data)), "supervise").([]any) {
		now := int64(jsjson.Path(c, "now_sec").(float64))
		worker, _ := jsjson.Path(c, "worker").(*jsjson.Object)
		liveness := Liveness{Alive: jsjson.Path(c, "alive") == true, LockPresent: jsjson.Path(c, "lock_present") == true}
		obs := Observe(worker, now, liveness)
		acks, _ := jsjson.Path(c, "acks").(*jsjson.Object)
		ev := ChooseEvent(obs, stateOf(jsjson.Path(c, "state")), acks, now)

		result := jsjson.NewObject()
		result.Set("observation", observationJSON(obs))
		if ev == nil {
			result.Set("event", nil)
			result.Set("reminder", nil)
		} else {
			event := jsjson.NewObject()
			event.Set("id", ev.ID)
			event.Set("reason", ev.Reason)
			result.Set("event", event)
			result.Set("reminder", ReminderText(ev.ID, ev.Reason, obs, jsjson.Path(c, "project_root").(string)))
		}
		results = append(results, result)
	}
	got, err := jsjson.MarshalIndent(results, "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(goldenDir + "/supervise.golden")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("supervise results differ from the JS output\n got: %s\nwant: %s", got, want)
	}
}

func stateOf(doc any) State {
	run, _ := jsjson.Path(doc, "run").(string)
	nextCheck, _ := jsjson.Path(doc, "next_check").(float64)
	events, _ := jsjson.Path(doc, "events").(*jsjson.Object)
	return State{Run: run, NextCheck: int64(nextCheck), Events: events}
}

// observationJSON is the JS observe() result object, for comparison with the golden.
func observationJSON(obs Observation) *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("run", obs.Run)
	if obs.HasLane {
		out.Set("lane", obs.Lane)
	}
	out.Set("phase", obs.Phase)
	out.Set("breadth", obs.Breadth)
	out.Set("interval", float64(obs.Interval))
	out.Set("progress_at", float64(obs.ProgressAt))
	out.Set("quiet", obs.Quiet)
	out.Set("quiet_for", float64(obs.QuietFor))
	return out
}
