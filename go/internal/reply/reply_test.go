package reply

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
)

var fixedNow = time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)

const at = "2026-10-21T09:30:00.000Z"

// fixture is a temp project with a fixed clock and sequential message ids.
type fixture struct {
	box     *mailbox.Mailbox
	project flipcmd.Project
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	box := mailbox.New(root)
	n := 0
	box.Now = func() time.Time { return fixedNow }
	box.NewID = func() string { n++; return fmt.Sprintf("id-%d", n) }
	project := flipcmd.NewProject(root)
	project.Now = box.Now
	return fixture{box: box, project: project}
}

func (f fixture) postQuestion(t *testing.T) string {
	t.Helper()
	answer := "ship it"
	msg, err := f.box.Post(mailbox.Draft{From: "conductor-claude", To: "human", Kind: "question", Subject: "Ship?", Recommended: &answer})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := msg.Get("id")
	return id.(string)
}

func (f fixture) writeFlip(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.project.FlipPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.project.FlipPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestATerminalReplyIsWrittenAsTheJSWritesIt(t *testing.T) {
	f := newFixture(t)
	id := f.postQuestion(t)
	result, err := Post(f.box, f.project, Request{ID: "id-", Body: "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != id || result.ReplyID != "id-2" || result.Gate != GateUntouched {
		t.Errorf("result = %+v", result)
	}
	msgs := lines(t, filepath.Join(f.box.Dir, "messages.jsonl"))
	want := `{"id":"id-2","at":"` + at + `","from":"human","to":"conductor-claude","kind":"note","subject":"Re: Ship?","body":"yes","refs":[],"thread":"id-1"}`
	if msgs[1] != want {
		t.Errorf("reply line = %s\nwant          %s", msgs[1], want)
	}
	acks := lines(t, filepath.Join(f.box.Dir, "acks.jsonl"))
	if want := `{"id":"id-1","by":"human","at":"` + at + `","note":"replied"}`; acks[0] != want {
		t.Errorf("ack = %s", acks[0])
	}
}

func TestARemoteReplyIsMarkedAfterThreadAndAckedViaWorkTrains(t *testing.T) {
	for _, kind := range []string{"accept", "typed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.postQuestion(t)
			remote := &mailbox.RemoteRef{AnswerID: "a-1", Kind: kind}
			if _, err := Post(f.box, f.project, Request{ID: "id-1", Body: "ship it", Remote: remote}); err != nil {
				t.Fatal(err)
			}
			msgs := lines(t, filepath.Join(f.box.Dir, "messages.jsonl"))
			want := `{"id":"id-2","at":"` + at + `","from":"human","to":"conductor-claude","kind":"note","subject":"Re: Ship?","body":"ship it","refs":[],"thread":"id-1","via":"remote","remote":{"answer_id":"a-1","kind":"` + kind + `"}}`
			if msgs[1] != want {
				t.Errorf("reply line = %s\nwant          %s", msgs[1], want)
			}
			acks := lines(t, filepath.Join(f.box.Dir, "acks.jsonl"))
			if want := `{"id":"id-1","by":"human","at":"` + at + `","note":"replied via Work Trains"}`; acks[0] != want {
				t.Errorf("ack = %s", acks[0])
			}
		})
	}
}

func TestAnUnknownIDPostsNothing(t *testing.T) {
	f := newFixture(t)
	f.postQuestion(t)
	if _, err := Post(f.box, f.project, Request{ID: "id-9", Body: "x"}); !errors.Is(err, mailbox.ErrNoMessage) {
		t.Fatalf("err = %v, want ErrNoMessage", err)
	}
	if got := len(lines(t, filepath.Join(f.box.Dir, "messages.jsonl"))); got != 1 {
		t.Errorf("%d messages, want only the question", got)
	}
}

func TestReplyingToTheGatesQuestionClearsTheGateAndQueuesTheRelaunch(t *testing.T) {
	f := newFixture(t)
	f.postQuestion(t)
	f.writeFlip(t, `{"to":"claude","awaiting_human":{"at":"x","door":"claude","reason":"r","question":"id-1"}}`)
	result, err := Post(f.box, f.project, Request{ID: "id-1", Body: "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate != GateCleared || result.Door != "claude" {
		t.Errorf("gate = %v door = %v", result.Gate, result.Door)
	}
	flip := strings.Join(lines(t, f.project.FlipPath), "\n")
	if strings.Contains(flip, "awaiting_human") || !strings.Contains(flip, `"reason": "human gate cleared"`) {
		t.Errorf("flip.json = %s", flip)
	}
}

func TestAKeptPendingLaunchWithoutADoorClearsWithoutRelaunch(t *testing.T) {
	f := newFixture(t)
	f.postQuestion(t)
	f.writeFlip(t, `{"pending_launch":{"to":""},"awaiting_human":{"door":"claude","question":"id-1"}}`)
	result, err := Post(f.box, f.project, Request{ID: "id-1", Body: "yes"})
	if err != nil || result.Gate != GateClearedWithoutRelaunch {
		t.Errorf("gate = %v, err = %v", result.Gate, err)
	}
}

func TestAGateOnAnotherQuestionIsUntouched(t *testing.T) {
	f := newFixture(t)
	f.postQuestion(t)
	f.postQuestion(t)
	const gated = `{"awaiting_human":{"door":"claude","question":"id-2"}}`
	f.writeFlip(t, gated)
	result, err := Post(f.box, f.project, Request{ID: "id-1", Body: "yes"})
	if err != nil || result.Gate != GateUntouched {
		t.Errorf("gate = %v, err = %v", result.Gate, err)
	}
	if got := strings.Join(lines(t, f.project.FlipPath), "\n"); got != gated {
		t.Errorf("flip.json = %s", got)
	}
}

func TestGateProblemsAreReportedAndTheReplyStands(t *testing.T) {
	for _, c := range []struct {
		name string
		flip string
		want Gate
	}{
		{"unparsable flip.json", "{", GateUnreadable},
		{"question matching nothing", `{"awaiting_human":{"question":"nope"}}`, GateUnmatched},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.postQuestion(t)
			f.writeFlip(t, c.flip)
			result, err := Post(f.box, f.project, Request{ID: "id-1", Body: "yes"})
			if err != nil || result.Gate != c.want || result.GateErr == nil {
				t.Errorf("result = %+v, err = %v", result, err)
			}
			if got := len(lines(t, filepath.Join(f.box.Dir, "messages.jsonl"))); got != 2 {
				t.Errorf("%d messages, want the question and the reply", got)
			}
		})
	}
}

// Finish is the tail a crash can cut off after the note is posted: it acks only when told the
// human has not, and clears the gate the original is the question of.
func TestFinishAcksOnlyWhenTheHumanHasNotAndClearsTheGate(t *testing.T) {
	for _, acked := range []bool{false, true} {
		t.Run(fmt.Sprint("acked=", acked), func(t *testing.T) {
			f := newFixture(t)
			id := f.postQuestion(t)
			f.writeFlip(t, `{"awaiting_human":{"door":"claude","question":"`+id+`"}}`)
			result, err := Finish(f.box, f.project, id, &mailbox.RemoteRef{AnswerID: "a-1", Kind: "accept"}, acked)
			if err != nil {
				t.Fatal(err)
			}
			if result.ID != id || result.ReplyID != "" || result.Gate != GateCleared || result.Door != "claude" {
				t.Errorf("result = %+v", result)
			}
			_, statErr := os.Stat(filepath.Join(f.box.Dir, "acks.jsonl"))
			if wrote := statErr == nil; wrote == acked {
				t.Errorf("ack written = %v with acked = %v", wrote, acked)
			}
		})
	}
}
