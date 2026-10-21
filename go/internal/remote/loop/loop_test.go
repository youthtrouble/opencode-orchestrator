package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/apply"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/client"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/config"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

var start = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// syncBuffer is the loop's log; the race detector sees the test read it after Serve wrote it.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is a loop over temp projects under one root, talking to a fakeWorker on a fake
// clock. Every scenario ends by checking that no secret reached the log, an error or the
// state file.
type harness struct {
	t         *testing.T
	root      string
	home      string
	stateFile string
	clock     *fakeClock
	worker    *fakeWorker
	log       *syncBuffer
	loop      *Loop
	errs      []error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, root: root, home: t.TempDir(), clock: &fakeClock{now: start}, log: &syncBuffer{}}
	h.stateFile = state.File(h.home)
	if _, err := state.Ensure(h.stateFile); err != nil {
		t.Fatal(err)
	}
	h.worker = newFakeWorker(t, h.clock)
	registryFile := filepath.Join(h.home, "models.json")
	writeFile(t, registryFile, `{"conductor":{"home":"claude"}}`)
	h.loop = &Loop{
		Config:      config.Config{URL: h.worker.server.URL, Roots: []string{root}, Exclude: []string{}, PollSeconds: 30},
		StateFile:   h.stateFile,
		Home:        h.home,
		OrchVersion: "0.0.0-test",
		Connect: func(machineID string) (Remote, error) {
			return client.New(h.worker.server.URL, fakeCredentials, machineID, "0.0.0-test"), nil
		},
		NewApplier: func(projects apply.ProjectResolver) Applier {
			return apply.New(projects, registryFile, state.Dir(h.home))
		},
		Budget: func() (*wire.Budget, error) { return nil, nil },
		Alive:  func(any) bool { return false },
		Now:    h.clock.Now,
		Sleep:  h.clock.Sleep,
		Jitter: func(ceiling time.Duration) time.Duration { return ceiling },
		Log:    h.log,
	}
	t.Cleanup(h.assertNoSecretsLeaked)
	return h
}

func (h *harness) assertNoSecretsLeaked() {
	data, err := os.ReadFile(h.stateFile)
	if err != nil {
		h.t.Fatal(err)
	}
	texts := []string{h.log.String(), string(data)}
	for _, err := range h.errs {
		texts = append(texts, err.Error())
	}
	for _, text := range texts {
		for _, secret := range []string{fakeCredentials.MachineToken, fakeCredentials.AccessClientID, fakeCredentials.AccessClientSecret} {
			if strings.Contains(text, secret) {
				h.t.Errorf("a secret leaked into %q", text)
			}
		}
	}
}

func (h *harness) cycle() (Report, error) {
	report, err := h.loop.Cycle(context.Background())
	if err != nil {
		h.errs = append(h.errs, err)
	}
	return report, err
}

func (h *harness) pushDue() (Report, error) {
	report, err := h.loop.PushDue(context.Background())
	if err != nil {
		h.errs = append(h.errs, err)
	}
	return report, err
}

func (h *harness) state() state.State {
	h.t.Helper()
	st, err := state.Load(h.stateFile)
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

// project creates a project directory under the root and returns its path.
func (h *harness) project(name string) string {
	h.t.Helper()
	dir := filepath.Join(h.root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".orchestrator"), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return dir
}

func (h *harness) key(dir string) string { return snapshot.Key(h.state().MachineID, dir) }

// post adds a message to the project's mailbox and returns its id.
func (h *harness) post(dir string, draft mailbox.Draft) string {
	h.t.Helper()
	msg, err := mailbox.New(dir).Post(draft)
	if err != nil {
		h.t.Fatal(err)
	}
	return jsjson.Path(msg, "id").(string)
}

func (h *harness) question(dir string) string {
	recommended := "Yes, ship it."
	return h.post(dir, mailbox.Draft{From: "conductor-claude", To: "human", Kind: "question", Subject: "Ship?", Recommended: &recommended})
}

// replies are the human's replies in the project's mailbox.
func (h *harness) replies(dir string) []string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".orchestrator", "mailbox", "messages.jsonl"))
	if err != nil {
		h.t.Fatal(err)
	}
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"from":"human"`) {
			found = append(found, line)
		}
	}
	return found
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

// paused reports whether the project's flip.json holds a human gate.
func paused(dir string) bool {
	return jsjson.Truthy(jsjson.Path(flipcmd.NewProject(dir).ReadFlip(), "awaiting_human"))
}

func pushedKeys(push timedPush) []string {
	keys := []string{}
	for _, project := range push.request.Projects {
		keys = append(keys, project.Key)
	}
	return keys
}

func answerFor(key, questionID string) wire.PulledAnswer {
	return wire.PulledAnswer{ID: "00000000-0000-4000-8000-00000000a001", ProjectKey: key, QuestionID: questionID, Kind: "accept", Text: "Yes, ship it.", CreatedAt: "2026-10-21T09:00:00.000Z"}
}

func pauseFor(key string) wire.PulledAction {
	return wire.PulledAction{ID: "00000000-0000-4000-8000-00000000b001", ProjectKey: key, Kind: "pause", CreatedAt: "2026-10-21T09:00:00.000Z"}
}

func TestAFullCycleAppliesAcksBothOutcomesAndPushesOnlyTheChangedProject(t *testing.T) {
	h := newHarness(t)
	alpha, beta := h.project("alpha"), h.project("beta")
	questionID := h.question(alpha)
	if report, err := h.cycle(); err != nil || report.Pushed != 2 || report.Pulled != 0 {
		t.Fatalf("first cycle = %+v, %v; want both projects pushed", report, err)
	}

	h.worker.queueAnswer(answerFor(h.key(alpha), questionID))
	h.worker.queueAction(pauseFor(h.key(alpha)))
	h.clock.advance(61 * time.Second)
	report, err := h.cycle()
	if err != nil || report != (Report{Pulled: 2, Applied: 2, Pushed: 1, PollAfter: 30 * time.Second}) {
		t.Fatalf("second cycle = %+v, %v", report, err)
	}

	replies := h.replies(alpha)
	if len(replies) != 1 || !strings.Contains(replies[0], `"via":"remote"`) || !strings.Contains(replies[0], `"thread":"`+questionID+`"`) {
		t.Errorf("replies = %q, want one remote reply to the question", replies)
	}
	flip, err := os.ReadFile(filepath.Join(alpha, ".orchestrator", "flip.json"))
	if err != nil || !strings.Contains(string(flip), `"reason": "paused from Work Trains"`) {
		t.Errorf("flip.json = %s, %v; want the page's gate", flip, err)
	}
	acks := h.worker.ackLog()
	if len(acks) != 1 || len(acks[0].Results) != 2 ||
		acks[0].Results[0] != (wire.AckResult{ID: answerFor("", "").ID, Type: "answer", Outcome: "applied", Detail: "replied"}) ||
		acks[0].Results[1].Type != "action" || acks[0].Results[1].Outcome != "applied" {
		t.Errorf("acks = %+v, want the answer and the pause applied", acks)
	}
	pushes := h.worker.pushLog()
	if len(pushes) != 2 || strings.Join(pushedKeys(pushes[1]), ",") != h.key(alpha) {
		t.Errorf("second push carried %v, want only alpha", pushedKeys(pushes[len(pushes)-1]))
	}
	if h.worker.pending() != 0 {
		t.Error("the Worker still holds pulled items")
	}
	st := h.state()
	if st.LastPullAt == nil || st.LastPushAt == nil || st.LastError != nil || st.Projects[h.key(beta)].LastHash == "" {
		t.Errorf("state = %+v", st)
	}
	for _, want := range []string{"answer " + answerFor("", "").ID + ": applied: replied", "action " + pauseFor("").ID + ": applied", "pushed 1 project(s), 0 deferred"} {
		if !strings.Contains(h.log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, h.log)
		}
	}
}

func TestAQuietCycleSendsNoPushAndLogsNothing(t *testing.T) {
	h := newHarness(t)
	h.project("alpha")
	if _, err := h.cycle(); err != nil {
		t.Fatal(err)
	}
	logged := h.log.String()
	h.clock.advance(5 * time.Minute)
	if report, err := h.cycle(); err != nil || report.Pushed != 0 {
		t.Fatalf("quiet cycle = %+v, %v", report, err)
	}
	if len(h.worker.pushLog()) != 1 || h.log.String() != logged {
		t.Errorf("a quiet cycle pushed or logged:\n%s", h.log)
	}
	before, err := os.Stat(h.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(15 * time.Second)
	if _, err := h.pushDue(); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(h.stateFile); err != nil || !os.SameFile(before, after) {
		t.Error("a quiet local check rewrote the state file")
	}
}

func TestNetworkLossBacksOffKeepsStateAndRecoversWithoutADuplicateReply(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	questionID := h.question(alpha)
	h.worker.queueAnswer(answerFor(h.key(alpha), questionID))
	h.worker.setDown(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lastErrorWhileDown *state.LastError
	h.clock.onSleep = func(n int) {
		switch n {
		case 1:
			lastErrorWhileDown = h.state().LastError
		case 3:
			h.worker.setDown(false)
		case 8:
			cancel()
		}
	}
	h.loop.Serve(ctx)

	sleeps := h.clock.sleepLog()
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}
	if fmt.Sprint(sleeps) != fmt.Sprint(want) {
		t.Errorf("sleeps = %v, want backoff 5s, 10s, 20s then 15 s rechecks", sleeps)
	}
	if lastErrorWhileDown == nil || lastErrorWhileDown.Kind != "network" {
		t.Errorf("last_error while down = %+v, want a network error persisted", lastErrorWhileDown)
	}
	if replies := h.replies(alpha); len(replies) != 1 {
		t.Errorf("replies = %q, want exactly one", replies)
	}
	if st := h.state(); st.LastError != nil || st.Projects[h.key(alpha)].LastHash == "" {
		t.Errorf("state after recovery = %+v", st)
	}
	log := h.log.String()
	if strings.Count(log, "cycle failed (network)") != 3 || !strings.Contains(log, "retrying in 20s") ||
		!strings.Contains(log, "recovered after 3 failed cycle(s)") || !strings.HasSuffix(log, "serve stopped\n") {
		t.Errorf("log:\n%s", log)
	}
}

func TestTheBackoffDoublesToFiveMinutesAndUsesEqualJitter(t *testing.T) {
	h := newHarness(t)
	var ceilings []time.Duration
	h.loop.Jitter = func(ceiling time.Duration) time.Duration { ceilings = append(ceilings, ceiling); return ceiling / 2 }
	for failures := 1; failures <= 9; failures++ {
		if got := h.loop.backoff(failures); got != ceilings[len(ceilings)-1]/2 {
			t.Errorf("backoff(%d) = %v, want the jittered ceiling", failures, got)
		}
	}
	want := "[5s 10s 20s 40s 1m20s 2m40s 5m0s 5m0s 5m0s]"
	if fmt.Sprint(ceilings) != want {
		t.Errorf("ceilings = %v, want %s", ceilings, want)
	}
	for range 1000 {
		if d := EqualJitter(time.Second); d < time.Second/2 || d > time.Second {
			t.Fatalf("EqualJitter(1s) = %v, want it in [500ms, 1s]", d)
		}
	}
}

func TestALostAckIsResentAndTheAnswerIsAppliedOnce(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	questionID := h.question(alpha)
	h.worker.queueAnswer(answerFor(h.key(alpha), questionID))
	h.worker.dropAcks = 1

	_, err := h.cycle()
	var network *client.NetworkError
	if !errors.As(err, &network) {
		t.Fatalf("cycle with a dropped ack = %v, want a network error", err)
	}
	if len(h.worker.pushLog()) != 0 {
		t.Error("a cycle whose ack failed went on to push")
	}
	report, err := h.cycle()
	if err != nil || report.Pulled != 1 || report.Applied != 1 {
		t.Fatalf("retry cycle = %+v, %v", report, err)
	}
	if replies := h.replies(alpha); len(replies) != 1 {
		t.Errorf("replies = %q, want exactly one", replies)
	}
	acks := h.worker.ackLog()
	if len(acks) != 2 || acks[0].Results[0].ID != acks[1].Results[0].ID || acks[1].Results[0].Outcome != "applied" {
		t.Errorf("acks = %+v, want the same answer acked twice, applied", acks)
	}
	if h.worker.pending() != 0 {
		t.Error("the answer is still pending after the re-sent ack")
	}
}

// The Worker re-sends a pause whose ack was lost after the owner resumed on the Mac. Every cycle
// builds its applier afresh from what is on disk, as a restarted serve would, and the action
// journal answers the replay with the recorded outcome instead of gating the project again.
func TestAPauseWhoseAckWasLostDoesNotUndoALocalResume(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	h.worker.queueAction(pauseFor(h.key(alpha)))
	h.worker.dropAcks = 1
	var network *client.NetworkError
	if _, err := h.cycle(); !errors.As(err, &network) {
		t.Fatalf("cycle with a dropped ack = %v, want a network error", err)
	}
	if !paused(alpha) {
		t.Fatal("the pause was not applied")
	}
	if _, err := flipcmd.NewProject(alpha).Gate(cliargs.Opts{"clear": {Flag: true}}); err != nil {
		t.Fatal(err)
	}

	report, err := h.cycle()
	if err != nil || report.Pulled != 1 || report.Applied != 1 {
		t.Fatalf("replay cycle = %+v, %v", report, err)
	}
	if paused(alpha) {
		t.Error("the replayed pause gated the project again over the local resume")
	}
	acks := h.worker.ackLog()
	if len(acks) != 2 || acks[1].Results[0] != (wire.AckResult{ID: pauseFor("").ID, Type: "action", Outcome: "applied", Detail: "pauses at the next brief boundary; no conductor running"}) {
		t.Errorf("acks = %+v, want the pause's first outcome resent", acks)
	}
	if h.worker.pending() != 0 {
		t.Error("the pause is still pending after the resent ack")
	}
}

// An item apply cannot settle (here a pause whose project's action journal does not parse) is
// left unacked for the Worker to send again, while the other items are acked, and logged once
// however many pulls and local checks pass; once the journal is readable the next pull applies
// and acks it.
func TestAnItemApplyCannotSettleIsNotAckedUntilItIs(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	questionID := h.question(alpha)
	journal := filepath.Join(alpha, ".orchestrator", "remote", "actions.json")
	writeFile(t, journal, "[")
	h.worker.queueAnswer(answerFor(h.key(alpha), questionID))
	h.worker.queueAction(pauseFor(h.key(alpha)))

	for range 2 {
		if report, err := h.cycle(); err != nil || report.Pulled < 1 {
			t.Fatalf("cycle = %+v, %v", report, err)
		}
		if _, err := h.pushDue(); err != nil {
			t.Fatal(err)
		}
	}
	acks := h.worker.ackLog()
	if len(acks) != 1 || len(acks[0].Results) != 1 || acks[0].Results[0].Type != "answer" {
		t.Fatalf("acks = %+v, want only the answer acked", acks)
	}
	if h.worker.pending() != 1 || paused(alpha) {
		t.Fatalf("pending = %d, paused = %v; want the pause pending and not applied", h.worker.pending(), paused(alpha))
	}
	if n := strings.Count(h.log.String(), "action <uuid> not applied, the Worker sends it again: read the action journal\n"); n != 1 {
		t.Errorf("logged the unsettled pause %d times over two pulls, want once:\n%s", n, h.log)
	}
	if strings.Contains(h.log.String(), "read the action journal: ") {
		t.Errorf("logged the unsettled pause's error text, want only its step:\n%s", h.log)
	}

	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	report, err := h.cycle()
	if err != nil || report != (Report{Pulled: 1, Applied: 1, PollAfter: 30 * time.Second}) {
		t.Fatalf("cycle after the repair = %+v, %v", report, err)
	}
	if acks := h.worker.ackLog(); len(acks) != 2 || acks[1].Results[0].ID != pauseFor("").ID || acks[1].Results[0].Outcome != "applied" {
		t.Errorf("acks = %+v, want the pause acked applied", acks)
	}
	if h.worker.pending() != 0 || !paused(alpha) {
		t.Errorf("pending = %d, paused = %v; want the pause applied and settled", h.worker.pending(), paused(alpha))
	}
}

func TestAProjectChangingEvery10sIsPushedAtMostOncePerMinute(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	for step := range 19 {
		h.post(alpha, mailbox.Draft{From: "conductor-claude", To: "*", Kind: "note", Subject: fmt.Sprintf("progress %d", step)})
		if _, err := h.pushDue(); err != nil {
			t.Fatal(err)
		}
		h.clock.advance(10 * time.Second)
	}
	pushes := h.worker.pushLog()
	var gaps []time.Duration
	for i := 1; i < len(pushes); i++ {
		gaps = append(gaps, pushes[i].at.Sub(pushes[i-1].at))
	}
	if len(pushes) != 4 || fmt.Sprint(gaps) != "[1m0s 1m0s 1m0s]" {
		t.Errorf("pushes over 3 minutes = %d with gaps %v, want 4, 60 s apart", len(pushes), gaps)
	}
}

func TestADeferredProjectWaitsForTheWorkersRetryAfter(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	h.worker.deferKey, h.worker.deferFor = h.key(alpha), 42
	report, err := h.pushDue()
	if err != nil || report.Pushed != 1 || report.Deferred != 1 {
		t.Fatalf("first push = %+v, %v; want it deferred", report, err)
	}
	if retry := h.state().Projects[h.key(alpha)].RetryAfter; retry == nil || !retry.Equal(start.Add(42*time.Second)) {
		t.Errorf("retry_after = %v, want now + 42 s", retry)
	}
	for range 3 {
		h.clock.advance(15 * time.Second)
		if _, err := h.pushDue(); err != nil {
			t.Fatal(err)
		}
	}
	pushes := h.worker.pushLog()
	if len(pushes) != 2 || !pushes[1].at.Equal(start.Add(45*time.Second)) {
		t.Errorf("pushes at %v, want the retry at the first check past 42 s", pushes)
	}
	if entry := h.state().Projects[h.key(alpha)]; entry.RetryAfter != nil || entry.LastAcceptedAt == nil {
		t.Errorf("entry after the accepted retry = %+v", entry)
	}
}

// A retry_after_seconds outside the schema's 1..300 is not trusted: the project waits the
// push spacing instead of an overflowed or near-endless wait persisted in the state.
func TestRetryDelayTrustsOnlyTheSchemasBounds(t *testing.T) {
	for _, tc := range []struct {
		seconds *int
		want    time.Duration
	}{
		{nil, pushSpacing},
		{intPtr(math.MinInt), pushSpacing},
		{intPtr(-1), pushSpacing},
		{intPtr(0), pushSpacing},
		{intPtr(1), time.Second},
		{intPtr(300), 300 * time.Second},
		{intPtr(301), pushSpacing},
		{intPtr(math.MaxInt), pushSpacing},
	} {
		if got := retryDelay(tc.seconds); got != tc.want {
			t.Errorf("retryDelay(%s) = %v, want %v", describeSeconds(tc.seconds), got, tc.want)
		}
	}
}

func intPtr(n int) *int { return &n }

func describeSeconds(seconds *int) string {
	if seconds == nil {
		return "nil"
	}
	return fmt.Sprint(*seconds)
}

func TestPushesOverTheSchemaCapLeaveTheRestDueForTheNextPush(t *testing.T) {
	h := newHarness(t)
	for i := range maxProjectsPerPush + 1 {
		h.project(fmt.Sprintf("p%02d", i))
	}
	first, err := h.pushDue()
	if err != nil || first.Pushed != maxProjectsPerPush {
		t.Fatalf("first push = %+v, %v; want the cap", first, err)
	}
	second, err := h.pushDue()
	if err != nil || second.Pushed != 1 {
		t.Fatalf("second push = %+v, %v; want the one left", second, err)
	}
	if pushes := h.worker.pushLog(); pushedKeys(pushes[1])[0] != h.key(filepath.Join(h.root, "p50")) {
		t.Errorf("second push = %v, want p50", pushedKeys(pushes[1]))
	}
}

// scriptedApplier returns fixed outcomes and errors.
type scriptedApplier struct {
	answer, action       apply.Outcome
	answerErr, actionErr error
}

func (s scriptedApplier) ApplyAnswer(wire.PulledAnswer) (apply.Outcome, error) {
	return s.answer, s.answerErr
}

func (s scriptedApplier) ApplyAction(wire.PulledAction) (apply.Outcome, error) {
	return s.action, s.actionErr
}

func TestAnAppliedOutcomeWithAnErrorIsAckedAppliedAndTheErrorOnlyLogged(t *testing.T) {
	h := newHarness(t)
	alpha := h.project("alpha")
	gateErr := fmt.Errorf("check the human gate: open %s/.orchestrator/flip.json: permission denied", h.home)
	h.loop.NewApplier = func(apply.ProjectResolver) Applier {
		return scriptedApplier{
			answer:    apply.Outcome{Result: apply.Applied, Detail: "replied; could not check the human gate"},
			answerErr: gateErr,
			action:    apply.Outcome{Result: apply.Rejected, Detail: "pause: disk full"},
			actionErr: errors.New("pause: disk full"),
		}
	}
	h.worker.queueAnswer(answerFor(h.key(alpha), "m-1"))
	h.worker.queueAction(pauseFor(h.key(alpha)))
	report, err := h.cycle()
	if err != nil || report.Applied != 1 {
		t.Fatalf("cycle = %+v, %v; want success with one applied", report, err)
	}
	acks := h.worker.ackLog()
	if len(acks) != 1 || acks[0].Results[0].Outcome != "applied" || acks[0].Results[0].Detail != "replied; could not check the human gate" ||
		acks[0].Results[1].Outcome != "rejected" {
		t.Errorf("acks = %+v, want applied then rejected", acks)
	}
	log := h.log.String()
	if !strings.Contains(log, "applied: replied; could not check the human gate (error: check the human gate: open ~/.orchestrator/flip.json") ||
		!strings.Contains(log, "rejected: pause: disk full (error: pause: disk full)") || strings.Contains(log, h.home) {
		t.Errorf("log does not carry the redacted errors:\n%s", log)
	}
	if h.state().LastError != nil {
		t.Error("an apply error was recorded as a failed cycle")
	}
}

func TestAPullWithAnUnknownFieldFailsTheCycleBeforeApplyingAnything(t *testing.T) {
	h := newHarness(t)
	h.project("alpha")
	h.worker.pullReply = func(rw http.ResponseWriter, r *http.Request) {
		reply(rw, http.StatusOK, json.RawMessage(`{"v":1,"server_time":"2026-10-21T09:00:00.000Z","poll_after_seconds":30,"answers":[],"actions":[],"surprise":1}`))
	}
	_, err := h.cycle()
	var protocol *client.ProtocolError
	if !errors.As(err, &protocol) {
		t.Fatalf("err = %v, want a ProtocolError", err)
	}
	if len(h.worker.ackLog()) != 0 || len(h.worker.pushLog()) != 0 {
		t.Error("a failed pull went on to ack or push")
	}
	if last := h.state().LastError; last == nil || last.Kind != "protocol" {
		t.Errorf("last_error = %+v, want protocol", last)
	}
}

func TestARefusedCredentialIsRecordedAndNeverLogged(t *testing.T) {
	h := newHarness(t)
	h.project("alpha")
	wrong := fakeCredentials
	wrong.AccessClientSecret = "fake-client-secret-91bc-but-wrong"
	h.loop.Connect = func(machineID string) (Remote, error) {
		return client.New(h.worker.server.URL, wrong, machineID, "0.0.0-test"), nil
	}
	_, err := h.cycle()
	var status *client.HTTPStatusError
	if !errors.As(err, &status) || status.ErrorCode != "forbidden" {
		t.Fatalf("err = %v, want 403 forbidden", err)
	}
	if strings.Contains(string(mustRead(t, h.stateFile)), wrong.AccessClientSecret) {
		t.Error("the refused secret reached the state file")
	}
}

// echoedSecret is what a response echoing the request's Access client secret repeats; the
// harness fails the test if it reaches the log, the state file or a returned error.
func echoedSecret(r *http.Request) string { return r.Header.Get("CF-Access-Client-Secret") }

func TestNoPulledTextReachesTheLogTheStateOrAnError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pullReply func(http.ResponseWriter, *http.Request, string)
		lastError *state.LastError
	}{{
		name: "an error code that is not the protocol's",
		pullReply: func(rw http.ResponseWriter, r *http.Request, _ string) {
			reply(rw, http.StatusForbidden, map[string]string{"error": echoedSecret(r), "message": echoedSecret(r)})
		},
		lastError: &state.LastError{Kind: "http_status", Message: "GET /api/v1/machine/pull: HTTP 403 unknown"},
	}, {
		name: "an unknown field named after the secret",
		pullReply: func(rw http.ResponseWriter, r *http.Request, _ string) {
			reply(rw, http.StatusOK, json.RawMessage(`{"v":1,"server_time":"2026-10-21T09:00:00.000Z","poll_after_seconds":30,"answers":[],"actions":[],"`+echoedSecret(r)+`":1}`))
		},
		lastError: &state.LastError{Kind: "protocol", Message: "GET /api/v1/machine/pull: bad response: a field not in the schema"},
	}, {
		name: "a malformed redirect location with user info",
		pullReply: func(rw http.ResponseWriter, r *http.Request, _ string) {
			rw.Header().Set("Location", "https://user:"+echoedSecret(r)+"@example.com/%zz")
			rw.WriteHeader(http.StatusFound)
		},
		lastError: &state.LastError{Kind: "access_denied", Message: "GET /api/v1/machine/pull: access denied (HTTP 302, not a Worker response); check the Access service token (orch remote service-token set) and the Access Service Auth policy"},
	}, {
		name: "an answer whose id is the secret",
		pullReply: func(rw http.ResponseWriter, r *http.Request, key string) {
			answer := answerFor(key, "m-1")
			answer.ID = echoedSecret(r)
			reply(rw, http.StatusOK, wire.PullResponse{V: 1, ServerTime: "2026-10-21T09:00:00.000Z", PollAfterSeconds: 30, Answers: []wire.PulledAnswer{answer}, Actions: []wire.PulledAction{}})
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			key := h.key(h.project("alpha"))
			h.worker.pullReply = func(rw http.ResponseWriter, r *http.Request) { tc.pullReply(rw, r, key) }
			_, err := h.cycle()
			if (err != nil) != (tc.lastError != nil) {
				t.Fatalf("cycle err = %v", err)
			}
			last := h.state().LastError
			switch {
			case tc.lastError == nil && last != nil:
				t.Errorf("last_error = %+v, want none", last)
			case tc.lastError != nil && (last == nil || last.Kind != tc.lastError.Kind || last.Message != tc.lastError.Message):
				t.Errorf("last_error = %+v, want %+v", last, tc.lastError)
			}
		})
	}
}

func TestServeStopsMidRequestWhenItsContextEnds(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.loop.Connect = func(string) (Remote, error) { return blockingRemote{cancel: cancel}, nil }
	done := make(chan struct{})
	go func() {
		h.loop.Serve(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context ended")
	}
	if last := h.state().LastError; last != nil {
		t.Errorf("last_error = %+v, want none: stopping is not a failure", last)
	}
}

// blockingRemote ends the context on pull and waits for the cancellation to reach it.
type blockingRemote struct{ cancel context.CancelFunc }

func (b blockingRemote) Pull(ctx context.Context) (wire.PullResponse, error) {
	b.cancel()
	<-ctx.Done()
	return wire.PullResponse{}, &client.NetworkError{Route: "GET /api/v1/machine/pull", Problem: "cancelled"}
}

func (blockingRemote) Push(context.Context, wire.PushRequest) (wire.PushResponse, error) {
	panic("not reached")
}

func (blockingRemote) Ack(context.Context, wire.AckRequest) (wire.AckResponse, error) {
	panic("not reached")
}

func TestAnUnreadableProjectIsLoggedOnceAndTheOthersStillGo(t *testing.T) {
	h := newHarness(t)
	h.project("alpha")
	broken := h.project("broken")
	writeFile(t, filepath.Join(broken, ".orchestrator", "mailbox", "messages.jsonl"), "")
	if err := os.Chmod(filepath.Join(broken, ".orchestrator", "mailbox", "messages.jsonl"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(broken, ".orchestrator", "mailbox", "messages.jsonl"), 0o600) })
	for range 3 {
		if report, err := h.pushDue(); err != nil || report.Pushed > 1 {
			t.Fatalf("push = %+v, %v", report, err)
		}
		h.clock.advance(15 * time.Second)
	}
	if n := strings.Count(h.log.String(), "skipped broken"); n != 1 {
		t.Errorf("logged the unreadable project %d times, want once:\n%s", n, h.log)
	}
	if pushes := h.worker.pushLog(); len(pushes) != 1 || len(pushes[0].request.Projects) != 1 {
		t.Errorf("pushes = %d, want alpha pushed once", len(pushes))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
