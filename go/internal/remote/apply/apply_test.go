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
// supervisor launcher records its calls.
type fixture struct {
	root              string
	applier           Applier
	supervisorStarts  []string
	supervisorFailure error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir()}
	registryFile := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, registryFile, `{"conductor":{"home":"claude"}}`)
	n := 0
	f.applier = Applier{
		Projects: func(key string) (string, bool) {
			return f.root, key == projectKey
		},
		Home:         "/Users/owner",
		RegistryFile: registryFile,
		Alive:        func(pid any) bool { return pid == float64(livePid) },
		StartSupervisor: func(root string) error {
			f.supervisorStarts = append(f.supervisorStarts, root)
			return f.supervisorFailure
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

func (f *fixture) runConductor(t *testing.T, harness string) {
	writeFile(t, worker.NewProject(f.root).StatePath("conductor"), fmt.Sprintf(`{"lane":"conductor","harness":%q,"status":"running","pid":%d}`, harness, livePid))
}

func answer(questionID, kind, text string) wire.PulledAnswer {
	return wire.PulledAnswer{ID: "answer-1", ProjectKey: projectKey, QuestionID: questionID, Kind: kind, Text: text, CreatedAt: at}
}

func action(kind string) wire.PulledAction {
	return wire.PulledAction{ID: "action-1", ProjectKey: projectKey, Kind: kind, CreatedAt: at}
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

func TestAnAnswerIsPostedAsTheHumansRemoteReplyAndAcked(t *testing.T) {
	for _, kind := range []string{"accept", "typed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			q := f.question(t)
			got, err := f.applier.ApplyAnswer(answer(q, kind, "ship it today"))
			expect(t, got, err, Outcome{Applied, "replied"})
			want := `{"id":"id-2","at":"` + at + `","from":"human","to":"conductor-claude","kind":"note","subject":"Re: Ship?","body":"ship it today","refs":[],"thread":"id-1","via":"remote","remote":{"answer_id":"answer-1","kind":"` + kind + `"}}`
			if msgs := f.messages(t); len(msgs) != 2 || msgs[1] != want {
				t.Errorf("messages = %q\nwant reply %s", msgs, want)
			}
			if acks := f.acks(t); len(acks) != 1 || acks[0] != `{"id":"id-1","by":"human","at":"`+at+`","note":"replied via Work Trains"}` {
				t.Errorf("acks = %q", acks)
			}
		})
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
	got, err := f.applier.ApplyAction(action("pause"))
	expect(t, got, err, Outcome{Superseded, "already paused"})
	if after, _ := os.ReadFile(f.flipPath()); string(after) != string(before) {
		t.Errorf("flip.json changed:\n%s\n%s", before, after)
	}
}

func TestResumeClearsTheGateAndQueuesTheRelaunch(t *testing.T) {
	f := newFixture(t)
	writeFile(t, supervisor.NewProject(f.root).LockPath, fmt.Sprintf("%d\n", livePid))
	writeFile(t, f.flipPath(), `{"awaiting_human":{"at":"x","door":"claude","reason":"paused from Work Trains","question":null}}`)
	got, err := f.applier.ApplyAction(action("resume"))
	expect(t, got, err, Outcome{Applied, "resuming: claude conductor relaunches within a minute"})
	flip := f.flip(t)
	if jsjson.Truthy(jsjson.Path(flip, "awaiting_human")) || jsjson.Path(flip, "pending_launch", "to") != "claude" {
		t.Errorf("flip.json = %v", flip)
	}
	if len(f.supervisorStarts) != 0 {
		t.Errorf("started a supervisor beside a running one: %v", f.supervisorStarts)
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

// A supervisor that will not start leaves the gate set, so the retried resume is still a
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
	got, err = f.applier.ApplyAction(action("resume"))
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

func TestARejectedDetailFitsTheAckCap(t *testing.T) {
	f := newFixture(t)
	got, _ := f.applier.rejected(f.root, errors.New(strings.Repeat("x", 199)+"😀"))
	if want := strings.Repeat("x", 199); got.Detail != want { // the pair would make 201 units
		t.Errorf("detail = %q (%d bytes)", got.Detail, len(got.Detail))
	}
}

// The detail sent to the page names neither the project root, wherever it is, nor the home.
func TestARejectedDetailRedactsTheProjectRootAndTheHome(t *testing.T) {
	f := newFixture(t)
	f.root = "/Volumes/work/app"
	got, err := f.applier.rejected(f.root, errors.New("open /Volumes/work/app/.orchestrator/flip.json; read /Users/owner/.config/x"))
	if want := "open <project>/.orchestrator/flip.json; read ~/.config/x"; got.Detail != want {
		t.Errorf("detail = %q, want %q", got.Detail, want)
	}
	if !strings.Contains(err.Error(), "/Volumes/work/app") {
		t.Errorf("the returned error lost the path: %v", err)
	}
}

// A real failed step: flip.json cannot be written because .orchestrator is a file. The root is a
// temp dir outside the fixture's home, and the page does not see it.
func TestAFailedPauseDoesNotSendTheProjectRootToThePage(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.root, ".orchestrator"), "not a directory")
	got, err := f.applier.ApplyAction(action("pause"))
	if err == nil || got.Result != Rejected {
		t.Fatalf("outcome = %+v, err = %v", got, err)
	}
	if strings.Contains(got.Detail, f.root) || !strings.Contains(got.Detail, projectPlaceholder) {
		t.Errorf("detail = %q", got.Detail)
	}
}

func TestCutUTF16NeverSplitsASurrogatePair(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"ab😀", 3, "ab"},
		{"ab😀", 4, "ab😀"},
		{"é😀", 2, "é"},
	} {
		if got := cutUTF16(c.in, c.n); got != c.want {
			t.Errorf("cutUTF16(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
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
