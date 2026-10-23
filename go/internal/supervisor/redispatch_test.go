package supervisor

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const laneLaunched = `{"run_id":"replacement","runner_pid":123}`
const conductorLaunched = `{"flipped":true,"launcher":"{\"run_id\":\"replacement\",\"runner_pid\":123}","run_id":"replacement","runner_pid":123}`

func deferredFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.mkdir("logs")
	f.worker("b1", `"status":"deferred","brief":"b.md","harness":"claude","resume_after":"`+iso(-60)+`"`)
	return f
}

func TestRedispatchReplacementStateSurvivesLauncherReturn(t *testing.T) {
	for _, lane := range []string{"b1", "conductor"} {
		t.Run(lane, func(t *testing.T) {
			f := deferredFixture(t)
			if lane == "conductor" {
				f = relaunchFixture(t, strings.Replace(deadConductor, `"finished"`, `"failed"`, 1), `{}`, `{"action":"stay"}`)
			}
			const replacement = "{\n  \"run_id\": \"replacement\", \"status\": \"finished\", \"exit_code\": 0\n}\n"
			f.respond = func(spec SyncSpec) SyncResult {
				if spec.Args[1] == "decide" {
					return SyncResult{Status: status(0), Stdout: `{"action":"stay"}`}
				}
				if got := f.read("workers/" + lane + ".json"); strings.Contains(got, "redispatched_at") {
					t.Error("supervisor wrote lane state before launch")
				}
				f.write("workers/"+lane+".json", replacement)
				out := laneLaunched
				if lane == "conductor" {
					out = conductorLaunched
				}
				return SyncResult{Status: status(0), Stdout: out}
			}
			if lane == "conductor" {
				f.mustEvaluate(`{}`)
			} else {
				f.mustTick(`{}`)
			}
			if got := f.read("workers/" + lane + ".json"); got != replacement {
				t.Fatalf("replacement overwritten: %s", got)
			}
		})
	}
}

func TestRedispatchFailedLaneRetriesAfterUnpublishedConfirmation(t *testing.T) {
	f := deferredFixture(t)
	f.runResult = SyncResult{Status: status(1), Stderr: "failed"}
	state := f.mustTick(`{}`)
	if jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(1) || jsjson.Truthy(jsjson.Path(state, "redispatch", "run-b1", "confirmed_run_id")) {
		t.Fatalf("failure record: %s", compact(t, state))
	}
	f.at(59)
	state = f.mustTick(compact(t, state))
	if len(f.runs) != 1 {
		t.Fatalf("retried before backoff: %v", f.runs)
	}
	f.at(60)
	f.runResult = SyncResult{Status: status(0), Stdout: "noise\n" + laneLaunched + "\n"}
	state = f.mustTick(compact(t, state))
	if len(f.runs) != 2 || jsjson.Path(state, "redispatch", "run-b1", "confirmed_run_id") != "replacement" {
		t.Fatalf("not confirmed: %s", compact(t, state))
	}
	f.at(659)
	state = f.mustTick(compact(t, state))
	if len(f.runs) != 2 {
		t.Fatalf("confirmed launch repeated before publication grace: %v", f.runs)
	}
	f.at(660)
	// Inspect the same state at the launch boundary, before the next attempt is counted.
	f.respond = func(SyncSpec) SyncResult {
		record := jsjson.Path(state, "redispatch", "run-b1")
		if jsjson.Path(record, "confirmed_run_id") != nil || jsjson.Path(record, "attempts") != float64(2) || jsjson.Path(record, "last_attempt_at") != iso(60) {
			t.Fatalf("expiry changed launch accounting: %s", compact(t, record))
		}
		return SyncResult{Status: status(0), Stdout: laneLaunched}
	}
	if err := f.sup.Tick(state); err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 3 || jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(3) {
		t.Fatalf("unpublished confirmation did not retry: runs=%d, state=%s", len(f.runs), compact(t, state))
	}
	if !strings.Contains(f.logText(), "b1 launch replacement never published a lane; confirmation expired") {
		t.Fatalf("missing publication-loss log: %s", f.logText())
	}
}

func TestRedispatchUnpublishedConfirmationWaitsForKernelHolder(t *testing.T) {
	f := deferredFixture(t)
	f.runResult = SyncResult{Status: status(0), Stdout: laneLaunched}
	state := f.mustTick(`{}`)
	before := compact(t, jsjson.Path(state, "redispatch", "run-b1"))
	holder := f.holdLock("workers/b1.lock")
	f.at(601)
	state = f.mustTick(compact(t, state))
	if got := compact(t, jsjson.Path(state, "redispatch", "run-b1")); got != before || len(f.runs) != 1 {
		t.Fatalf("expired while a runner owns the lane: %s; runs=%d", got, len(f.runs))
	}
	if err := holder.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if code := holder.exitCode(t); code != 0 {
		t.Fatalf("lock holder exited %d", code)
	}
	state = f.mustTick(compact(t, state))
	if len(f.runs) != 2 || jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(2) {
		t.Fatalf("unpublished launch did not retry after holder exited: runs=%d, state=%s", len(f.runs), compact(t, state))
	}
}

func TestRedispatchUnpublishedFifthLaunchStopsAndNotesOnce(t *testing.T) {
	f := deferredFixture(t)
	state := f.mustTick(`{"redispatch":{"run-b1":{"attempts":5,"last_attempt_at":"` + iso(-600) + `","confirmed_run_id":"r5"}}}`)
	record := jsjson.Path(state, "redispatch", "run-b1")
	if jsjson.Path(record, "confirmed_run_id") != nil || jsjson.Path(record, "attempts") != float64(5) || jsjson.Path(record, "last_attempt_at") != iso(-600) || jsjson.Path(record, "blocked_noted_at") != iso(0) || len(f.runs) != 0 {
		t.Fatalf("unpublished fifth launch not stopped: %s; runs=%d", compact(t, record), len(f.runs))
	}
	blocked := f.sent("blocked")
	if len(blocked) != 1 || jsjson.Path(blocked[0], "to") != "coordinator" || jsjson.Path(blocked[0], "subject") != "b1 relaunch stopped after 5 failed attempts" {
		t.Fatalf("blocked notes: %v", blocked)
	}
	body := jsjson.Path(blocked[0], "body").(string)
	if !strings.Contains(body, "never published a lane") || !strings.Contains(body, "\nRetry by hand: orch worker start --brief b.md --harness claude --lane b1") {
		t.Fatalf("blocked body: %s", body)
	}
	messageCount := len(f.messages())
	f.at(10000)
	f.mustTick(compact(t, state))
	if len(f.messages()) != messageCount || len(f.runs) != 0 {
		t.Fatalf("stopped key repeated: messages=%v; runs=%v", f.messages(), f.runs)
	}
}

func TestRedispatchUnpublishedFifthLaunchArgumentErrorCanRecover(t *testing.T) {
	f := deferredFixture(t)
	validWorker := f.read("workers/b1.json")
	f.write("workers/b1.json", strings.TrimSuffix(validWorker, "}")+`,"launch_options":{"model":"bad\u0000model"}}`)
	state := mustParse(t, `{"redispatch":{"run-b1":{"attempts":5,"last_attempt_at":"`+iso(-600)+`","confirmed_run_id":"r5"}}}`).(*jsjson.Object)
	before := compact(t, jsjson.Path(state, "redispatch", "run-b1"))
	if err := f.sup.Tick(state); !errors.Is(err, errArgValue) {
		t.Fatalf("argument error: %v", err)
	}
	if got := compact(t, jsjson.Path(state, "redispatch", "run-b1")); got != before || len(f.sent("blocked")) != 0 || len(f.runs) != 0 {
		t.Fatalf("argument error consumed blocked note: record=%s; messages=%v; runs=%v", got, f.messages(), f.runs)
	}
	f.write("workers/b1.json", validWorker)
	f.onMailbox = func() {
		if jsjson.Path(state, "redispatch", "run-b1", "blocked_noted_at") != iso(0) {
			t.Error("blocked note not stamped before posting")
		}
	}
	if err := f.sup.Tick(state); err != nil {
		t.Fatal(err)
	}
	f.onMailbox = nil
	if len(f.sent("blocked")) != 1 || jsjson.Path(state, "redispatch", "run-b1", "confirmed_run_id") != nil || jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(5) {
		t.Fatalf("blocked note did not recover: %s; messages=%v", compact(t, state), f.messages())
	}
	f.at(10000)
	f.mustTick(compact(t, state))
	if len(f.sent("blocked")) != 1 || len(f.runs) != 0 {
		t.Fatalf("recovered blocked note repeated or launched: messages=%v; runs=%v", f.messages(), f.runs)
	}
}

func TestRedispatchExpiredFourthLaunchRetriesFifthInSameTick(t *testing.T) {
	f := deferredFixture(t)
	f.runResult = SyncResult{Status: status(0), Stdout: laneLaunched}
	state := f.mustTick(`{"redispatch":{"run-b1":{"attempts":4,"last_attempt_at":"` + iso(-600) + `","confirmed_run_id":"r4"}}}`)
	if len(f.runs) != 1 || jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(5) || jsjson.Path(state, "redispatch", "run-b1", "confirmed_run_id") != "replacement" || len(f.sent("blocked")) != 0 {
		t.Fatalf("fourth launch did not retry as fifth: %s; runs=%v; messages=%v", compact(t, state), f.runs, f.messages())
	}
}

func TestRedispatchPublishedFinalReplacementIsNotRestarted(t *testing.T) {
	for _, finalStatus := range []string{"failed", "finished"} {
		t.Run(finalStatus, func(t *testing.T) {
			f := deferredFixture(t)
			replacement := `{"run_id":"replacement","status":"` + finalStatus + `"}`
			f.respond = func(SyncSpec) SyncResult {
				f.write("workers/b1.json", replacement)
				return SyncResult{Status: status(0), Stdout: laneLaunched}
			}
			state := f.mustTick(`{}`)
			if jsjson.Path(state, "redispatch", "run-b1") != nil {
				t.Fatalf("published replacement kept old key: %s", compact(t, state))
			}
			for _, at := range []float64{600, 10000} {
				f.at(at)
				state = f.mustTick(compact(t, state))
				if len(f.runs) != 1 || f.read("workers/b1.json") != replacement {
					t.Fatalf("replacement restarted or rewritten: runs=%v; lane=%s", f.runs, f.read("workers/b1.json"))
				}
			}
		})
	}
}

func TestRedispatchUnpublishedConfirmationGuards(t *testing.T) {
	for _, guard := range []string{"conductor", "opt-out", "not deferred", "legacy stamp", "resume not due"} {
		t.Run(guard, func(t *testing.T) {
			f := deferredFixture(t)
			lane, config := "b1", `{}`
			fields := `"status":"deferred","brief":"b.md","harness":"claude","resume_after":"` + iso(-60) + `"`
			switch guard {
			case "conductor":
				lane = "conductor"
				if err := os.Remove(f.path("workers/b1.json")); err != nil {
					t.Fatal(err)
				}
			case "opt-out":
				config = `{"auto_redispatch_deferred":false}`
			case "not deferred":
				fields = `"status":"failed"`
			case "legacy stamp":
				fields += `,"redispatched_at":"legacy"`
			case "resume not due":
				fields = strings.ReplaceAll(fields, iso(-60), iso(1))
			}
			f.worker(lane, fields)
			f.write("supervisor.json", config)
			record := `{"attempts":2,"last_attempt_at":"` + iso(-600) + `","confirmed_run_id":"replacement"}`
			state := f.mustTick(fmt.Sprintf(`{"last_flip_check":%d,"redispatch":{"run-%s":%s}}`, t0, lane, record))
			if got := compact(t, jsjson.Path(state, "redispatch", "run-"+lane)); got != record || len(f.runs) != 0 || strings.Contains(f.logText(), "never published") {
				t.Fatalf("guard changed confirmation: %s; runs=%v; log=%s", got, f.runs, f.logText())
			}
		})
	}
}

func TestRedispatchFailedConductorRetriesThenConfirmsOnce(t *testing.T) {
	f := relaunchFixture(t, strings.Replace(deadConductor, `"finished"`, `"failed"`, 1), `{}`, `{"action":"stay"}`)
	out := SyncResult{Status: status(1), Stderr: "failed"}
	f.respond = func(spec SyncSpec) SyncResult {
		if spec.Args[1] == "decide" {
			return SyncResult{Status: status(0), Stdout: `{"action":"stay"}`}
		}
		return out
	}
	state := f.mustEvaluate(`{}`)
	f.at(60)
	state = f.mustEvaluate(compact(t, state))
	if len(f.launches()) != 1 {
		t.Fatal("project cooldown ignored")
	}
	f.at(600)
	out = SyncResult{Status: status(0), Stdout: conductorLaunched}
	state = f.mustEvaluate(compact(t, state))
	if len(f.launches()) != 2 || jsjson.Path(state, "redispatch", "9:conductor", "confirmed_run_id") != "replacement" {
		t.Fatalf("retry lost: %s; runs %v", compact(t, state), f.runs)
	}
	f.at(10000)
	f.mustEvaluate(compact(t, state))
	if len(f.launches()) != 2 {
		t.Fatal("failed-status lane relaunched after confirmation")
	}
}

func TestRedispatchPendingBackoffSlowsAfterFiveFailures(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("flip.json", `{"pending_launch":{"at":"pending-at","to":"claude","from":"opencode"}}`)
	f.runResult = SyncResult{Status: status(1), Stderr: "last failure"}
	state := `{}`
	for i, at := range []float64{0, 60, 180, 420, 900} {
		f.at(at)
		state = compact(t, f.mustEvaluate(state))
		if len(f.launches()) != i+1 {
			t.Fatalf("launch %d at %v: %v", i+1, at, f.launches())
		}
		if i > 0 && i < 4 {
			f.at(at + 60)
			state = compact(t, f.mustEvaluate(state))
			if len(f.launches()) != i+1 {
				t.Fatalf("early retry at %v", at+60)
			}
		}
	}
	f.at(900 + 1799)
	state = compact(t, f.mustEvaluate(state))
	if len(f.launches()) != 5 {
		t.Fatalf("did not slow down: %v", f.launches())
	}
	msgs := f.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], `"to":"human","kind":"blocked","subject":"conductor relaunch stopped after 5 failed attempts"`) || !strings.Contains(msgs[0], "last failure") || !strings.Contains(msgs[0], "orch flip run") {
		t.Fatalf("blocked notes: %v; state %s", msgs, state)
	}
}

func TestRedispatchRejectsUnconfirmedLauncherOutput(t *testing.T) {
	for _, out := range []SyncResult{
		{Status: status(0), Stdout: `{"run_id":"replacement"}`},
		{Status: status(1), Stdout: laneLaunched},
		{Status: status(0), Stderr: laneLaunched},
		{Status: status(0), Stdout: laneLaunched + "\ntrailer"},
		{Status: status(0), Stdout: `{"run_id":1,"runner_pid":123}`},
		{Status: status(0), Stdout: `{"run_id":"replacement","runner_pid":"123"}`},
	} {
		f := deferredFixture(t)
		f.runResult = out
		state := f.mustTick(`{}`)
		if jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(1) || jsjson.Truthy(jsjson.Path(state, "redispatch", "run-b1", "confirmed_run_id")) {
			t.Fatalf("accepted %+v: %s", out, compact(t, state))
		}
	}
}

func TestRedispatchConductorNonAttemptsDoNotStampState(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, out := range []SyncResult{
			{Status: status(0), Stdout: `{"flipped":false,"pending":true}`},
			{Status: status(1), Stdout: `{"flipped":false,"reason":"awaiting human"}`},
		} {
			f := relaunchFixture(t, deadConductor, `{}`, `{"action":"stay"}`)
			if pending {
				f.write("flip.json", `{"pending_launch":{"at":"a","to":"claude"}}`)
			}
			f.respond = func(spec SyncSpec) SyncResult {
				if spec.Args[1] == "decide" {
					return SyncResult{Status: status(0), Stdout: `{"action":"stay"}`}
				}
				return out
			}
			state := f.mustEvaluate(`{}`)
			if jsjson.Path(state, "last_relaunch_at") != nil || jsjson.Path(state, "redispatch") != nil {
				t.Fatalf("non-attempt changed state: %s", compact(t, state))
			}
		}
	}
}

func TestRedispatchNoteFailureStillPersistsAndContinues(t *testing.T) {
	f := deferredFixture(t)
	f.write("mailbox", "file")
	f.worker("b2", `"status":"paused"`)
	state := jsjson.NewObject()
	if err := f.sup.serveTick(state); err != nil {
		t.Fatal(err)
	}
	if jsjson.Path(state, "lanes", "b2") == nil || jsjson.Path(state, "storage_failures") != nil {
		t.Fatalf("tick aborted: %s", compact(t, state))
	}
	persisted := mustParse(t, f.read("supervisor-state.json"))
	if jsjson.Path(persisted, "redispatch", "run-b1", "attempts") != float64(1) || strings.Count(f.logText(), "note post failed") != 1 {
		t.Fatalf("record/log lost: %s / %s", compact(t, persisted), f.logText())
	}
}

func TestRedispatchPrunesReplacedAndRemovedRunKeys(t *testing.T) {
	f := deferredFixture(t)
	state := f.mustTick(`{}`)
	f.worker("b1", `"status":"paused"`)
	file := strings.Replace(f.read("workers/b1.json"), "run-b1", "new-run", 1)
	f.write("workers/b1.json", file)
	state = f.mustTick(compact(t, state))
	if jsjson.Path(state, "redispatch", "run-b1") != nil {
		t.Fatalf("old key kept: %s", compact(t, state))
	}
	if err := os.Remove(f.path("workers/b1.json")); err != nil {
		t.Fatal(err)
	}
	f.mustTick(compact(t, state))
}

func TestRedispatchFifthFailureNotesAreSingleAndAddressed(t *testing.T) {
	for _, lane := range []string{"b1", "conductor"} {
		t.Run(lane, func(t *testing.T) {
			f := deferredFixture(t)
			key, recipient := "run-b1", "coordinator"
			if lane == "conductor" {
				f = relaunchFixture(t, strings.Replace(deadConductor, `"finished"`, `"failed"`, 1), `{}`, `{"action":"stay"}`)
				key, recipient = "9:conductor", "human"
				f.respond = func(spec SyncSpec) SyncResult {
					if spec.Args[1] == "decide" {
						return SyncResult{Status: status(0), Stdout: `{"action":"stay"}`}
					}
					return SyncResult{Status: status(1), Stderr: strings.Repeat("x", 600)}
				}
			} else {
				f.runResult = SyncResult{Status: status(1), Stderr: strings.Repeat("x", 600)}
			}
			state := `{"redispatch":{"` + key + `":{"attempts":4,"last_attempt_at":"` + iso(-480) + `","confirmed_run_id":null}}}`
			run := f.mustTick
			if lane == "conductor" {
				run = f.mustEvaluate
			}
			object := run(state)
			f.at(1799) // before a conductor key's slow retry
			run(compact(t, object))
			blocked := []any{}
			for _, line := range f.messages() {
				msg := mustParse(t, line)
				if jsjson.Path(msg, "kind") == "blocked" {
					blocked = append(blocked, msg)
				}
			}
			if len(blocked) != 1 || jsjson.Path(blocked[0], "to") != recipient || jsjson.Path(blocked[0], "subject") != lane+" relaunch stopped after 5 failed attempts" {
				t.Fatalf("blocked notes: %v", blocked)
			}
			body := jsjson.Path(blocked[0], "body").(string)
			if !strings.HasPrefix(body, strings.Repeat("x", 500)+"\nRetry by hand: orch ") || strings.Contains(body, strings.Repeat("x", 501)) {
				t.Fatalf("body: %s", body)
			}
		})
	}
}

func TestRedispatchBlockedNoteAndItsLogCanFailWithoutRetry(t *testing.T) {
	f := deferredFixture(t)
	f.write("mailbox", "file")
	// Force both the blocked-note post and its diagnostic append to fail.
	f.mkdir("logs/supervisor.log")
	state := f.mustTick(`{"events":{"run-b1:deferred":{"status":"queued"}},"redispatch":{"run-b1":{"attempts":4,"last_attempt_at":"` + iso(-480) + `","confirmed_run_id":null}}}`)
	if jsjson.Path(state, "redispatch", "run-b1", "attempts") != float64(5) {
		t.Fatalf("lost record: %s", compact(t, state))
	}
	f.at(100000)
	// Suppress the independent deferred wake so only redispatch failure handling is tested.
	f.write("supervisor-acks.json", `{"run-b1:deferred":true}`)
	f.mustTick(compact(t, state))
	if len(f.runs) != 1 {
		t.Fatalf("stopped key retried: %v", f.runs)
	}
}

func TestRedispatchConductorLogErrorKeepsRecordForNextWrite(t *testing.T) {
	f := relaunchFixture(t, strings.Replace(deadConductor, `"finished"`, `"failed"`, 1), `{}`, `{"action":"stay"}`)
	f.mkdir("logs/supervisor.log")
	state := mustParse(t, `{"events":{"9:conductor:failed":{"status":"queued"}}}`).(*jsjson.Object)
	if err := f.sup.serveTick(state); err != nil {
		t.Fatal(err)
	}
	if jsjson.Path(state, "redispatch", "9:conductor", "attempts") != float64(1) || jsjson.Path(state, "last_relaunch_at") != float64(t0) {
		t.Fatalf("record lost: %s", compact(t, state))
	}
	if err := os.Remove(f.path("logs/supervisor.log")); err != nil {
		t.Fatal(err)
	}
	f.at(60)
	if err := f.sup.serveTick(state); err != nil {
		t.Fatal(err)
	}
	persisted := mustParse(t, f.read("supervisor-state.json"))
	if jsjson.Path(persisted, "redispatch", "9:conductor", "attempts") != float64(1) || len(f.launches()) != 1 {
		t.Fatalf("record not persisted: %s", compact(t, persisted))
	}
}

// A pending launch records under the conductor slot key (S13o v3); pending:<at> keys are
// retired, so a leftover one is pruned like any obsolete key.
func TestRedispatchPendingSuccessConfirmsAndPrunesOnlyObsoleteKeys(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("flip.json", `{"pending_launch":{"at":"a","to":"claude"}}`)
	f.respond = func(SyncSpec) SyncResult {
		f.write("workers/conductor.json", `{"run_id":"replacement","status":"running","harness":"claude","pid":123}`)
		f.live[123] = true
		return SyncResult{Status: status(0), Stdout: conductorLaunched}
	}
	state := f.mustTick(`{"redispatch":{"gone":{"attempts":1},"pending:old":{"attempts":1}}}`)
	if jsjson.Path(state, "redispatch", "conductor:none", "confirmed_run_id") != "replacement" || jsjson.Path(state, "redispatch", "gone") != nil || jsjson.Path(state, "redispatch", "pending:old") != nil {
		t.Fatalf("records: %s", compact(t, state))
	}
	f.at(10000)
	state = f.mustTick(compact(t, state))
	if len(f.launches()) != 1 || jsjson.Path(state, "redispatch", "conductor:none") != nil {
		t.Fatalf("confirmed pending launch repeated or slot key kept after publication: %s", compact(t, state))
	}
}

func TestRedispatchAbsentConductorSlowsAfterFiveFailures(t *testing.T) {
	for _, decision := range []string{`{"action":"stay"}`, `{"action":"flip","to":"claude"}`} {
		t.Run(decision, func(t *testing.T) {
			f := relaunchFixture(t, "", `{"paused":true}`, decision)
			f.respond = func(spec SyncSpec) SyncResult {
				if spec.Args[1] == "decide" {
					return SyncResult{Status: status(0), Stdout: decision}
				}
				return SyncResult{Status: status(1), Stderr: "bootstrap failed"}
			}
			state := jsjson.NewObject()
			for attempt := 1; attempt <= 5; attempt++ {
				f.at(float64((attempt - 1) * 601))
				state = f.mustTick(compact(t, state))
				if len(f.launches()) != attempt || jsjson.Path(state, "redispatch", "conductor:none", "attempts") != float64(attempt) {
					t.Errorf("attempt %d: launches=%d, records=%s", attempt, len(f.launches()), compact(t, jsjson.Path(state, "redispatch")))
				}
			}
			f.at(4*601 + 1740) // the last check before the slow retry (checks run a minute apart)
			state = f.mustTick(compact(t, state))
			if len(f.launches()) != 5 || jsjson.Path(state, "redispatch", "conductor:none", "attempts") != float64(5) {
				t.Fatalf("sixth launch before the slow retry: %s", compact(t, state))
			}
			f.at(4*601 + 1800)
			state = f.mustTick(compact(t, state))
			if len(f.launches()) != 6 || jsjson.Path(state, "redispatch", "conductor:none", "attempts") != float64(6) {
				t.Fatalf("no slow retry: %s", compact(t, state))
			}
			blocked := f.sent("blocked")
			if len(blocked) != 1 || jsjson.Path(blocked[0], "to") != "human" || jsjson.Path(blocked[0], "subject") != "conductor relaunch stopped after 5 failed attempts" {
				t.Fatalf("blocked notes: %v", blocked)
			}
		})
	}
}

func TestRedispatchConductorSlotPrunesSyntheticKeyAfterPublication(t *testing.T) {
	f := relaunchFixture(t, "", `{"paused":true}`, `{"action":"stay"}`)
	f.respond = func(spec SyncSpec) SyncResult {
		if spec.Args[1] == "decide" {
			return SyncResult{Status: status(0), Stdout: `{"action":"stay"}`}
		}
		f.write("workers/conductor.json", `{"run_id":"R","status":"running","harness":"opencode","pid":123}`)
		f.live[123] = true
		return SyncResult{Status: status(0), Stdout: strings.ReplaceAll(conductorLaunched, "replacement", "R")}
	}
	state := f.mustTick(`{"redispatch":{"obsolete":{"attempts":1}}}`)
	if jsjson.Path(state, "redispatch", "conductor:none", "confirmed_run_id") != "R" || jsjson.Path(state, "redispatch", "obsolete") != nil {
		t.Fatalf("publication record: %s", compact(t, state))
	}
	f.at(601)
	state = f.mustTick(compact(t, state))
	if jsjson.Path(state, "redispatch", "conductor:none") != nil || len(f.launches()) != 1 {
		t.Fatalf("synthetic key retained after publication: %s", compact(t, state))
	}
}

func TestRedispatchPrimitiveConductorRetainsSlotKey(t *testing.T) {
	f := relaunchFixture(t, `5`, `{}`, `{"action":"flip","to":"claude"}`)
	state := jsjson.NewObject()
	for attempt := 1; attempt <= 2; attempt++ {
		f.at(float64((attempt - 1) * 601))
		// Tick's primitive-worker rejection is S13k. Exercise its prune/evaluation
		// boundary directly to preserve that separate, unchanged behavior.
		if err := f.sup.pruneRedispatch(state); err != nil {
			t.Fatal(err)
		}
		state = f.mustEvaluate(compact(t, state))
		if jsjson.Path(state, "redispatch", "conductor:none", "attempts") != float64(attempt) || f.read("workers/conductor.json") != "5" {
			t.Fatalf("primitive slot lost or rewritten: %s", compact(t, state))
		}
	}
}

// E-L1: a legacy redispatched_at means only "restart done". It never blocks a decided
// flip or a pause-resume relaunch.
func TestRedispatchLegacyStampBlocksOnlyRestarts(t *testing.T) {
	stamped := func(status string) string {
		return strings.Replace(strings.Replace(deadConductor, `"finished"`, `"`+status+`"`, 1), `}`, `, "redispatched_at": "x"}`, 1)
	}
	for _, c := range []struct {
		name, lane, flip, decision string
		wantArgs                   []string // the launch's --to and --from; nil when nothing launches
	}{
		{"flip to the other door", stamped("finished"), `{}`, `{"action":"flip","to":"claude"}`, []string{"--to", "claude", "--from", "opencode"}},
		{"paused stay", stamped("finished"), `{"paused":true}`, `{"action":"stay"}`, []string{"--to", "opencode", "--from", "opencode"}},
		{"failed stay", stamped("failed"), `{}`, `{"action":"stay"}`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := relaunchFixture(t, c.lane, c.flip, c.decision)
			f.mustEvaluate(`{}`)
			launches := f.launches()
			if c.wantArgs == nil {
				if len(launches) != 0 {
					t.Fatalf("launched %+v", launches)
				}
				return
			}
			if len(launches) != 1 || !reflect.DeepEqual(launches[0].Args[3:7], c.wantArgs) {
				t.Fatalf("launches %+v, want one with %q", launches, c.wantArgs)
			}
		})
	}
	t.Run("deferred lane", func(t *testing.T) {
		f := newFixture(t)
		f.mkdir("logs")
		f.worker("b1", `"status":"deferred","brief":"b.md","harness":"claude","resume_after":"`+iso(-60)+`","redispatched_at":"x"`)
		f.mustTick(`{}`)
		if len(f.runs) != 0 {
			t.Fatalf("redispatched a stamped lane: %+v", f.runs)
		}
	})
}

// E-L2: the supervisor reads run_id and runner_pid from the answer's top level, so a
// launcher quote cut at 400 units still confirms the launch.
func TestRedispatchConductorReadsTopLevelLaunchFields(t *testing.T) {
	cut := `{"flipped":true,"to":"claude","from":"opencode","launcher":"{\"run_id\":\"replacement\",\"lane\":\"conductor\",\"cwd\":\"/very/lo","run_id":"replacement","runner_pid":123}`
	for _, c := range []struct {
		name, stdout string
		want         any
		exit         int
	}{
		{"cut launcher, top-level fields", cut, "replacement", 0},
		{"exit 1", cut, nil, 1},
		{"nested launcher only", `{"flipped":true,"launcher":"{\"run_id\":\"r\",\"runner_pid\":123}"}`, nil, 0},
		{"no runner_pid", `{"flipped":true,"launcher":"x","run_id":"r"}`, nil, 0},
		{"string runner_pid", `{"flipped":true,"launcher":"x","run_id":"r","runner_pid":"123"}`, nil, 0},
		{"numeric run_id", `{"flipped":true,"launcher":"x","run_id":1,"runner_pid":123}`, nil, 0},
		{"not flipped", `{"flipped":false,"launcher":"x","run_id":"r","runner_pid":123}`, nil, 0},
	} {
		for _, path := range []string{"decided", "pending"} {
			t.Run(c.name+"/"+path, func(t *testing.T) {
				f := relaunchFixture(t, deadConductor, `{}`, `{"action":"stay"}`)
				key := "9:conductor"
				if path == "pending" {
					f.write("flip.json", `{"pending_launch":{"at":"a","to":"claude"}}`)
					key = "9:conductor" // the slot key: pending launches record under it
				}
				f.respond = func(spec SyncSpec) SyncResult {
					if spec.Args[1] == "decide" {
						return SyncResult{Status: status(0), Stdout: `{"action":"stay"}`}
					}
					return SyncResult{Status: status(c.exit), Stdout: c.stdout}
				}
				state := f.mustEvaluate(`{}`)
				if got := jsjson.Path(state, "redispatch", key, "confirmed_run_id"); got != c.want || jsjson.Path(state, "redispatch", key, "attempts") != float64(1) {
					t.Fatalf("record %s, want confirmed_run_id %v", compact(t, state), c.want)
				}
			})
		}
	}
}

// E-N1: a failed lane attempt is not announced as a re-dispatch; the fifth failure posts
// only the blocked note.
func TestRedispatchLaneNoteSubjects(t *testing.T) {
	f := deferredFixture(t)
	f.runResult = SyncResult{Status: status(1), Stderr: "failed"}
	state := `{}`
	for i, at := range []float64{0, 60, 180, 420, 900} {
		f.at(at)
		state = compact(t, f.mustTick(state))
		msgs := f.messages()
		if i < 4 {
			want := fmt.Sprintf(`"kind":"note","subject":"b1 re-dispatch failed (attempt %d of 5)","body":"failed"`, i+1)
			if len(msgs) != i+1 || !strings.Contains(msgs[i], want) {
				t.Fatalf("attempt %d notes %v, want %s", i+1, msgs, want)
			}
			continue
		}
		if len(msgs) != 5 || !strings.Contains(msgs[4], `"kind":"blocked","subject":"b1 relaunch stopped after 5 failed attempts"`) {
			t.Fatalf("fifth failure notes %v", msgs)
		}
	}

	f = deferredFixture(t)
	f.runResult = SyncResult{Status: status(0), Stdout: laneLaunched}
	f.mustTick(`{}`)
	if msgs := f.messages(); len(msgs) != 1 || !strings.Contains(msgs[0], `"kind":"note","subject":"b1 re-dispatched after budget reset"`) {
		t.Fatalf("success notes %v", msgs)
	}
}

func TestRedispatchArgsReplayLaunchOptions(t *testing.T) {
	const head = `"brief":"b.md","harness":"claude","model":"m","session_id":"s"`
	today := []string{"worker", "start", "--brief", "b.md", "--harness", "claude", "--lane", "b1", "--breadth", "standard", "--cwd", "/p", "--detach", "--model", "m", "--resume", "s"}
	for _, tc := range []struct {
		name   string
		worker string
		extra  []string
	}{
		{"older state", `{` + head + `}`, nil},
		{"non-object value", `{` + head + `,"launch_options":"--effort high"}`, nil},
		{"empty object", `{` + head + `,"launch_options":{}}`, nil},
		{"options in key order", `{` + head + `,"launch_options":{"effort":"high","timeout-min":"30","agent":"conductor","permission-mode":"auto"}}`,
			[]string{"--effort", "high", "--timeout-min", "30", "--agent", "conductor", "--permission-mode", "auto"}},
		{"root lane", `{` + head + `,"launch_options":{"root":"/p","cwd":"/p/child"}}`, []string{"--root", "/p", "--cwd", "/p/child"}},
	} {
		worker, err := jsjson.Unmarshal([]byte(tc.worker))
		if err != nil {
			t.Fatal(err)
		}
		got, err := spawnArgs(redispatchArgs("b1", worker.(*jsjson.Object), "/p")...)
		if want := append(append([]string{}, today...), tc.extra...); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: redispatchArgs %q, %v; want %q", tc.name, got, err, want)
		}
	}
}
