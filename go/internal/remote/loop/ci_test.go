package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/ci"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/ci/citest"
)

const greenRun = `[{"workflowName":"ci","status":"completed","conclusion":"success","url":"https://github.com/example/app/actions/runs/1","createdAt":"2026-10-21T08:00:00Z"}]`

// withCI turns ci on over a fake git whose origin is a GitHub repository and a fake gh.
func (h *harness) withCI() *citest.Fake {
	h.loop.Config.CI = true
	return citest.Install(h.t, "git@github.com:example/app.git", "")
}

func TestCIRunsArePushedAndCachedForFiveMinutes(t *testing.T) {
	h := newHarness(t)
	fake := h.withCI()
	fake.PrintRuns(greenRun)
	alpha := h.project("alpha")
	if _, err := h.cycle(); err != nil {
		t.Fatal(err)
	}
	pushed := h.worker.pushLog()[0].request.Projects[0].CI
	if len(pushed) != 1 || pushed[0].Repo != "example/app" || *pushed[0].Conclusion != "success" {
		t.Errorf("pushed ci = %+v, want the green run", pushed)
	}
	if entry := h.state().CI[h.key(alpha)]; len(entry.Runs) != 1 || !entry.CheckedAt.Equal(start) {
		t.Errorf("state ci entry = %+v", entry)
	}

	h.clock.advance(ci.RefreshAfter - time.Second)
	if _, err := h.pushDue(); err != nil || fake.Calls() != 1 {
		t.Errorf("within five minutes: %v after %d gh calls, want the cached runs", err, fake.Calls())
	}
	fake.PrintRuns("[]")
	h.clock.advance(time.Second)
	if report, err := h.pushDue(); err != nil || fake.Calls() != 2 || report.Pushed != 1 {
		t.Errorf("after five minutes: %+v, %v after %d gh calls, want a fresh check pushed", report, err, fake.Calls())
	}
	if pushes := h.worker.pushLog(); len(pushes[len(pushes)-1].request.Projects[0].CI) != 0 {
		t.Errorf("the last push still carries the old run")
	}
}

func TestACIFailureIsANoteInLastErrorThatMovesOnlyWhenItChanges(t *testing.T) {
	h := newHarness(t)
	fake := h.withCI()
	fake.Fail(4, "To get started with GitHub CLI, please run:  gh auth login")
	h.project("alpha")
	if _, err := h.cycle(); err != nil {
		t.Fatalf("a CI failure failed the cycle: %v", err)
	}
	note := h.state().LastError
	if note == nil || note.Kind != "ci" || note.Message != "alpha: gh run list: exit status 4: To get started with GitHub CLI, please run:  gh auth login" || !note.At.Equal(start) {
		t.Fatalf("last_error = %+v, want the ci note", note)
	}
	if pushed := h.worker.pushLog()[0].request.Projects[0].CI; pushed == nil || len(pushed) != 0 {
		t.Errorf("pushed ci = %v, want []", pushed)
	}

	h.clock.advance(ci.RefreshAfter)
	if _, err := h.cycle(); err != nil || fake.Calls() != 2 {
		t.Fatalf("second cycle: %v after %d gh calls", err, fake.Calls())
	}
	if again := h.state().LastError; again == nil || !again.At.Equal(start) {
		t.Errorf("the same note moved: %+v", again)
	}
	if logged := strings.Count(h.log.String(), "ci left out for alpha"); logged != 1 {
		t.Errorf("the note was logged %d times, want once:\n%s", logged, h.log)
	}

	fake.PrintRuns(greenRun)
	h.clock.advance(ci.RefreshAfter)
	if _, err := h.cycle(); err != nil || h.state().LastError != nil {
		t.Errorf("after gh recovered: %v, last_error %+v; want none", err, h.state().LastError)
	}
}

func TestTurningCIOffRunsNoGhAndDropsTheCachedRuns(t *testing.T) {
	h := newHarness(t)
	fake := h.withCI()
	fake.PrintRuns(greenRun)
	h.project("alpha")
	if _, err := h.cycle(); err != nil || len(h.state().CI) != 1 {
		t.Fatalf("with ci on: %v, state ci %+v", err, h.state().CI)
	}

	h.loop.Config.CI = false
	h.clock.advance(ci.RefreshAfter)
	if _, err := h.cycle(); err != nil || h.state().CI != nil || fake.Calls() != 1 {
		t.Errorf("with ci off: %v, state ci %+v after %d gh calls; want none and no new call", err, h.state().CI, fake.Calls())
	}
	if pushes := h.worker.pushLog(); len(pushes) != 2 || len(pushes[1].request.Projects[0].CI) != 0 {
		t.Errorf("with ci off the runs were not withdrawn from the page")
	}
}
