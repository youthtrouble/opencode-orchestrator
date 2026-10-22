package supervisor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

func TestTickOnAFreshStateWritesTheJSKeyOrderAndTheTwoPhaseIntent(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.worker("b1", `"status": "finished", "pid": 7, "breadth": "focused", "started_at": "`+iso(-50)+`"`)
	var firstWrite string
	f.onMailbox = func() { firstWrite = f.read("supervisor-state.json") }
	var secondWrite string
	f.sup.deliver = func(channel any, text string) *jsjson.Object {
		secondWrite = f.read("supervisor-state.json")
		return unknownChannel(channel, text)
	}
	f.write("supervisor.json", `{"wake": [{"type": "nope"}]}`)

	state := f.mustTick(`{}`)

	id := "run-b1:finished"
	wantFirst := fmt.Sprintf(`{
  "events": {
    %q: {
      "status": "intent_persisted",
      "reason": "finished",
      "lane": "b1",
      "created_at": %q,
      "retry_after": %d
    }
  },
  "lanes": {
    "b1": {
      "run": "run-b1",
      "phase": "finished",
      "breadth": "focused",
      "quiet": false,
      "checked_at": %q,
      "next_check": %d,
      "next_check_at": %q
    }
  }
}
`, id, iso(0), t0+120, iso(0), t0+120, iso(120))
	if firstWrite != wantFirst {
		t.Errorf("state after the intent write:\n%s\nwant:\n%s", firstWrite, wantFirst)
	}
	wantSecond := strings.Replace(wantFirst, `"status": "intent_persisted"`, `"status": "cli_invoked"`, 1)
	wantSecond = strings.Replace(wantSecond, fmt.Sprintf(`"retry_after": %d`, t0+120), fmt.Sprintf(`"retry_after": %d,
      "mailbox_id": "msg-1"`, t0+120), 1)
	if secondWrite != wantSecond {
		t.Errorf("state after the cli_invoked write:\n%s\nwant:\n%s", secondWrite, wantSecond)
	}
	want := fmt.Sprintf(`{
  "events": {
    %q: {
      "status": "queued",
      "reason": "finished",
      "lane": "b1",
      "created_at": %q,
      "retry_after": %d,
      "mailbox_id": "msg-1",
      "channels": [
        {
          "type": "nope",
          "status": "delivery_failed",
          "error": "unknown channel nope"
        }
      ]
    }
  },
  "lanes": {
    "b1": {
      "run": "run-b1",
      "phase": "finished",
      "breadth": "focused",
      "quiet": false,
      "checked_at": %q,
      "next_check": %d,
      "next_check_at": %q
    }
  },
  "checked_at": %q,
  "pid": 4242,
  "status": "running"
}`, id, iso(0), t0+120, iso(0), t0+120, iso(120), iso(0))
	if got := indent(t, state); got != want {
		t.Errorf("state:\n%s\nwant:\n%s", got, want)
	}
	wantLog := iso(0) + " " + id + ` queued [{"type":"nope","status":"delivery_failed","error":"unknown channel nope"}]` + "\n"
	if got := f.read("logs/supervisor.log"); got != wantLog {
		t.Errorf("log = %q, want %q", got, wantLog)
	}
	messages := f.messages()
	if len(messages) != 1 || !strings.Contains(messages[0], `"kind":"wake","subject":"b1: finished","body":"Orchestrator supervisor event `+id+`: worker lane \"b1\" is finished (finished; focused cadence).`) ||
		!strings.HasSuffix(messages[0], `"refs":[],"thread":"`+id+`"}`) {
		t.Errorf("mailbox = %q", messages)
	}
	if len(f.incidents) != 0 {
		t.Errorf("incidents = %+v, want none (the mailbox took the event)", f.incidents)
	}
}

func TestTickKeepsTheKeyOrderOfASeededState(t *testing.T) {
	f := newFixture(t)
	state := f.mustTick(`{"status": "stopped", "pid": 1, "lanes": {}, "stopped_at": "x", "checked_at": "y", "events": {}}`)
	want := `{"status":"running","pid":4242,"lanes":{},"stopped_at":"x","checked_at":"` + iso(0) + `","events":{}}`
	if got := compact(t, state); got != want {
		t.Errorf("state = %s, want %s", got, want)
	}
}

func TestTickPerPhase(t *testing.T) {
	for _, tc := range []struct {
		phase, fields string
		live, lock    bool
		wantEvent     string // "" for none
		wantCrash     bool
	}{
		{"finished", `"status": "finished", "pid": 7`, false, false, "run-b1:finished", false},
		{"failed", `"status": "failed", "pid": 7`, false, false, "run-b1:failed", false},
		{"interrupted", `"status": "interrupted", "pid": 7`, false, false, "run-b1:interrupted", false},
		{"deferred", `"status": "deferred", "pid": 7`, false, false, "run-b1:deferred", false},
		{"finishing", `"status": "finished", "pid": 7`, true, true, "", false},
		{"stale_lock", `"status": "failed", "pid": 7`, false, true, "run-b1:stale_lock", true},
		{"process_exited", `"status": "running", "pid": 7`, false, true, "run-b1:process_exited", true},
		{"running", `"status": "running", "pid": 7, "last_progress_at": "` + iso(-10) + `"`, true, true, "", false},
		{"unknown", `"status": "paused", "pid": 7`, false, false, "", false},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			f.worker("b1", tc.fields)
			f.live[7] = tc.live
			if tc.lock {
				f.holdLock("workers/b1.lock")
			}
			state := f.mustTick(`{}`)
			if got := jsjson.Path(state, "lanes", "b1", "phase"); got != tc.phase {
				t.Errorf("lane phase = %v, want %s", got, tc.phase)
			}
			events := jsjson.Path(state, "events").(*jsjson.Object)
			if tc.wantEvent == "" {
				if events.Len() != 0 || len(f.messages()) != 0 {
					t.Errorf("events = %s, messages = %q; want none", compact(t, events), f.messages())
				}
				return
			}
			if got := compact(t, jsjson.Path(events, tc.wantEvent)); got != `{"status":"queued","reason":"`+tc.phase+`","lane":"b1","created_at":"`+iso(0)+`","retry_after":`+fmt.Sprint(t0+300)+`,"mailbox_id":"msg-1","channels":[]}` {
				t.Errorf("event = %s", got)
			}
			if tc.wantCrash != (len(f.incidents) == 1) {
				t.Fatalf("incidents = %+v, want crash %v", f.incidents, tc.wantCrash)
			}
			if tc.wantCrash {
				got := f.incidents[0]
				if got.Component != "supervisor" || got.Kind != "crash" || got.Summary != "worker lane b1: "+tc.phase || got.Detail != "" || got.Project != f.root {
					t.Errorf("incident = %+v", got)
				}
			}
		})
	}
}

// PE-30: a lane lock file no process holds (a runner killed with SIGKILL) is no stale_lock;
// the lane reads as its terminal status. The JS tests the file's presence.
func TestALaneLockFileNoProcessHoldsIsNotAStaleLock(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.worker("b1", `"status": "failed", "pid": 7`)
	f.write("workers/b1.lock", "7")
	state := f.mustTick(`{}`)
	if got := jsjson.Path(state, "lanes", "b1", "phase"); got != "failed" {
		t.Errorf("lane phase = %v, want failed", got)
	}
	if len(f.incidents) != 0 {
		t.Errorf("incidents = %+v, want none", f.incidents)
	}
}

func TestStaleLockIncidentIsRecordedOnEveryEmissionIncludingRetries(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("mailbox", "not a directory") // the post fails, no channel: delivery_failed
	f.worker("b1", `"status": "failed", "pid": 7`)
	f.holdLock("workers/b1.lock")
	state := f.mustTick(`{}`)
	if got := jsjson.Path(state, "events", "run-b1:stale_lock", "status"); got != "delivery_failed" {
		t.Fatalf("status = %v", got)
	}
	f.at(300) // retry_after reached
	f.sup.Tick(state)
	var kinds []string
	for _, d := range f.incidents {
		kinds = append(kinds, d.Kind+":"+d.Summary)
	}
	want := "bug:wake delivery failed for stale_lock,crash:worker lane b1: stale_lock,bug:wake delivery failed for stale_lock,crash:worker lane b1: stale_lock"
	if got := strings.Join(kinds, ","); got != want {
		t.Errorf("incidents = %s, want %s", got, want)
	}
	if f.incidents[0].Detail != "[]" {
		t.Errorf("bug detail = %q, want the results JSON", f.incidents[0].Detail)
	}
}

func TestDeliveryOutcomeOrder(t *testing.T) {
	for _, tc := range []struct {
		name, channelStatus string
		mailboxFails        bool
		want                string
	}{
		{"mailbox alone queues", "delivery_failed", false, "queued"},
		{"a queued channel queues without the mailbox", "queued", true, "queued"},
		{"an uncertain channel without the mailbox", "delivery_uncertain", true, "delivery_uncertain"},
		{"nothing took it", "delivery_failed", true, "delivery_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			if tc.mailboxFails {
				f.write("mailbox", "file")
			}
			f.write("supervisor.json", `{"wake": [{"type": "x"}]}`)
			f.deliverWith(tc.channelStatus)
			f.worker("b1", `"status": "finished"`)
			state := f.mustTick(`{}`)
			event := jsjson.Path(state, "events", "run-b1:finished")
			if got := jsjson.Path(event, "status"); got != tc.want {
				t.Errorf("status = %v, want %s", got, tc.want)
			}
			if tc.mailboxFails && jsjson.Path(event, "mailbox_id") != nil {
				t.Errorf("mailbox_id = %v, want null", jsjson.Path(event, "mailbox_id"))
			}
		})
	}
}

func TestCheckIsDueAtTheRecordedNextCheckOfTheSameRunOnly(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.worker("b1", `"status": "running", "pid": 7, "last_progress_at": "`+iso(0)+`"`)
	f.live[7] = true
	state := f.mustTick(`{}`)
	if got := compact(t, jsjson.Path(state, "lanes", "b1", "next_check")); got != fmt.Sprint(t0+300) {
		t.Fatalf("next_check = %s", got)
	}
	f.at(299)
	f.sup.Tick(state)
	if jsjson.Path(state, "events").(*jsjson.Object).Len() != 0 {
		t.Fatal("a check before next_check")
	}
	f.at(300)
	f.sup.Tick(state)
	id := fmt.Sprintf("run-b1:check:%d", t0+300)
	if got := jsjson.Path(state, "events", id, "status"); got != "queued" {
		t.Fatalf("event %s = %v", id, got)
	}
	if got := jsjson.Path(state, "lanes", "b1", "next_check"); got != float64(t0+600) {
		t.Errorf("next_check after the event = %v, want now + interval", got)
	}

	// A new run ignores the recorded next_check.
	g := newFixture(t)
	g.mkdir("logs")
	g.worker("b1", `"status": "running", "pid": 7, "last_progress_at": "`+iso(0)+`"`)
	g.live[7] = true
	state = g.mustTick(fmt.Sprintf(`{"lanes": {"b1": {"run": "other", "next_check": %d}}}`, t0-5))
	if jsjson.Path(state, "events").(*jsjson.Object).Len() != 0 {
		t.Error("a check for another run's next_check")
	}
}

func TestQuietAlertsAtThreeIntervalsOrTenMinutesAndReAlertsPerBucket(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.worker("b1", `"status": "running", "pid": 7, "breadth": "focused", "last_progress_at": "`+iso(0)+`"`)
	f.live[7] = true
	f.at(599)
	state := f.mustTick(`{}`)
	if jsjson.Path(state, "lanes", "b1", "quiet") != false {
		t.Fatal("quiet before max(600, 3 x 120)")
	}
	f.at(600)
	f.sup.Tick(state)
	first := fmt.Sprintf("run-b1:quiet:%d:0", t0)
	if jsjson.Path(state, "events", first, "reason") != "quiet" {
		t.Fatalf("events = %s", compact(t, jsjson.Path(state, "events")))
	}
	f.write("supervisor-acks.json", `{"`+first+`": {"by": "c"}}`)
	f.at(1799)
	f.sup.Tick(state)
	if jsjson.Path(state, "events").(*jsjson.Object).Len() != 1 {
		t.Fatalf("re-alert inside the first bucket: %s", compact(t, jsjson.Path(state, "events")))
	}
	f.at(1800)
	f.sup.Tick(state)
	if jsjson.Path(state, "events", fmt.Sprintf("run-b1:quiet:%d:1", t0), "reason") != "quiet" {
		t.Errorf("no re-alert in the next bucket: %s", compact(t, jsjson.Path(state, "events")))
	}
}

func TestDeliveryFailedIsRetriedOnlyAfterRetryAfterAndAckedEventsAreNotResent(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("mailbox", "file")
	f.worker("b1", `"status": "finished"`)
	state := f.mustTick(`{}`)
	f.at(299)
	f.sup.Tick(state)
	if len(f.incidents) != 1 {
		t.Fatalf("retried before retry_after: %d incidents", len(f.incidents))
	}
	f.at(300)
	f.sup.Tick(state)
	if len(f.incidents) != 2 {
		t.Fatalf("not retried at retry_after: %d incidents", len(f.incidents))
	}
	f.write("supervisor-acks.json", `{"run-b1:finished": {"by": "c"}}`)
	f.at(900)
	f.sup.Tick(state)
	if len(f.incidents) != 2 {
		t.Errorf("an acked event was re-sent: %d incidents", len(f.incidents))
	}
}

func TestEventsArePrunedAfterSevenDaysAndKeptAtExactlySeven(t *testing.T) {
	f := newFixture(t)
	week := int64(7 * 24 * 3600)
	state := f.mustTick(fmt.Sprintf(`{"events": {"kept": {"created_at": %q}, "gone": {"created_at": %q}, "undated": {}}}`, iso(-week), iso(-week-1)))
	if got := strings.Join(jsjson.Path(state, "events").(*jsjson.Object).Keys(), ","); got != "kept,undated" {
		t.Errorf("events kept = %s", got)
	}
}

func TestRedispatchOfADeferredLane(t *testing.T) {
	deferred := `"status": "deferred", "brief": "b.md", "harness": "claude", "breadth": "broad", "model": "m", "session_id": "s1", "resume_after": "` + iso(-60) + `"`
	t.Run("one minute after resume_after: launch, then redispatched_at, then the note", func(t *testing.T) {
		f := newFixture(t)
		f.worker("b1", deferred)
		f.runResult = SyncResult{Stdout: "  launched\n"}
		state := f.mustTick(`{}`)
		if len(f.runs) != 1 {
			t.Fatalf("runs = %+v", f.runs)
		}
		wantArgs := "worker start --brief b.md --harness claude --lane b1 --breadth broad --cwd " + f.root + " --detach --model m --resume s1"
		if got := strings.Join(f.runs[0].Args, " "); f.runs[0].Name != "/bin/orch" || got != wantArgs || f.runs[0].Dir != f.root {
			t.Errorf("run = %+v, want args %s", f.runs[0], wantArgs)
		}
		wantWorker := `{"run_id":"run-b1",` + strings.ReplaceAll(strings.ReplaceAll(deferred, `": `, `":`), `", "`, `","`) + `,"lane":"b1","redispatched_at":"` + iso(0) + `"}`
		if got := compactFile(t, f.read("workers/b1.json")); got != wantWorker {
			t.Errorf("worker = %s\nwant %s", got, wantWorker)
		}
		messages := f.messages()
		if len(messages) != 1 || !strings.Contains(messages[0], `"kind":"note","subject":"b1 re-dispatched after budget reset","body":"launched","refs":[],"thread":null}`) {
			t.Errorf("mailbox = %q", messages)
		}
		if jsjson.Path(state, "events").(*jsjson.Object).Len() != 0 || jsjson.Path(state, "lanes", "b1", "phase") != "deferred" {
			t.Errorf("state = %s; want the lane entry and no event", compact(t, state))
		}
	})
	t.Run("59.999 s after resume_after: the deferred event instead", func(t *testing.T) {
		f := newFixture(t)
		f.mkdir("logs")
		f.worker("b1", deferred)
		f.at(-0.001)
		state := f.mustTick(`{}`)
		if len(f.runs) != 0 || jsjson.Path(state, "events", "run-b1:deferred") == nil {
			t.Errorf("runs = %+v, state = %s", f.runs, compact(t, state))
		}
	})
	for name, fields := range map[string]string{
		"already redispatched":           `, "redispatched_at": "x"`,
		"no resume_after":                `, "resume_after": ""`,
		"unparsable resume_after":        `, "resume_after": "soon"`,
		"opted out (only boolean false)": ``,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			f.worker("b1", deferred+fields)
			if strings.HasPrefix(name, "opted out") {
				f.write("supervisor.json", `{"auto_redispatch_deferred": false}`)
			}
			f.mustTick(`{}`)
			if len(f.runs) != 0 {
				t.Errorf("redispatched: %+v", f.runs)
			}
		})
	}
	t.Run(`"false" as a string keeps it on; absent values print undefined`, func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.json", `{"auto_redispatch_deferred": "false"}`)
		f.worker("b1", `"status": "deferred", "breadth": "", "resume_after": "`+iso(-60)+`"`)
		f.runResult = SyncResult{Stderr: "boom"}
		f.mustTick(`{}`)
		want := "worker start --brief undefined --harness undefined --lane b1 --breadth standard --cwd " + f.root + " --detach"
		if len(f.runs) != 1 || strings.Join(f.runs[0].Args, " ") != want {
			t.Fatalf("runs = %+v, want %s", f.runs, want)
		}
		if messages := f.messages(); len(messages) != 1 || !strings.Contains(messages[0], `"body":"boom"`) {
			t.Errorf("note = %q, want stderr when stdout is empty", messages)
		}
	})
	t.Run("a failed note aborts the tick after the worker write", func(t *testing.T) {
		f := newFixture(t)
		f.write("mailbox", "file")
		f.worker("b1", deferred)
		f.worker("b2", `"status": "finished"`)
		state, err := f.tick(`{}`)
		if err == nil || nodefs.ErrorCode(err) != "EEXIST" { // mkdirSync of the mailbox
			t.Fatalf("error = %v (code %s), want EEXIST", err, nodefs.ErrorCode(err))
		}
		if !strings.Contains(f.read("workers/b1.json"), `"redispatched_at"`) {
			t.Error("worker file not written before the note")
		}
		if jsjson.Path(state, "lanes", "b2") != nil || jsjson.Path(state, "checked_at") != nil {
			t.Errorf("the tick went on: %s", compact(t, state))
		}
	})
	t.Run("a null config throws at the deferred lane only", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.json", `null`)
		f.worker("a", `"status": "paused"`)
		f.worker("b1", deferred)
		state, err := f.tick(`{}`)
		wantError(t, err, "Cannot read properties of null (reading 'auto_redispatch_deferred')")
		if jsjson.Path(state, "lanes", "b1", "phase") != "deferred" {
			t.Errorf("lane entry not set before the throw: %s", compact(t, state))
		}
	})
}

func compactFile(t *testing.T, text string) string {
	t.Helper()
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return compact(t, value)
}

func TestTickThrowSites(t *testing.T) {
	t.Run("logs/ missing: ENOENT after the event writes, incidents skipped", func(t *testing.T) {
		f := newFixture(t)
		f.write("mailbox", "file")
		f.worker("b1", `"status": "failed"`)
		state, err := f.tick(`{}`)
		if nodefs.ErrorCode(err) != "ENOENT" {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(f.read("supervisor-state.json"), `"status": "cli_invoked"`) {
			t.Error("the state file should hold cli_invoked")
		}
		if jsjson.Path(state, "events", "run-b1:failed", "status") != "delivery_failed" || len(f.incidents) != 0 || jsjson.Path(state, "checked_at") != nil {
			t.Errorf("state = %s, incidents = %+v", compact(t, state), f.incidents)
		}
	})
	t.Run("STATE a directory: the intent write fails with rename EISDIR, nothing posted", func(t *testing.T) {
		f := newFixture(t)
		f.mkdir("supervisor-state.json")
		f.worker("b1", `"status": "failed"`)
		state, err := f.tick(`{}`)
		path := f.path("supervisor-state.json")
		wantRenameOntoDirError(t, err, path)
		if nodefs.ErrorCode(err) != "EISDIR" || len(f.messages()) != 0 || jsjson.Path(state, "events", "run-b1:failed", "status") != "intent_persisted" {
			t.Errorf("state = %s", compact(t, state))
		}
	})
	t.Run("a primitive worker file aborts the tick", func(t *testing.T) {
		f := newFixture(t)
		f.write("workers/a.json", `7`)
		f.worker("b", `"status": "finished"`)
		state, err := f.tick(`{"events": "ab"}`)
		wantError(t, err, "Cannot create property 'lane' on number '7'")
		if nodefs.ErrorCode(err) != "TypeError: Cannot create property 'lane' on number '7'" {
			t.Errorf("storage_error = %s", nodefs.ErrorCode(err))
		}
		if got := compact(t, state); got != `{"events":{"0":"a","1":"b"},"lanes":{}}` {
			t.Errorf("state = %s", got)
		}
	})
	t.Run("a primitive lanes throws at the assignment", func(t *testing.T) {
		f := newFixture(t)
		f.worker("b", `"status": "running"`)
		_, err := f.tick(`{"lanes": "abc"}`)
		wantError(t, err, "Cannot create property 'b' on string 'abc'")
	})
	t.Run("next_check abc of the same run: RangeError", func(t *testing.T) {
		f := newFixture(t)
		f.worker("b", `"status": "running"`)
		_, err := f.tick(`{"lanes": {"b": {"run": "run-b", "next_check": "abc"}}}`)
		if nodefs.ErrorCode(err) != "RangeError: Invalid time value" {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("a null config throws at the wake of an event", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.json", `null`)
		f.worker("b", `"status": "finished"`)
		state, err := f.tick(`{}`)
		wantError(t, err, "Cannot read properties of null (reading 'wake')")
		if jsjson.Path(state, "events", "run-b:finished", "status") != "cli_invoked" {
			t.Errorf("state = %s", compact(t, state))
		}
	})
	t.Run("a null channel", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.json", `{"wake": [{"type": "x"}, null]}`)
		f.worker("b", `"status": "finished"`)
		_, err := f.tick(`{}`)
		wantError(t, err, "Cannot read properties of null (reading 'type')")
	})
	t.Run("a wake that is not an array", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.json", `{"wake": {"type": "x"}}`)
		f.worker("b", `"status": "finished"`)
		_, err := f.tick(`{}`)
		if got := nodefs.ErrorCode(err); !strings.HasPrefix(got, "TypeError: ") {
			t.Errorf("storage_error = %q, want a TypeError", got)
		}
	})
	t.Run("a null event entry throws in pruneEvents", func(t *testing.T) {
		f := newFixture(t)
		state, err := f.tick(`{"events": {"x": null}}`)
		wantError(t, err, "Cannot read properties of null (reading 'created_at')")
		if got := compact(t, state); got != `{"events":{"x":null}}` {
			t.Errorf("state = %s", got)
		}
	})
	t.Run("a workers path that is a file: scandir ENOTDIR", func(t *testing.T) {
		f := newFixture(t)
		f.write("workers", "file")
		_, err := f.tick(`{}`)
		if nodefs.ErrorCode(err) != "ENOTDIR" {
			t.Errorf("error = %v", err)
		}
	})
}

func TestTickShapes(t *testing.T) {
	t.Run("PE-16: array lanes and an array worker are read as {}", func(t *testing.T) {
		f := newFixture(t)
		f.write("workers/a.json", `[1]`)
		state := f.mustTick(`{"lanes": [1]}`)
		if got := compact(t, jsjson.Path(state, "lanes", "a")); !strings.HasPrefix(got, `{"run":"undefined:a","phase":"unknown"`) {
			t.Errorf("lane = %s", got)
		}
	})
	t.Run("PE-16: null acks are read as none", func(t *testing.T) {
		f := newFixture(t)
		f.mkdir("logs")
		f.write("supervisor-acks.json", `null`)
		f.worker("b", `"status": "finished"`)
		state := f.mustTick(`{}`)
		if jsjson.Path(state, "events", "run-b:finished", "status") != "queued" {
			t.Errorf("state = %s", compact(t, state))
		}
	})
	t.Run("PE-16: a prototype-name breadth uses the standard cadence", func(t *testing.T) {
		f := newFixture(t)
		f.worker("b", `"status": "running", "pid": 7, "breadth": "constructor"`)
		f.live[7] = true
		state := f.mustTick(`{}`)
		if jsjson.Path(state, "lanes", "b", "breadth") != "standard" || jsjson.Path(state, "lanes", "b", "next_check") != float64(t0+300) {
			t.Errorf("lane = %s", compact(t, jsjson.Path(state, "lanes", "b")))
		}
	})
	t.Run("a falsy worker file is skipped; lanes of vanished workers stay", func(t *testing.T) {
		f := newFixture(t)
		f.write("workers/a.json", `0`)
		f.write("workers/c.json", `not json`)
		state := f.mustTick(`{"lanes": {"gone": {"run": "r"}}}`)
		if got := compact(t, jsjson.Path(state, "lanes")); got != `{"gone":{"run":"r"}}` {
			t.Errorf("lanes = %s", got)
		}
	})
	t.Run("a hand-edited next_check is passed through", func(t *testing.T) {
		f := newFixture(t)
		f.worker("b", `"status": "running", "pid": 7`)
		f.live[7] = true
		state := f.mustTick(`{"lanes": {"b": {"run": "run-b", "next_check": 1e12}}}`)
		if got := compact(t, jsjson.Path(state, "lanes", "b")); !strings.HasSuffix(got, `"next_check":1000000000000,"next_check_at":"+033658-09-27T01:46:40.000Z"}`) {
			t.Errorf("lane = %s", got)
		}
	})
}

func TestIsoSecondsIsNewDateTimesThousandToISOString(t *testing.T) {
	// Probed with Node v26.5.1.
	for _, tc := range []struct {
		in   any
		want string
	}{
		{1e12, "+033658-09-27T01:46:40.000Z"},
		{-1e12, "-029719-04-05T22:13:20.000Z"},
		{8.64e12, "+275760-09-13T00:00:00.000Z"},
		{8.64e12 + 1, "RangeError: Invalid time value"},
		{"abc", "RangeError: Invalid time value"},
		{[]any{5.0}, "1970-01-01T00:00:05.000Z"},
		{1.5, "1970-01-01T00:00:01.500Z"},
		{true, "1970-01-01T00:00:01.000Z"},
	} {
		got, err := isoSeconds(tc.in)
		if err != nil {
			got = jsjson.ErrorString(err)
		}
		if got != tc.want {
			t.Errorf("iso(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestTickLaneNamedProtoUsesThePrototypeSetter: `state.lanes["__proto__"] = entry` without an
// own key of that name replaces the prototype and writes no key; an own key (JSON.parse
// creates one) is assigned in place.
func TestTickLaneNamedProtoUsesThePrototypeSetter(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("workers/__proto__.json", `{"run_id": "r", "status": "finished"}`)
	state := f.mustTick(`{"lanes": {"a": 1}}`)
	wantJSON(t, compact(t, jsjson.Path(state, "lanes")), `{"a":1}`)

	f.at(1)
	state = f.mustTick(`{"lanes": {"__proto__": 1, "a": 1}}`)
	if got := strings.Join(jsjson.Path(state, "lanes").(*jsjson.Object).Keys(), ","); got != "__proto__,a" {
		t.Errorf("lanes keys %s", got)
	}
	if run := jsjson.Path(state, "lanes", "__proto__", "run"); run != "r" {
		t.Errorf("own __proto__ entry run %v", run)
	}
}

// TestTickKeepsANonStringRunIDAsItsJSValue: obs.run is worker.run_id itself, compared with
// the recorded run by ===, and recorded as that value.
func TestTickKeepsANonStringRunIDAsItsJSValue(t *testing.T) {
	cases := []struct {
		recorded string
		wantNext float64
	}{
		{`5`, t0 + 1000},  // same run: the recorded next_check stays
		{`"5"`, t0 + 120}, // "5" !== 5: a new run, next_check from now
		{`[5]`, t0 + 120}, // never === a value parsed from another file
	}
	for _, c := range cases {
		f := newFixture(t)
		f.live[7] = true
		f.write("workers/w.json", `{"run_id": 5, "status": "running", "pid": 7, "breadth": "focused", "updated_at": "`+iso(0)+`"}`)
		state := f.mustTick(`{"lanes": {"w": {"run": ` + c.recorded + `, "next_check": ` + jsjson.TemplateString(float64(t0+1000)) + `}}}`)
		if run := jsjson.Path(state, "lanes", "w", "run"); run != 5.0 {
			t.Errorf("recorded %s: run %#v, want the number 5", c.recorded, run)
		}
		if next := jsjson.Path(state, "lanes", "w", "next_check"); next != c.wantNext {
			t.Errorf("recorded %s: next_check %v, want %v", c.recorded, next, c.wantNext)
		}
	}
}
