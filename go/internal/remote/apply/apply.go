// Package apply carries out on the Mac what the owner did on the Work Trains page: an answer
// becomes the human's mailbox reply, a pause becomes a human gate, a resume clears it. Each goes
// through the path a human at the terminal uses (reply.Post, flipcmd.Project.Gate and
// ClearGate), is re-validated against the project's current state first and is never applied
// twice: an answer is known by the remote id its reply carries, an action by the project's
// action journal, and either one whose project could not be resolved by the unresolved journal.
//
// Each yields an Outcome for the Worker's ack, final once returned: the caller acks it whatever
// error accompanies it, since the receipt that keeps a replay from acting again is durable. The
// exception is a *RetryableError, returned with the zero Outcome when a receipt could not be
// read or written: no outcome exists yet, so the caller must not ack it and lets the Worker send
// it again.
package apply

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/reply"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/supervisor"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

// Result is the outcome kind the Worker records for an answer or an action.
type Result string

const (
	Applied    Result = "applied"
	Superseded Result = "superseded"
	Rejected   Result = "rejected"
)

// Outcome is what applying one answer or action did, with a short detail for the page.
type Outcome struct {
	Result Result
	Detail string
}

// pauseReason is the gate reason of a pause from the page.
const pauseReason = "paused from Work Trains"

// conductorLane is the worker lane a headless conductor runs in.
const conductorLane = "conductor"

// Error is the step of an apply that failed. Writes made by earlier steps stay.
type Error struct {
	Step string
	Err  error
}

func (e *Error) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// RetryableError is a journal or mailbox that could not be read, or a receipt that could not be
// written. It comes with the zero Outcome and must not be acked: acking a rejection with no
// receipt behind it would let a replay (its ack lost) apply once storage or discovery recovers,
// overriding whatever the Mac decided since. Nothing was applied, except an answer's reply: it
// may be posted with its ack or gate still undone, and the reply, which carries the answer's
// id, is the receipt by which the resend finishes it rather than posting it again.
type RetryableError struct {
	Step string
	Err  error
}

func (e *RetryableError) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// ErrSupervisorNotLive is a supervisor start that reported success while no live supervisor
// came to hold supervisor.lock within supervisorReadyPolls polls: `supervisor start` exits 0
// even when its detached serve cannot be launched, and serve can die before claiming the lock.
var ErrSupervisorNotLive = errors.New("no live supervisor holds supervisor.lock after starting one")

// ProjectResolver maps a Work Trains project key to the project's root, ok false when no
// discovered project has that key. Discovery (WT12) provides it.
type ProjectResolver func(key string) (root string, ok bool)

// Applier applies pulled answers and actions. RegistryFile is models.json, read for the home
// door. StateDir is orch remote's state directory (state.Dir), which holds the unresolved
// journal. Alive, StartSupervisor, Sleep, Now and NewID are the process probe, the supervisor
// launcher, the wait between supervisor readiness polls, the clock and the message id source;
// tests replace them.
type Applier struct {
	Projects        ProjectResolver
	RegistryFile    string
	StateDir        string
	Alive           func(pid any) bool
	StartSupervisor func(root string) error
	Sleep           func(time.Duration)
	Now             func() time.Time
	NewID           func() string
}

// New is an Applier with the real process probe, supervisor launcher, sleep, clock and ids.
func New(projects ProjectResolver, registryFile, stateDir string) Applier {
	return Applier{
		Projects:        projects,
		RegistryFile:    registryFile,
		StateDir:        stateDir,
		Alive:           worker.Alive,
		StartSupervisor: StartSupervisorBySelfExec,
		Sleep:           time.Sleep,
		Now:             time.Now,
		NewID:           mailbox.NewUUID,
	}
}

// ApplyAnswer posts a pulled answer as the human's reply to its question. An answer already
// in the mailbox (a retried pull after a crash or a failed ack) is not posted again: only what
// the reply still lacks, the human's ack and clearing the gate, is done, and it is applied. A
// question gone, answered on the Mac or no longer a question to the human is not answered. An
// answer whose project is unknown is rejected, and stays rejected on replay (see resolve). A
// mailbox that cannot be read, a reply that cannot be posted and an ack that cannot be written
// are a *RetryableError with no outcome (see RetryableError); a reply whose human gate could not
// be checked or cleared is applied and the gate's error returned.
func (a Applier) ApplyAnswer(answer wire.PulledAnswer) (Outcome, error) {
	root, ok, outcome, err := a.resolve(journalKey{answer.ProjectKey, answerKind, answer.ID})
	if !ok {
		return outcome, err
	}
	box := a.mailbox(root)
	msgs, err := box.List(mailbox.Query{})
	if err != nil {
		return retryable("read the mailbox", err)
	}
	remote := &mailbox.RemoteRef{AnswerID: answer.ID, Kind: answer.Kind}
	if alreadyApplied(msgs, answer) {
		result, err := reply.Finish(box, a.flipProject(root), answer.QuestionID, remote, !awaitsHumanAck(msgs, answer.QuestionID))
		if err != nil {
			return retryable("finish the reply", err)
		}
		return answerOutcome("already applied", result)
	}
	if outcome, open := questionState(msgs, answer.QuestionID); !open {
		return outcome, nil
	}
	result, err := reply.Post(box, a.flipProject(root), reply.Request{ID: answer.QuestionID, Body: answer.Text, Remote: remote})
	if err != nil {
		return retryable("reply", err)
	}
	return answerOutcome("replied", result)
}

// alreadyApplied reports whether the mailbox holds this answer's reply: a message threaded on
// the question that carries the answer's id.
func alreadyApplied(msgs []*jsjson.Object, answer wire.PulledAnswer) bool {
	for _, m := range msgs {
		if jsjson.Path(m, "thread") == answer.QuestionID && jsjson.Path(m, "remote", "answer_id") == answer.ID {
			return true
		}
	}
	return false
}

// questionState is whether the question is still open to the human's answer, and the outcome
// when it is not.
func questionState(msgs []*jsjson.Object, questionID string) (Outcome, bool) {
	for _, m := range msgs {
		if jsjson.Path(m, "id") != questionID {
			continue
		}
		to := jsjson.Path(m, "to")
		if jsjson.Path(m, "kind") != "question" || (to != "human" && to != "*") {
			return Outcome{Rejected, "not a question"}, false
		}
		if ackedByHuman(m) {
			return Outcome{Superseded, "answered on the Mac"}, false
		}
		return Outcome{}, true
	}
	return Outcome{Rejected, "question not found"}, false
}

// awaitsHumanAck is whether the message questionID is in the mailbox and the human has not
// acknowledged it. A message gone has nothing left to ack.
func awaitsHumanAck(msgs []*jsjson.Object, questionID string) bool {
	for _, m := range msgs {
		if jsjson.Path(m, "id") == questionID {
			return !ackedByHuman(m)
		}
	}
	return false
}

func ackedByHuman(m *jsjson.Object) bool {
	ackedBy, _ := jsjson.Path(m, "acked_by").([]any)
	return slices.Contains(ackedBy, any("human"))
}

// answerOutcome is applied, with the detail what (replied, or already applied) and what the
// reply did to the human gate: the reply stands whatever happened to it. A gate that answered
// but could not be cleared is named in the detail and returned for the log.
func answerOutcome(what string, result reply.Result) (Outcome, error) {
	switch result.Gate {
	case reply.GateCleared:
		return Outcome{Applied, fmt.Sprintf("%s; cleared the human gate, the %s conductor relaunches within a minute", what, jsjson.TemplateString(result.Door))}, nil
	case reply.GateClearedWithoutRelaunch:
		return Outcome{Applied, what + "; cleared the human gate"}, nil
	case reply.GateUntouched:
		return Outcome{Applied, what}, nil
	}
	return Outcome{Applied, what + "; could not check the human gate"}, &Error{"check the human gate", result.GateErr}
}

// ApplyAction applies a pause or a resume to its project at most once. The project's action
// journal records it before flip.json is touched and its outcome after, so a replay (its ack
// lost) resends the recorded outcome and never undoes a pause or resume made on the Mac since.
// An action found recorded but not settled was interrupted mid-apply and is never applied
// again; see interruptedOutcome. A journal that cannot be read, or an action that cannot be
// recorded in flight, is a *RetryableError with no outcome; once the in-flight record is
// durable every outcome is final, even when recording it fails (see settle).
func (a Applier) ApplyAction(action wire.PulledAction) (Outcome, error) {
	if action.Kind != "pause" && action.Kind != "resume" {
		return Outcome{Rejected, "unknown action"}, nil
	}
	root, ok, outcome, err := a.resolve(journalKey{action.ProjectKey, action.Kind, action.ID})
	if !ok {
		return outcome, err
	}
	journal := newActionJournal(root)
	key := journalKey{Kind: action.Kind, ID: action.ID}
	entry, found, err := journal.lookup(key)
	switch {
	case err != nil:
		return retryable("read the action journal", err)
	case found && entry.settled():
		return entry.outcome(), nil
	case found:
		return settle(journal, entry, a.interruptedOutcome(root, action.Kind), nil)
	}
	entry = journalEntry{journalKey: key}
	if err := journal.record(entry); err != nil {
		return retryable("record the action", err)
	}
	outcome, applyErr := a.applyKind(root, action.Kind)
	return settle(journal, entry, outcome, applyErr)
}

// resolve is the root of key's project, ok false when there is none to apply to: then outcome
// is key's rejection, the one the unresolved journal holds or else a new "unknown project"
// that it records. The journal is consulted first, so a rejection is resent even after
// discovery has since found the project. A journal that cannot be read or a rejection that
// cannot be recorded is a *RetryableError with no outcome: the id is not rejected until its
// rejection is durable.
func (a Applier) resolve(key journalKey) (root string, ok bool, outcome Outcome, err error) {
	unresolved := newUnresolvedJournal(a.StateDir)
	entry, found, err := unresolved.lookup(key)
	if err != nil {
		outcome, err = retryable("read the unresolved journal", err)
		return "", false, outcome, err
	}
	if found {
		return "", false, entry.outcome(), nil
	}
	if root, ok = a.Projects(key.ProjectKey); ok {
		return root, true, Outcome{}, nil
	}
	outcome = Outcome{Rejected, "unknown project"}
	if err := unresolved.record(journalEntry{key, outcome.Result, outcome.Detail}); err != nil {
		outcome, err = retryable("record the rejection", err)
		return "", false, outcome, err
	}
	return "", false, outcome, nil
}

func (a Applier) applyKind(root, kind string) (Outcome, error) {
	if kind == "pause" {
		return a.pause(root)
	}
	return a.resume(root)
}

// interruptedOutcome is the outcome of an action recorded in flight but never settled: the
// process stopped somewhere between recording it and recording its outcome. flip.json cannot
// tell whether the action wrote it (a local resume can restore the very bytes a pause
// replaced), so the action is not applied again: it is applied when the project is now as it
// asked, else superseded, and the owner sends it again as a new action. An action interrupted
// before its write is therefore dropped rather than risk overriding a newer local decision.
func (a Applier) interruptedOutcome(root, kind string) Outcome {
	paused := jsjson.Truthy(jsjson.Path(a.flipProject(root).ReadFlip(), "awaiting_human"))
	if paused == (kind == "pause") {
		return Outcome{Applied, "already applied"}
	}
	return Outcome{Superseded, "interrupted; send it again"}
}

// settle records entry's outcome in the journal and returns it. An outcome that cannot be
// recorded still stands and is final; the failure joins applyErr for the log. The entry stays
// in flight, so a replay (its ack lost) is never applied again: it gets interruptedOutcome.
func settle(journal actionJournal, entry journalEntry, outcome Outcome, applyErr error) (Outcome, error) {
	entry.Result, entry.Detail = outcome.Result, outcome.Detail
	if err := journal.record(entry); err != nil {
		return outcome, errors.Join(applyErr, &Error{"record the outcome", err})
	}
	return outcome, applyErr
}

// pause sets the human gate for the conductor's door, so the conductor stops at its next brief
// boundary (flip decide answers gate) and the supervisor does not relaunch it.
func (a Applier) pause(root string) (Outcome, error) {
	project := a.flipProject(root)
	record := project.ReadFlip()
	if jsjson.Truthy(jsjson.Path(record, "awaiting_human")) {
		return Outcome{Superseded, "already paused"}, nil
	}
	door, running := a.runningConductor(root)
	if !isDoor(door) {
		var err error
		if door, err = a.fallbackDoor(record); err != nil {
			return rejected("pause: could not read the registry", "read the registry", err)
		}
	}
	gate := cliargs.Opts{"active": {Value: door}, "reason": {Value: pauseReason}}
	if _, err := project.Gate(gate); err != nil {
		return rejected("pause: could not write the gate", "pause", err)
	}
	if !running {
		return Outcome{Applied, "pauses at the next brief boundary; no conductor running"}, nil
	}
	return Outcome{Applied, "pauses at the next brief boundary"}, nil
}

// runningConductor is whether the conductor lane's process is alive and, when it is, its
// harness (the door it runs, unless the lane was started with another harness).
func (a Applier) runningConductor(root string) (harness string, running bool) {
	lane := worker.NewProject(root).ReadState(conductorLane)
	status := jsjson.Path(lane, "status")
	if (status != "running" && status != "starting") || !a.Alive(jsjson.Path(lane, "pid")) {
		return "", false
	}
	harness, _ = jsjson.Path(lane, "harness").(string)
	return harness, true
}

// fallbackDoor is the door to gate when no running conductor names one: the last flip's door,
// else the home door of models.json (`conductor.home`, else opencode). The home-door rule is
// also kept by kickoff.homeDoor and flip.conductorConfigOf; a change to it must change all three.
func (a Applier) fallbackDoor(record any) (string, error) {
	if to, _ := jsjson.Path(record, "to").(string); isDoor(to) {
		return to, nil
	}
	reg, err := registry.Load(a.RegistryFile)
	if err != nil {
		return "", err
	}
	if home, _ := jsjson.Path(reg, "conductor", "home").(string); home != "" {
		return home, nil
	}
	return flip.DoorOpencode, nil
}

func isDoor(name string) bool {
	return name == flip.DoorOpencode || name == flip.DoorClaude
}

// resume clears the human gate and queues the gated conductor's relaunch. The supervisor is
// what relaunches it, so a live one must hold supervisor.lock first: while the gate is set a
// supervisor launches nothing, and a start that fails or never yields a live supervisor leaves
// the gate set, so the owner's next resume still finds it paused and tries again rather than
// stranding the queued relaunch.
func (a Applier) resume(root string) (Outcome, error) {
	project := a.flipProject(root)
	record := project.ReadFlip()
	if !jsjson.Truthy(jsjson.Path(record, "awaiting_human")) {
		return Outcome{Superseded, "not paused"}, nil
	}
	if err := a.ensureSupervisor(root); err != nil {
		return rejected("could not start the supervisor", "start the supervisor", err)
	}
	door, _, err := project.ClearGate(record, true)
	if err != nil {
		return rejected("resume: could not clear the gate", "resume", err)
	}
	return Outcome{Applied, fmt.Sprintf("resuming: %s conductor relaunches within a minute", jsjson.TemplateString(door))}, nil
}

// supervisorReadyPolls and supervisorReadyInterval bound the wait for a started supervisor to
// claim its lock: up to about 5 seconds, checked every 100 ms.
const (
	supervisorReadyPolls    = 50
	supervisorReadyInterval = 100 * time.Millisecond
)

// ensureSupervisor returns once a live supervisor holds supervisor.lock, starting one when
// none does. ErrSupervisorNotLive is a start that succeeded without one appearing in time.
func (a Applier) ensureSupervisor(root string) error {
	if a.supervisorRunning(root) {
		return nil
	}
	if err := a.StartSupervisor(root); err != nil {
		return err
	}
	for range supervisorReadyPolls {
		a.Sleep(supervisorReadyInterval)
		if a.supervisorRunning(root) {
			return nil
		}
	}
	return ErrSupervisorNotLive
}

// supervisorRunning reads the pid the serving supervisor holds in supervisor.lock, as
// `supervisor start` does before it launches another.
func (a Applier) supervisorRunning(root string) bool {
	data, err := os.ReadFile(supervisor.NewProject(root).LockPath)
	if err != nil {
		return false
	}
	return a.Alive(jsjson.ToNumber(string(data)))
}

// StartSupervisorBySelfExec runs `orch supervisor start --cwd <root>` from this executable,
// as kickoff's preflight does.
func StartSupervisorBySelfExec(root string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return startSupervisor(self, root)
}

// supervisorStartGrace bounds the wait for the starter's pipes once it has exited; the detached
// supervisor does not hold them.
const supervisorStartGrace = 10 * time.Second

func startSupervisor(self, root string) error {
	result := procs.RunSync(procs.SyncSpec{Name: self, Args: []string{"supervisor", "start", "--cwd", root}, Grace: supervisorStartGrace})
	switch {
	case result.Err != nil:
		return result.Err
	case result.Status == nil || *result.Status != 0:
		return &SupervisorStartError{Output: result.Stderr + result.Stdout}
	}
	return nil
}

// SupervisorStartError is `orch supervisor start` exiting other than 0, with what it printed.
type SupervisorStartError struct {
	Output string
}

func (e *SupervisorStartError) Error() string { return "supervisor start failed: " + e.Output }

func (a Applier) mailbox(root string) *mailbox.Mailbox {
	box := mailbox.New(root)
	box.Now, box.NewID = a.Now, a.NewID
	return box
}

func (a Applier) flipProject(root string) flipcmd.Project {
	project := flipcmd.NewProject(root)
	project.Now, project.NewID = a.Now, a.NewID
	return project
}

// rejected is the outcome of the step that failed: a fixed detail for the page, since an
// error's text can name any local path, and the full error for the caller's log.
func rejected(detail, step string, err error) (Outcome, error) {
	return Outcome{Rejected, detail}, &Error{step, err}
}

// retryable is the zero Outcome and the step that failed before an outcome was durable.
func retryable(step string, err error) (Outcome, error) {
	return Outcome{}, &RetryableError{step, err}
}
