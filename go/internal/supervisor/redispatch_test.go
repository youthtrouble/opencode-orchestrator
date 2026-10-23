package supervisor

import (
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

func TestRedispatchFailedLaneRetriesThenConfirmsOnce(t *testing.T) {
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
	f.at(10000)
	f.mustTick(compact(t, state))
	if len(f.runs) != 2 {
		t.Fatalf("confirmed launch repeated: %v", f.runs)
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

func TestRedispatchPendingBackoffStopsAfterFiveFailures(t *testing.T) {
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
	f.at(100000)
	state = compact(t, f.mustEvaluate(state))
	if len(f.launches()) != 5 {
		t.Fatalf("did not stop: %v", f.launches())
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
			f.at(100000)
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

func TestRedispatchPendingSuccessConfirmsAndPrunesOnlyObsoleteKeys(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("flip.json", `{"pending_launch":{"at":"a","to":"claude"}}`)
	f.runResult = SyncResult{Status: status(0), Stdout: conductorLaunched}
	state := f.mustTick(`{"redispatch":{"gone":{"attempts":1},"pending:old":{"attempts":1}}}`)
	if jsjson.Path(state, "redispatch", "pending:a", "confirmed_run_id") != "replacement" || jsjson.Path(state, "redispatch", "gone") != nil || jsjson.Path(state, "redispatch", "pending:old") != nil {
		t.Fatalf("records: %s", compact(t, state))
	}
	f.at(10000)
	state = f.mustTick(compact(t, state))
	if len(f.launches()) != 1 {
		t.Fatal("confirmed pending launch repeated")
	}
	f.write("flip.json", `{}`)
	f.at(20000)
	state = f.mustTick(compact(t, state))
	if jsjson.Path(state, "redispatch", "pending:a") != nil {
		t.Fatalf("obsolete pending key kept: %s", compact(t, state))
	}
}

func TestRedispatchAbsentConductorStopsAfterFiveFailures(t *testing.T) {
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
			f.at(4*601 + 2*3600)
			state = f.mustTick(compact(t, state))
			if len(f.launches()) != 5 || jsjson.Path(state, "redispatch", "conductor:none", "attempts") != float64(5) {
				t.Fatalf("sixth launch after stop: %s", compact(t, state))
			}
			messages := f.messages()
			if len(messages) != 1 || !strings.Contains(messages[0], `"to":"human","kind":"blocked","subject":"conductor relaunch stopped after 5 failed attempts"`) {
				t.Fatalf("blocked notes: %v", messages)
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
					key = "pending:a"
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
