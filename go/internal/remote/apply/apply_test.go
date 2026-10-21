package apply

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/supervisor"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

const (
	projectKey = "0123456789abcdef0123456789abcdef"
	livePid    = 4242
	at         = "2026-10-21T09:30:00.000Z"
)

var fixedNow = time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)

// fixture is one temp project behind an Applier whose only live process is livePid and whose
// discovery finds the project unless undiscovered is set. Its supervisor launcher records its calls and, unless told otherwise, leaves a live supervisor
// holding the lock; its sleeper records each wait and runs onSleep.
type fixture struct {
	root              string
	stateDir          string
	undiscovered      bool
	applier           Applier
	supervisorStarts  []string
	supervisorFailure error
	startLeavesNoLock bool
	sleeps            []time.Duration
	onSleep           func()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureAt(t, t.TempDir(), t.TempDir())
}

// restarted is a fixture on the same project and remote state, as a restarted process sees
// them.
func (f *fixture) restarted(t *testing.T) *fixture {
	t.Helper()
	return newFixtureAt(t, f.root, f.stateDir)
}

func newFixtureAt(t *testing.T, root, stateDir string) *fixture {
	t.Helper()
	f := &fixture{root: root, stateDir: stateDir}
	registryFile := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, registryFile, `{"conductor":{"home":"claude"}}`)
	n := 0
	f.applier = Applier{
		Projects: func(key string) (string, bool) {
			return f.root, key == projectKey && !f.undiscovered
		},
		RegistryFile: registryFile,
		StateDir:     stateDir,
		Alive:        func(pid any) bool { return pid == float64(livePid) },
		StartSupervisor: func(root string) error {
			f.supervisorStarts = append(f.supervisorStarts, root)
			if f.supervisorFailure == nil && !f.startLeavesNoLock {
				f.holdSupervisorLock(t)
			}
			return f.supervisorFailure
		},
		Sleep: func(d time.Duration) {
			f.sleeps = append(f.sleeps, d)
			if f.onSleep != nil {
				f.onSleep()
			}
		},
		Now:   func() time.Time { return fixedNow },
		NewID: func() string { n++; return fmt.Sprintf("id-%d", n) },
	}
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (f *fixture) box() *mailbox.Mailbox { return f.applier.mailbox(f.root) }

func (f *fixture) messages(t *testing.T) []string {
	return readLines(t, filepath.Join(f.box().Dir, "messages.jsonl"))
}

func (f *fixture) acks(t *testing.T) []string {
	return readLines(t, filepath.Join(f.box().Dir, "acks.jsonl"))
}

// post appends a message and returns its id.
func (f *fixture) post(t *testing.T, d mailbox.Draft) string {
	t.Helper()
	msg, err := f.box().Post(d)
	if err != nil {
		t.Fatal(err)
	}
	return jsjson.Path(msg, "id").(string)
}

func (f *fixture) question(t *testing.T) string {
	recommended := "ship it"
	return f.post(t, mailbox.Draft{From: "conductor-claude", To: "human", Kind: "question", Subject: "Ship?", Recommended: &recommended})
}

func (f *fixture) flipPath() string { return flipcmd.NewProject(f.root).FlipPath }

func (f *fixture) flip(t *testing.T) any {
	t.Helper()
	data, err := os.ReadFile(f.flipPath())
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// holdSupervisorLock is a live supervisor claiming supervisor.lock.
func (f *fixture) holdSupervisorLock(t *testing.T) {
	writeFile(t, supervisor.NewProject(f.root).LockPath, fmt.Sprintf("%d\n", livePid))
}

func (f *fixture) journal() actionJournal { return newActionJournal(f.root) }

// makeJournalDir creates the action journal's directory, so a test can make .orchestrator
// read-only and still have the action recorded.
func (f *fixture) makeJournalDir(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.journal().path), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) runConductor(t *testing.T, harness string) {
	writeFile(t, worker.NewProject(f.root).StatePath("conductor"), fmt.Sprintf(`{"lane":"conductor","harness":%q,"status":"running","pid":%d}`, harness, livePid))
}

func answer(questionID, kind, text string) wire.PulledAnswer {
	return wire.PulledAnswer{ID: "answer-1", ProjectKey: projectKey, QuestionID: questionID, Kind: kind, Text: text, CreatedAt: at}
}

func action(kind string) wire.PulledAction {
	return actionWithID("action-1", kind)
}

func actionWithID(id, kind string) wire.PulledAction {
	return wire.PulledAction{ID: id, ProjectKey: projectKey, Kind: kind, CreatedAt: at}
}

func expect(t *testing.T, got Outcome, err error, want Outcome) {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("outcome = %+v, want %+v", got, want)
	}
}

// A typed answer is posted as typed; an accept posts the recommendation the mailbox holds,
// whatever text the Worker sent with it.
func TestAnAnswerIsPostedAsTheHumansRemoteReplyAndAcked(t *testing.T) {
	for _, c := range []struct{ kind, body string }{{"accept", "ship it"}, {"typed", "ship it today"}} {
		t.Run(c.kind, func(t *testing.T) {
			f := newFixture(t)
			q := f.question(t)
			got, err := f.applier.ApplyAnswer(answer(q, c.kind, "ship it today"))
			expect(t, got, err, Outcome{Applied, "replied"})
			want := `{"id":"id-2","at":"` + at + `","from":"human","to":"conductor-claude","kind":"note","subject":"Re: Ship?","body":"` + c.body + `","refs":[],"thread":"id-1","via":"remote","remote":{"answer_id":"answer-1","kind":"` + c.kind + `"}}`
			if msgs := f.messages(t); len(msgs) != 2 || msgs[1] != want {
				t.Errorf("messages = %q\nwant reply %s", msgs, want)
			}
			if acks := f.acks(t); len(acks) != 1 || acks[0] != `{"id":"id-1","by":"human","at":"`+at+`","note":"replied via Work Trains"}` {
				t.Errorf("acks = %q", acks)
			}
		})
	}
}

// The Worker holds the recommendation as the snapshot redacted it; accepting it must deliver what
// the conductor wrote, paths and all.
func TestAnAcceptDeliversTheOriginalRecommendationNotTheRedactedCopy(t *testing.T) {
	f := newFixture(t)
	original := "Accept all five defaults: GET /v1/installations/me; keep the dump in /Users/someone/backups/db.sql"
	q := f.post(t, mailbox.Draft{From: "conductor-claude", To: "human", Kind: "question", Subject: "Defaults?", Recommended: &original})
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "Accept all five defaults: GET /v1/installations/me; keep the dump in <path>"))
	expect(t, got, err, Outcome{Applied, "replied"})
	msgs := f.messages(t)
	if len(msgs) != 2 || !strings.Contains(msgs[1], `"body":"`+original+`"`) {
		t.Errorf("messages = %q\nwant a reply whose body is %q", msgs, original)
	}
}

// A question posted before the recommended/blocking contract has no recommendation: an accept of it
// is refused for the page to show, and nothing, placeholder or redacted copy, is posted.
func TestAnAcceptOfAQuestionWithoutARecommendationIsRejected(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.box().Dir, "messages.jsonl"),
		`{"id":"legacy-1","at":"`+at+`","from":"conductor-claude","to":"human","kind":"question","subject":"Ship?","body":"","refs":[],"thread":null}`+"\n")
	got, err := f.applier.ApplyAnswer(answer("legacy-1", "accept", "ship it"))
	expect(t, got, err, Outcome{Rejected, "the question has no recommendation on the Mac; reply instead"})
	if len(f.messages(t)) != 1 || f.acks(t) != nil {
		t.Errorf("messages = %q, acks = %q", f.messages(t), f.acks(t))
	}
}

func TestAnAnswerOfAnUnknownKindIsRejected(t *testing.T) {
	f := newFixture(t)
	q := f.question(t)
	got, err := f.applier.ApplyAnswer(answer(q, "accept_recommended", "ship it"))
	expect(t, got, err, Outcome{Rejected, "unknown answer kind"})
	if len(f.messages(t)) != 1 || f.acks(t) != nil {
		t.Errorf("messages = %q, acks = %q", f.messages(t), f.acks(t))
	}
}

func TestAnAnswerAppliedTwiceIsWrittenOnce(t *testing.T) {
	f := newFixture(t)
	q := f.question(t)
	if _, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it")); err != nil {
		t.Fatal(err)
	}
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "already applied"})
	if len(f.messages(t)) != 2 || len(f.acks(t)) != 1 {
		t.Errorf("messages = %q, acks = %q", f.messages(t), f.acks(t))
	}
}

// A crash (or a failed ack) can leave the reply posted while its question is not yet acked or
// the gate it answers not yet cleared; the retried pull finishes both without a second reply.
func TestARetriedAnswerFinishesWhatACrashLeftUndone(t *testing.T) {
	for _, c := range []struct {
		name  string
		acked bool
	}{
		{"posted, not acked", false},
		{"posted and acked, gate not cleared", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			q := f.question(t)
			f.post(t, mailbox.Draft{From: "human", To: "conductor-claude", Kind: "note", Subject: "Re: Ship?", Body: "ship it", Thread: q,
				Remote: &mailbox.RemoteRef{AnswerID: "answer-1", Kind: "accept"}})
			if c.acked {
				human := "human"
				if _, err := f.box().Ack(mailbox.AckRequest{ID: &q, By: &human, Note: "replied via Work Trains"}); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, f.flipPath(), `{"awaiting_human":{"at":"x","door":"claude","reason":"r","question":"`+q+`"}}`)
			got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
			expect(t, got, err, Outcome{Applied, "already applied; cleared the human gate, the claude conductor relaunches within a minute"})
			if msgs := f.messages(t); len(msgs) != 2 {
				t.Errorf("messages = %q", msgs)
			}
			if acks := f.acks(t); len(acks) != 1 || acks[0] != `{"id":"`+q+`","by":"human","at":"`+at+`","note":"replied via Work Trains"}` {
				t.Errorf("acks = %q", acks)
			}
			flip := f.flip(t)
			if jsjson.Truthy(jsjson.Path(flip, "awaiting_human")) || jsjson.Path(flip, "pending_launch", "to") != "claude" {
				t.Errorf("flip.json = %v", flip)
			}
		})
	}
}

// A retried answer whose reply was finished writes nothing at all.
func TestARetriedAnswerThatWasFinishedWritesNothing(t *testing.T) {
	f := newFixture(t)
	q := f.question(t)
	writeFile(t, f.flipPath(), `{"awaiting_human":{"at":"x","door":"claude","reason":"r","question":"`+q+`"}}`)
	if _, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it")); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []string {
		var files []string
		for _, path := range []string{filepath.Join(f.box().Dir, "messages.jsonl"), filepath.Join(f.box().Dir, "acks.jsonl"), f.flipPath()} {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, string(data))
		}
		return files
	}
	before := snapshot()
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "already applied"})
	if after := snapshot(); !slices.Equal(after, before) {
		t.Errorf("files changed:\n%q\n%q", before, after)
	}
}

func TestAQuestionAnsweredOnTheMacIsSuperseded(t *testing.T) {
	f := newFixture(t)
	q := f.question(t)
	human := "human"
	if _, err := f.box().Ack(mailbox.AckRequest{ID: &q, By: &human, Note: "replied"}); err != nil {
		t.Fatal(err)
	}
	got, err := f.applier.ApplyAnswer(answer(q, "typed", "no"))
	expect(t, got, err, Outcome{Superseded, "answered on the Mac"})
	if len(f.messages(t)) != 1 {
		t.Errorf("messages = %q", f.messages(t))
	}
}

func TestAnAnswerToSomethingElseIsRejected(t *testing.T) {
	recommended := "x"
	for _, c := range []struct {
		name  string
		draft *mailbox.Draft
		want  Outcome
	}{
		{"a note", &mailbox.Draft{From: "conductor-claude", To: "human", Kind: "note", Subject: "fyi"}, Outcome{Rejected, "not a question"}},
		{"a question to an agent", &mailbox.Draft{From: "conductor-claude", To: "coder", Kind: "question", Subject: "q", Recommended: &recommended}, Outcome{Rejected, "not a question"}},
		{"nothing", nil, Outcome{Rejected, "question not found"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id := "missing"
			if c.draft != nil {
				id = f.post(t, *c.draft)
			}
			got, err := f.applier.ApplyAnswer(answer(id, "typed", "yes"))
			expect(t, got, err, c.want)
			if f.acks(t) != nil {
				t.Errorf("acks = %q", f.acks(t))
			}
		})
	}
}

func TestAnUnknownProjectIsRejected(t *testing.T) {
	f := newFixture(t)
	a := answer("id-1", "accept", "x")
	a.ProjectKey = "ffffffffffffffffffffffffffffffff"
	got, err := f.applier.ApplyAnswer(a)
	expect(t, got, err, Outcome{Rejected, "unknown project"})
	pause := action("pause")
	pause.ProjectKey = a.ProjectKey
	got, err = f.applier.ApplyAction(pause)
	expect(t, got, err, Outcome{Rejected, "unknown project"})
}

func TestAnswerToTheGatesQuestionClearsTheGateAndQueuesTheRelaunch(t *testing.T) {
	f := newFixture(t)
	q := f.question(t)
	writeFile(t, f.flipPath(), `{"awaiting_human":{"at":"x","door":"claude","reason":"r","question":"`+q+`"}}`)
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "replied; cleared the human gate, the claude conductor relaunches within a minute"})
	flip := f.flip(t)
	if jsjson.Truthy(jsjson.Path(flip, "awaiting_human")) || jsjson.Path(flip, "pending_launch", "to") != "claude" {
		t.Errorf("flip.json = %v", flip)
	}
}

func TestAGateThatCannotBeCheckedLeavesTheReplyAppliedAndIsReturned(t *testing.T) {
	f := newFixture(t)
	q := f.question(t)
	writeFile(t, f.flipPath(), `{`)
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	var applyErr *Error
	if got != (Outcome{Applied, "replied; could not check the human gate"}) || !errors.As(err, &applyErr) {
		t.Errorf("outcome = %+v, err = %v", got, err)
	}
}

func TestPauseGatesTheRunningConductorsDoor(t *testing.T) {
	f := newFixture(t)
	f.runConductor(t, "opencode")
	got, err := f.applier.ApplyAction(action("pause"))
	expect(t, got, err, Outcome{Applied, "pauses at the next brief boundary"})
	gate := jsjson.Path(f.flip(t), "awaiting_human")
	if jsjson.Path(gate, "door") != "opencode" || jsjson.Path(gate, "reason") != "paused from Work Trains" || jsjson.Path(gate, "question") != nil {
		t.Errorf("awaiting_human = %v", gate)
	}
}

func TestPauseWithoutAConductorGatesTheLastFlipsDoorElseTheHomeDoor(t *testing.T) {
	for _, c := range []struct {
		name, flip, want string
	}{
		{"last flip", `{"to":"opencode"}`, "opencode"},
		{"home door", "", "claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.flip != "" {
				writeFile(t, f.flipPath(), c.flip)
			}
			got, err := f.applier.ApplyAction(action("pause"))
			expect(t, got, err, Outcome{Applied, "pauses at the next brief boundary; no conductor running"})
			if door := jsjson.Path(f.flip(t), "awaiting_human", "door"); door != c.want {
				t.Errorf("door = %v, want %s", door, c.want)
			}
		})
	}
}

func TestPauseTwiceIsSuperseded(t *testing.T) {
	f := newFixture(t)
	if _, err := f.applier.ApplyAction(action("pause")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.flipPath())
	got, err := f.applier.ApplyAction(actionWithID("action-2", "pause"))
	expect(t, got, err, Outcome{Superseded, "already paused"})
	if after, _ := os.ReadFile(f.flipPath()); string(after) != string(before) {
		t.Errorf("flip.json changed:\n%s\n%s", before, after)
	}
}

func TestResumeClearsTheGateAndQueuesTheRelaunch(t *testing.T) {
	f := newFixture(t)
	f.holdSupervisorLock(t)
	writeFile(t, f.flipPath(), `{"awaiting_human":{"at":"x","door":"claude","reason":"paused from Work Trains","question":null}}`)
	got, err := f.applier.ApplyAction(action("resume"))
	expect(t, got, err, Outcome{Applied, "resuming: claude conductor relaunches within a minute"})
	flip := f.flip(t)
	if jsjson.Truthy(jsjson.Path(flip, "awaiting_human")) || jsjson.Path(flip, "pending_launch", "to") != "claude" {
		t.Errorf("flip.json = %v", flip)
	}
	if len(f.supervisorStarts) != 0 || len(f.sleeps) != 0 {
		t.Errorf("started or waited for a supervisor beside a running one: starts %v, sleeps %v", f.supervisorStarts, f.sleeps)
	}
}

func TestResumeStartsTheSupervisorWhenNoneRuns(t *testing.T) {
	f := newFixture(t)
	writeFile(t, supervisor.NewProject(f.root).LockPath, "999999")
	writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
	got, err := f.applier.ApplyAction(action("resume"))
	expect(t, got, err, Outcome{Applied, "resuming: opencode conductor relaunches within a minute"})
	if len(f.supervisorStarts) != 1 || f.supervisorStarts[0] != f.root {
		t.Errorf("supervisor starts = %v", f.supervisorStarts)
	}
}

// A started supervisor that takes a while to claim its lock is waited for, poll by poll.
func TestResumeWaitsForTheStartedSupervisorToHoldTheLock(t *testing.T) {
	f := newFixture(t)
	f.startLeavesNoLock = true
	f.onSleep = func() {
		if len(f.sleeps) == 3 {
			f.holdSupervisorLock(t)
		}
	}
	writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
	got, err := f.applier.ApplyAction(action("resume"))
	expect(t, got, err, Outcome{Applied, "resuming: opencode conductor relaunches within a minute"})
	if want := slices.Repeat([]time.Duration{supervisorReadyInterval}, 3); !slices.Equal(f.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", f.sleeps, want)
	}
}

// `supervisor start` can exit 0 without a supervisor ever holding the lock (its serve could not
// be launched, or died first). The gate stays for the retry after about 5 s of polling.
func TestAStartedSupervisorThatNeverHoldsTheLockLeavesTheGate(t *testing.T) {
	f := newFixture(t)
	f.startLeavesNoLock = true
	writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
	got, err := f.applier.ApplyAction(action("resume"))
	if !errors.Is(err, ErrSupervisorNotLive) {
		t.Fatalf("err = %v", err)
	}
	if want := (Outcome{Rejected, "could not start the supervisor"}); got != want {
		t.Errorf("outcome = %+v, want %+v", got, want)
	}
	var waited time.Duration
	for _, d := range f.sleeps {
		waited += d
	}
	if waited != 5*time.Second || len(f.supervisorStarts) != 1 {
		t.Errorf("waited %v over %d starts", waited, len(f.supervisorStarts))
	}
	if flip := f.flip(t); !jsjson.Truthy(jsjson.Path(flip, "awaiting_human")) || jsjson.Path(flip, "pending_launch") != nil {
		t.Errorf("flip.json = %v", flip)
	}
}

// A supervisor that will not start leaves the gate set, so the owner's next resume is still a
// resume (not "not paused") and starts it; the page gets the step, the log the full error.
func TestASupervisorThatWillNotStartLeavesTheGateForTheRetry(t *testing.T) {
	f := newFixture(t)
	f.supervisorFailure = &SupervisorStartError{Output: "cannot lock " + f.root}
	writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
	got, err := f.applier.ApplyAction(action("resume"))
	var applyErr *Error
	if !errors.As(err, &applyErr) || applyErr.Step != "start the supervisor" || !strings.Contains(err.Error(), f.root) {
		t.Fatalf("err = %v", err)
	}
	if want := (Outcome{Rejected, "could not start the supervisor"}); got != want {
		t.Errorf("outcome = %+v, want %+v", got, want)
	}
	if !jsjson.Truthy(jsjson.Path(f.flip(t), "awaiting_human")) {
		t.Fatal("the gate was cleared although no supervisor runs to relaunch the conductor")
	}
	f.supervisorFailure = nil
	got, err = f.applier.ApplyAction(actionWithID("action-2", "resume"))
	expect(t, got, err, Outcome{Applied, "resuming: opencode conductor relaunches within a minute"})
	if len(f.supervisorStarts) != 2 || jsjson.Path(f.flip(t), "pending_launch", "to") != "opencode" {
		t.Errorf("supervisor starts = %v, flip.json = %v", f.supervisorStarts, f.flip(t))
	}
}

func TestResumeWithoutAGateIsSuperseded(t *testing.T) {
	f := newFixture(t)
	got, err := f.applier.ApplyAction(action("resume"))
	expect(t, got, err, Outcome{Superseded, "not paused"})
	if _, statErr := os.Stat(f.flipPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("flip.json written: %v", statErr)
	}
}

func TestAnUnknownActionIsRejected(t *testing.T) {
	got, err := newFixture(t).applier.ApplyAction(action("hide"))
	expect(t, got, err, Outcome{Rejected, "unknown action"})
}

// pauseLocally and resumeLocally are the human at the terminal setting and clearing the gate.
func (f *fixture) pauseLocally(t *testing.T) {
	f.gateLocally(t, cliargs.Opts{"active": {Value: "claude"}, "reason": {Value: "local review"}})
}

func (f *fixture) resumeLocally(t *testing.T) {
	f.gateLocally(t, cliargs.Opts{"clear": {Flag: true}})
}

func (f *fixture) gateLocally(t *testing.T, opts cliargs.Opts) {
	t.Helper()
	if _, err := flipcmd.NewProject(f.root).Gate(opts); err != nil {
		t.Fatal(err)
	}
}

// flipBytes is flip.json's content, "" when it does not exist.
func (f *fixture) flipBytes(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.flipPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// recordInFlight leaves the journal as a process interrupted mid-action does: the action
// recorded, its outcome not yet.
func (f *fixture) recordInFlight(t *testing.T, kind string) {
	t.Helper()
	if err := f.journal().record(journalEntry{journalKey: journalKey{Kind: kind, ID: "action-1"}}); err != nil {
		t.Fatal(err)
	}
}

// replayAfterLocalChange applies an action, makes a local change the replay must not undo,
// and replays the action (its ack lost) in the same process and in a restarted one: each
// resends the first outcome and leaves flip.json as the local change left it.
func replayAfterLocalChange(t *testing.T, f *fixture, kind string, want Outcome, change func(*testing.T)) {
	t.Helper()
	got, err := f.applier.ApplyAction(action(kind))
	expect(t, got, err, want)
	change(t)
	changed := f.flipBytes(t)
	for _, run := range []struct {
		name    string
		applier Applier
	}{
		{"same process", f.applier},
		{"restarted", f.restarted(t).applier},
	} {
		got, err := run.applier.ApplyAction(action(kind))
		expect(t, got, err, want)
		if f.flipBytes(t) != changed {
			t.Errorf("%s: the replay changed flip.json:\n%s\n%s", run.name, changed, f.flipBytes(t))
		}
	}
}

func TestAReplayedPauseKeepsALocalResume(t *testing.T) {
	f := newFixture(t)
	f.runConductor(t, "opencode")
	replayAfterLocalChange(t, f, "pause", Outcome{Applied, "pauses at the next brief boundary"}, f.resumeLocally)
	if jsjson.Truthy(jsjson.Path(f.flip(t), "awaiting_human")) {
		t.Errorf("the replayed pause gated the project again: %v", f.flip(t))
	}
}

func TestAReplayedResumeKeepsALocalPause(t *testing.T) {
	f := newFixture(t)
	f.holdSupervisorLock(t)
	writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
	replayAfterLocalChange(t, f, "resume", Outcome{Applied, "resuming: opencode conductor relaunches within a minute"}, f.pauseLocally)
	if reason := jsjson.Path(f.flip(t), "awaiting_human", "reason"); reason != "local review" {
		t.Errorf("the replayed resume cleared the local gate: %v", f.flip(t))
	}
}

// A replayed rejection is resent, not retried: the page already shows it, and the owner's next
// resume is a new action.
func TestAReplayedRejectionIsResent(t *testing.T) {
	f := newFixture(t)
	f.supervisorFailure = &SupervisorStartError{Output: "no"}
	writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
	if _, err := f.applier.ApplyAction(action("resume")); err == nil {
		t.Fatal("the supervisor start did not fail")
	}
	f.supervisorFailure = nil
	got, err := f.applier.ApplyAction(action("resume"))
	expect(t, got, err, Outcome{Rejected, "could not start the supervisor"})
	if len(f.supervisorStarts) != 1 || !jsjson.Truthy(jsjson.Path(f.flip(t), "awaiting_human")) {
		t.Errorf("the replay was applied: starts %v, flip.json %v", f.supervisorStarts, f.flip(t))
	}
}

// An action interrupted mid-apply is never applied again, whether or not it wrote flip.json
// before the interruption (flip.json cannot tell: a local resume can restore the very bytes a
// pause replaced). The retry writes nothing; it is applied when the project is as the action
// asked, else superseded for the owner to send again, and that outcome is recorded.
func TestAnInterruptedActionIsNeverAppliedAgain(t *testing.T) {
	interrupted := Outcome{Superseded, "interrupted; send it again"}
	for _, c := range []struct {
		name, kind string
		gated      bool
		wrote      bool
		meanwhile  func(*fixture, *testing.T)
		want       Outcome
	}{
		{"pause before its write", "pause", false, false, nil, interrupted},
		{"pause after its write", "pause", false, true, nil, Outcome{Applied, "already applied"}},
		{"pause after its write, then a local resume", "pause", false, true, (*fixture).resumeLocally, interrupted},
		{"resume before its write", "resume", true, false, nil, interrupted},
		{"resume after its write", "resume", true, true, nil, Outcome{Applied, "already applied"}},
		{"resume after its write, then a local pause", "resume", true, true, (*fixture).pauseLocally, interrupted},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.holdSupervisorLock(t)
			if c.gated {
				writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
			}
			f.recordInFlight(t, c.kind)
			if c.wrote {
				if _, err := f.applier.applyKind(f.root, c.kind); err != nil {
					t.Fatal(err)
				}
			}
			if c.meanwhile != nil {
				c.meanwhile(f, t)
			}
			expectRetryWritesNothing(t, f, c.kind, c.want)
		})
	}
}

// The reviewer's case: a pause that wrote its gate, then a local resume that restored
// flip.json byte for byte (the queued launch it kept), must not pause again on the retry.
func TestAnInterruptedPauseUndoneLocallyToTheSameBytesIsNotAppliedAgain(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.flipPath(), `{"pending_launch":{"to":"claude"}}`)
	f.pauseLocally(t)
	f.resumeLocally(t)
	original := f.flipBytes(t)
	f.recordInFlight(t, "pause")
	if _, err := f.applier.applyKind(f.root, "pause"); err != nil {
		t.Fatal(err)
	}
	f.resumeLocally(t)
	if f.flipBytes(t) != original {
		t.Fatalf("the local resume did not restore flip.json's bytes:\n%s\n%s", original, f.flipBytes(t))
	}
	expectRetryWritesNothing(t, f, "pause", Outcome{Superseded, "interrupted; send it again"})
}

// expectRetryWritesNothing retries action-1 of kind in a restarted process and again after:
// both give want, flip.json is left as it was, and the journal holds want.
func expectRetryWritesNothing(t *testing.T, f *fixture, kind string, want Outcome) {
	t.Helper()
	before := f.flipBytes(t)
	restarted := f.restarted(t)
	for range 2 {
		got, err := restarted.applier.ApplyAction(action(kind))
		expect(t, got, err, want)
	}
	if f.flipBytes(t) != before {
		t.Errorf("the retry wrote flip.json:\n%s\n%s", before, f.flipBytes(t))
	}
	if entry, _, _ := f.journal().lookup(journalKey{Kind: kind, ID: "action-1"}); entry.outcome() != want {
		t.Errorf("journal = %+v, want the outcome %+v recorded", entry, want)
	}
}

// A rejection for an unknown project is recorded in the unresolved journal and resent on a
// replay, even once discovery finds the project and the Mac has decided something since.
func TestAnUnknownProjectsRejectionIsResentAfterDiscovery(t *testing.T) {
	unknown := Outcome{Rejected, "unknown project"}
	t.Run("pause", func(t *testing.T) {
		f := newFixture(t)
		f.undiscovered = true
		got, err := f.applier.ApplyAction(action("pause"))
		expect(t, got, err, unknown)
		f.pauseLocally(t)
		f.resumeLocally(t)
		resumed := f.flipBytes(t)
		got, err = f.restarted(t).applier.ApplyAction(action("pause"))
		expect(t, got, err, unknown)
		if f.flipBytes(t) != resumed {
			t.Errorf("the replayed pause was applied: %s", f.flipBytes(t))
		}
	})
	t.Run("answer", func(t *testing.T) {
		f := newFixture(t)
		q := f.question(t)
		f.undiscovered = true
		got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
		expect(t, got, err, unknown)
		got, err = f.restarted(t).applier.ApplyAnswer(answer(q, "accept", "ship it"))
		expect(t, got, err, unknown)
		if len(f.messages(t)) != 1 || f.acks(t) != nil {
			t.Errorf("the replayed answer was applied: messages %q, acks %q", f.messages(t), f.acks(t))
		}
	})
}

// The unresolved journal keys an entry by project, kind and id: an answer and an action may
// share an id, and an action of a known project is not taken for another project's rejection.
func TestTheUnresolvedJournalKeysByProjectKindAndID(t *testing.T) {
	f := newFixture(t)
	f.undiscovered = true
	q := f.question(t)
	a := answer(q, "accept", "ship it")
	a.ID = "action-1"
	if got, err := f.applier.ApplyAnswer(a); got.Detail != "unknown project" || err != nil {
		t.Fatalf("outcome = %+v, err = %v", got, err)
	}
	other := action("pause")
	other.ProjectKey = "ffffffffffffffffffffffffffffffff"
	if got, err := f.applier.ApplyAction(other); got.Detail != "unknown project" || err != nil {
		t.Fatalf("outcome = %+v, err = %v", got, err)
	}
	f.undiscovered = false
	got, err := f.applier.ApplyAction(action("pause"))
	expect(t, got, err, Outcome{Applied, "pauses at the next brief boundary; no conductor running"})
}

func TestTheJournalKeepsTheNewestActions(t *testing.T) {
	journal := newActionJournal(t.TempDir())
	for i := range journalLimit + 1 {
		entry := journalEntry{journalKey{Kind: "pause", ID: fmt.Sprintf("action-%d", i)}, Applied, "paused"}
		if err := journal.record(entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := journal.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != journalLimit || entries[0].ID != "action-1" || entries[journalLimit-1].ID != fmt.Sprintf("action-%d", journalLimit) {
		t.Errorf("kept %d entries, %s to %s", len(entries), entries[0].ID, entries[len(entries)-1].ID)
	}
}

// A journal that is not an array of valid, distinct entries is refused before anything is
// applied, with no outcome to ack: dropping or misreading it could act twice or resend a wrong
// outcome.
func TestAnInvalidJournalIsRefused(t *testing.T) {
	const settled = `"result":"applied","detail":"paused"`
	for _, c := range []struct {
		name, project, unresolved string
	}{
		{"null", "null", ""},
		{"an object", "{}", ""},
		{"trailing data", "[] []", ""},
		{"an unknown field", `[{"id":"a","kind":"pause","flip_sha256":""}]`, ""},
		{"no id", `[{"id":"","kind":"pause",` + settled + `}]`, ""},
		{"a duplicate id", `[{"id":"a","kind":"pause",` + settled + `},{"id":"a","kind":"pause"}]`, ""},
		{"an unsupported kind", `[{"id":"a","kind":"hide",` + settled + `}]`, ""},
		{"an answer in a project's journal", `[{"id":"a","kind":"answer",` + settled + `}]`, ""},
		{"a project key in a project's journal", `[{"project_key":"k","id":"a","kind":"pause",` + settled + `}]`, ""},
		{"an unsupported result", `[{"id":"a","kind":"pause","result":"done","detail":"x"}]`, ""},
		{"a result without a detail", `[{"id":"a","kind":"pause","result":"applied"}]`, ""},
		{"a detail without a result", `[{"id":"a","kind":"pause","detail":"x"}]`, ""},
		{"unresolved: null", "", "null"},
		{"unresolved: no project key", "", `[{"id":"a","kind":"answer","result":"rejected","detail":"unknown project"}]`},
		{"unresolved: not a rejection", "", `[{"project_key":"k","id":"a","kind":"pause",` + settled + `}]`},
		{"unresolved: in flight", "", `[{"project_key":"k","id":"a","kind":"pause"}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			step := "read the action journal"
			if c.project != "" {
				writeFile(t, f.journal().path, c.project)
			} else {
				step = "read the unresolved journal"
				writeFile(t, newUnresolvedJournal(f.stateDir).path, c.unresolved)
			}
			got, err := f.applier.ApplyAction(action("pause"))
			expectRetryable(t, got, err, step)
			var corrupt *CorruptJournalError
			if !errors.As(err, &corrupt) {
				t.Errorf("err = %v, want a corrupt journal", err)
			}
			if _, statErr := os.Stat(f.flipPath()); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("paused despite the invalid journal: %v", statErr)
			}
		})
	}
}

// Each failed step sends the page a fixed detail, never an error's text (which names local
// paths), and returns the full error, with its step, for the log.
func TestAFailedStepSendsAFixedDetailAndReturnsTheError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		step, detail string
		apply        func(t *testing.T, f *fixture) (Outcome, error)
	}{
		{"read the registry", "pause: could not read the registry", func(t *testing.T, f *fixture) (Outcome, error) {
			writeFile(t, f.applier.RegistryFile, "{")
			return f.applier.ApplyAction(action("pause"))
		}},
		{"pause", "pause: could not write the gate", func(t *testing.T, f *fixture) (Outcome, error) {
			f.makeJournalDir(t)
			readOnly(t, filepath.Dir(f.flipPath()))
			return f.applier.ApplyAction(action("pause"))
		}},
		{"resume", "resume: could not clear the gate", func(t *testing.T, f *fixture) (Outcome, error) {
			f.holdSupervisorLock(t)
			writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
			f.makeJournalDir(t)
			readOnly(t, filepath.Dir(f.flipPath()))
			return f.applier.ApplyAction(action("resume"))
		}},
		{"start the supervisor", "could not start the supervisor", func(t *testing.T, f *fixture) (Outcome, error) {
			f.supervisorFailure = &SupervisorStartError{Output: "cannot lock " + f.root + " under " + home}
			writeFile(t, f.flipPath(), `{"awaiting_human":{"door":"opencode"}}`)
			return f.applier.ApplyAction(action("resume"))
		}},
	} {
		t.Run(c.step, func(t *testing.T) {
			f := newFixture(t)
			got, err := c.apply(t, f)
			if want := (Outcome{Rejected, c.detail}); got != want {
				t.Errorf("outcome = %+v, want %+v", got, want)
			}
			if strings.Contains(got.Detail, f.root) || (home != "" && strings.Contains(got.Detail, home)) {
				t.Errorf("detail %q names a local path", got.Detail)
			}
			var applyErr *Error
			if !errors.As(err, &applyErr) || applyErr.Step != c.step || applyErr.Err == nil {
				t.Errorf("err = %v, want the %q step's error", err, c.step)
			}
		})
	}
}

// expectRetryable is a *RetryableError at step with no outcome to ack.
func expectRetryable(t *testing.T, got Outcome, err error, step string) {
	t.Helper()
	if got != (Outcome{}) {
		t.Errorf("outcome = %+v, want none", got)
	}
	var retry *RetryableError
	if !errors.As(err, &retry) || retry.Step != step || retry.Err == nil {
		t.Errorf("err = %v, want a retryable failure at the %q step", err, step)
	}
	var final *Error
	if errors.As(err, &final) {
		t.Errorf("err = %v also carries a final step's error", err)
	}
}

// A journal or mailbox that cannot be read, or a receipt that cannot be written, gives no
// outcome to ack, and nothing is applied.
func TestAMissingReceiptIsRetryableWithNoOutcome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	for _, c := range []struct {
		step  string
		apply func(t *testing.T, f *fixture) (Outcome, error)
	}{
		{"read the mailbox", func(t *testing.T, f *fixture) (Outcome, error) {
			if err := os.MkdirAll(filepath.Join(f.box().Dir, "messages.jsonl"), 0o700); err != nil {
				t.Fatal(err)
			}
			return f.applier.ApplyAnswer(answer("q", "accept", "ship it"))
		}},
		{"finish the reply", func(t *testing.T, f *fixture) (Outcome, error) {
			id := f.question(t)
			f.post(t, mailbox.Draft{From: "human", To: "conductor-claude", Kind: "note", Subject: "Re: Ship?", Thread: id, Remote: &mailbox.RemoteRef{AnswerID: "answer-1", Kind: "accept"}})
			readOnly(t, filepath.Join(f.box().Dir, "acks.jsonl"))
			return f.applier.ApplyAnswer(answer(id, "accept", "ship it"))
		}},
		{"reply", func(t *testing.T, f *fixture) (Outcome, error) {
			id := f.question(t)
			readOnly(t, filepath.Join(f.box().Dir, "messages.jsonl"))
			return f.applier.ApplyAnswer(answer(id, "accept", "ship it"))
		}},
		{"read the unresolved journal", func(t *testing.T, f *fixture) (Outcome, error) {
			writeFile(t, newUnresolvedJournal(f.stateDir).path, "[")
			return f.applier.ApplyAction(action("pause"))
		}},
		{"record the rejection", func(t *testing.T, f *fixture) (Outcome, error) {
			f.undiscovered = true
			f.lockStateDir(t)
			return f.applier.ApplyAction(action("pause"))
		}},
		{"read the action journal", func(t *testing.T, f *fixture) (Outcome, error) {
			writeFile(t, f.journal().path, "[")
			return f.applier.ApplyAction(action("pause"))
		}},
		{"record the action", func(t *testing.T, f *fixture) (Outcome, error) {
			f.makeJournalDir(t)
			readOnly(t, filepath.Dir(f.journal().path))
			return f.applier.ApplyAction(action("pause"))
		}},
	} {
		t.Run(c.step, func(t *testing.T) {
			f := newFixture(t)
			got, err := c.apply(t, f)
			expectRetryable(t, got, err, c.step)
			if _, statErr := os.Stat(f.flipPath()); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("paused without a durable receipt: %v", statErr)
			}
		})
	}
}

// An unknown project's rejection that could not be recorded is not final: once storage
// recovers, a restarted replay records it if the project is still unknown, and that rejection
// is resent after discovery and a local decision.
func TestAnUnrecordedRejectionIsRecordedOnceStorageRecovers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	f := newFixture(t)
	f.undiscovered = true
	f.lockStateDir(t)
	got, err := f.applier.ApplyAction(action("pause"))
	expectRetryable(t, got, err, "record the rejection")
	writable(t, f.stateDir)
	restarted := f.restarted(t)
	restarted.undiscovered = true
	got, err = restarted.applier.ApplyAction(action("pause"))
	expect(t, got, err, Outcome{Rejected, "unknown project"})
	f.pauseLocally(t)
	f.resumeLocally(t)
	resumed := f.flipBytes(t)
	got, err = f.restarted(t).applier.ApplyAction(action("pause"))
	expect(t, got, err, Outcome{Rejected, "unknown project"})
	if f.flipBytes(t) != resumed {
		t.Errorf("the replayed pause was applied after its rejection: %s", f.flipBytes(t))
	}
}

// A retryable failure was never acked, so the Worker's resend is still the owner's pending
// request: once storage and discovery recover it is applied exactly once, and a later replay
// (its ack lost) resends that outcome without undoing a local decision made since.
func TestARetriedActionAppliesOnceAfterRecoveryAndKeepsANewerLocalDecision(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	paused := Outcome{Applied, "pauses at the next brief boundary; no conductor running"}
	for _, c := range []struct {
		step         string
		breakStorage func(t *testing.T, f *fixture) (restore func())
	}{
		{"record the rejection", func(t *testing.T, f *fixture) func() {
			f.undiscovered = true
			f.lockStateDir(t)
			return func() { writable(t, f.stateDir) }
		}},
		{"record the action", func(t *testing.T, f *fixture) func() {
			f.makeJournalDir(t)
			dir := filepath.Dir(f.journal().path)
			readOnly(t, dir)
			return func() { writable(t, dir) }
		}},
	} {
		t.Run(c.step, func(t *testing.T) {
			f := newFixture(t)
			restore := c.breakStorage(t, f)
			got, err := f.applier.ApplyAction(action("pause"))
			expectRetryable(t, got, err, c.step)
			restore()
			got, err = f.restarted(t).applier.ApplyAction(action("pause"))
			expect(t, got, err, paused)
			f.resumeLocally(t)
			resumed := f.flipBytes(t)
			got, err = f.restarted(t).applier.ApplyAction(action("pause"))
			expect(t, got, err, paused)
			if f.flipBytes(t) != resumed {
				t.Errorf("the replay undid the local resume: %s", f.flipBytes(t))
			}
		})
	}
}

// An answer whose rejection could not be recorded is posted once when the project is found.
func TestAnAnswerWhoseRejectionWasNotRecordedIsPostedOnceAfterDiscovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	f := newFixture(t)
	q := f.question(t)
	f.undiscovered = true
	f.lockStateDir(t)
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expectRetryable(t, got, err, "record the rejection")
	writable(t, f.stateDir)
	for _, want := range []string{"replied", "already applied"} {
		got, err := f.restarted(t).applier.ApplyAnswer(answer(q, "accept", "ship it"))
		expect(t, got, err, Outcome{Applied, want})
	}
	if len(f.messages(t)) != 2 || len(f.acks(t)) != 1 {
		t.Errorf("messages %q, acks %q, want the question and one reply acked", f.messages(t), f.acks(t))
	}
}

// gatedQuestion is a question to the human that the human gate awaits.
func (f *fixture) gatedQuestion(t *testing.T) string {
	t.Helper()
	q := f.question(t)
	writeFile(t, f.flipPath(), `{"awaiting_human":{"at":"x","door":"claude","reason":"r","question":"`+q+`"}}`)
	return q
}

// expectGateClearedOnce checks the mailbox holds the question and one reply, acked once, and
// the gate the question held is cleared with the conductor's relaunch queued.
func (f *fixture) expectGateClearedOnce(t *testing.T) {
	t.Helper()
	if len(f.messages(t)) != 2 || len(f.acks(t)) != 1 {
		t.Errorf("messages %q, acks %q, want the question and one reply acked once", f.messages(t), f.acks(t))
	}
	flip := f.flip(t)
	if jsjson.Truthy(jsjson.Path(flip, "awaiting_human")) || jsjson.Path(flip, "pending_launch", "to") != "claude" {
		t.Errorf("flip.json = %v, want the gate cleared and the relaunch queued", flip)
	}
}

// An answer whose reply could not be posted is not final: once the mailbox is writable again,
// the resend posts it once, acks it and clears the gate, and a later replay posts nothing.
func TestAnAnswerThatCouldNotBePostedIsPostedOnceOnRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	f := newFixture(t)
	q := f.gatedQuestion(t)
	messages := filepath.Join(f.box().Dir, "messages.jsonl")
	readOnly(t, messages)
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expectRetryable(t, got, err, "reply")
	if len(f.messages(t)) != 1 || !jsjson.Truthy(jsjson.Path(f.flip(t), "awaiting_human")) {
		t.Fatalf("the failed reply wrote something: messages %q, flip.json %v", f.messages(t), f.flip(t))
	}
	writable(t, messages)
	got, err = f.restarted(t).applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "replied; cleared the human gate, the claude conductor relaunches within a minute"})
	got, err = f.restarted(t).applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "already applied"})
	f.expectGateClearedOnce(t)
}

// A reply posted whose ack could not be written is not final either: the reply, carrying the
// answer's id, is the receipt by which the resend acks it and clears the gate without posting
// it again.
func TestAnAnswerPostedButNotAckedIsFinishedOnRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	f := newFixture(t)
	q := f.gatedQuestion(t)
	acks := filepath.Join(f.box().Dir, "acks.jsonl")
	readOnly(t, acks)
	got, err := f.applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expectRetryable(t, got, err, "reply")
	if len(f.messages(t)) != 2 || strings.Join(f.acks(t), "") != "" || !jsjson.Truthy(jsjson.Path(f.flip(t), "awaiting_human")) {
		t.Fatalf("want the reply posted and not acked: messages %q, acks %q, flip.json %v", f.messages(t), f.acks(t), f.flip(t))
	}
	writable(t, acks)
	got, err = f.restarted(t).applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "already applied; cleared the human gate, the claude conductor relaunches within a minute"})
	got, err = f.restarted(t).applier.ApplyAnswer(answer(q, "accept", "ship it"))
	expect(t, got, err, Outcome{Applied, "already applied"})
	f.expectGateClearedOnce(t)
}

// Once an action is recorded in flight its outcome is final even if recording the outcome
// fails: the error is for the log only, and a replay (its ack lost) finds the action in flight
// and is never applied again, so it cannot undo a local decision made since.
func TestAnOutcomeThatCouldNotBeRecordedIsFinalAndNotAppliedAgain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only modes these failures rely on")
	}
	f := newFixture(t)
	f.runConductor(t, "opencode")
	dir := filepath.Dir(f.journal().path)
	broken := false
	f.applier.Alive = func(pid any) bool {
		// The conductor probe runs after the in-flight record and before the gate is written.
		if !broken {
			broken = true
			readOnly(t, dir)
		}
		return pid == float64(livePid)
	}
	got, err := f.applier.ApplyAction(action("pause"))
	if want := (Outcome{Applied, "pauses at the next brief boundary"}); got != want {
		t.Errorf("outcome = %+v, want %+v", got, want)
	}
	var final *Error
	var retry *RetryableError
	if !errors.As(err, &final) || final.Step != "record the outcome" || errors.As(err, &retry) {
		t.Errorf("err = %v, want a final outcome whose record failed", err)
	}
	writable(t, dir)
	f.resumeLocally(t)
	expectRetryWritesNothing(t, f, "pause", Outcome{Superseded, "interrupted; send it again"})
}

// lockStateDir makes the remote state directory unwritable. It stays private (0500), since
// MkdirPrivate tightens a directory open to group or other back to 0700.
func (f *fixture) lockStateDir(t *testing.T) {
	t.Helper()
	if err := os.Chmod(f.stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.stateDir, 0o700) })
}

// writable gives path's owner write permission back, as storage recovering does.
func writable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o200); err != nil {
		t.Fatal(err)
	}
}

// readOnly makes path read-only (creating it as an empty file when absent) until the
// test ends, so the next write to or under it fails.
func readOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		writeFile(t, path, "")
		info, err = os.Stat(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, info.Mode().Perm()&^0o222); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, info.Mode().Perm()) })
}

func TestStartSupervisorRunsSupervisorStartAndReportsAFailure(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	ok := filepath.Join(dir, "ok")
	writeFile(t, ok, "#!/bin/sh\necho \"$@\" > "+argsFile+"\n")
	failing := filepath.Join(dir, "failing")
	writeFile(t, failing, "#!/bin/sh\necho 'lock busy' >&2\nexit 3\n")
	for _, script := range []string{ok, failing} {
		if err := os.Chmod(script, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := startSupervisor(ok, "/p"); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, argsFile); len(got) != 1 || got[0] != "supervisor start --cwd /p" {
		t.Errorf("args = %q", got)
	}
	var startErr *SupervisorStartError
	if err := startSupervisor(failing, "/p"); !errors.As(err, &startErr) || startErr.Output != "lock busy\n" {
		t.Errorf("err = %v", err)
	}
}
