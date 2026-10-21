// Package apply carries out on the Mac what the owner did on the Work Trains page: an answer
// becomes the human's mailbox reply, a pause becomes a human gate, a resume clears it. Each goes
// through the path a human at the terminal uses (reply.Post, flipcmd.Project.Gate and
// ClearGate), is re-validated against the project's current state first and is never applied
// twice. Each yields an Outcome for the Worker's ack.
package apply

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
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

// maxDetailUnits is the ack schema's cap on detail, in UTF-16 code units.
const maxDetailUnits = 200

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

// ProjectResolver maps a Work Trains project key to the project's root, ok false when no
// discovered project has that key. Discovery (WT12) provides it.
type ProjectResolver func(key string) (root string, ok bool)

// Applier applies pulled answers and actions. Home is the home directory redacted from error
// details; RegistryFile is models.json, read for the home door. Alive, StartSupervisor, Now
// and NewID are the process probe, the supervisor launcher, the clock and the message id
// source; tests replace them.
type Applier struct {
	Projects        ProjectResolver
	Home            string
	RegistryFile    string
	Alive           func(pid any) bool
	StartSupervisor func(root string) error
	Now             func() time.Time
	NewID           func() string
}

// New is an Applier with the real process probe, supervisor launcher, clock and ids.
func New(projects ProjectResolver, home, registryFile string) Applier {
	return Applier{
		Projects:        projects,
		Home:            home,
		RegistryFile:    registryFile,
		Alive:           worker.Alive,
		StartSupervisor: StartSupervisorBySelfExec,
		Now:             time.Now,
		NewID:           mailbox.NewUUID,
	}
}

// ApplyAnswer posts a pulled answer as the human's reply to its question. An answer already
// in the mailbox (a retried pull after a crash or a failed ack) is not posted again: only what
// the reply still lacks, the human's ack and clearing the gate, is done, and it is applied. A
// question gone, answered on the Mac or no longer a question to the human is not answered. A
// failed step is rejected with its error, which is also returned; a reply whose human gate
// could not be checked or cleared is applied and the gate's error returned.
func (a Applier) ApplyAnswer(answer wire.PulledAnswer) (Outcome, error) {
	root, ok := a.Projects(answer.ProjectKey)
	if !ok {
		return Outcome{Rejected, "unknown project"}, nil
	}
	box := a.mailbox(root)
	msgs, err := box.List(mailbox.Query{})
	if err != nil {
		return a.rejected(root, &Error{"read the mailbox", err})
	}
	remote := &mailbox.RemoteRef{AnswerID: answer.ID, Kind: answer.Kind}
	if alreadyApplied(msgs, answer) {
		result, err := reply.Finish(box, a.flipProject(root), answer.QuestionID, remote, !awaitsHumanAck(msgs, answer.QuestionID))
		if err != nil {
			return a.rejected(root, &Error{"finish the reply", err})
		}
		return answerOutcome("already applied", result)
	}
	if outcome, open := questionState(msgs, answer.QuestionID); !open {
		return outcome, nil
	}
	result, err := reply.Post(box, a.flipProject(root), reply.Request{ID: answer.QuestionID, Body: answer.Text, Remote: remote})
	if err != nil {
		return a.rejected(root, &Error{"reply", err})
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

// ApplyAction applies a pause or a resume to its project.
func (a Applier) ApplyAction(action wire.PulledAction) (Outcome, error) {
	root, ok := a.Projects(action.ProjectKey)
	if !ok {
		return Outcome{Rejected, "unknown project"}, nil
	}
	switch action.Kind {
	case "pause":
		return a.pause(root)
	case "resume":
		return a.resume(root)
	}
	return Outcome{Rejected, "unknown action"}, nil
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
			return a.rejected(root, err)
		}
	}
	gate := cliargs.Opts{"active": {Value: door}, "reason": {Value: pauseReason}}
	if _, err := project.Gate(gate); err != nil {
		return a.rejected(root, &Error{"pause", err})
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
		return "", &Error{"read the registry", err}
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
// what relaunches it, so one is started first when none runs: while the gate is set a
// supervisor launches nothing, and a start that fails leaves the gate set, so a retried resume
// still finds it paused and tries again rather than stranding the queued relaunch.
func (a Applier) resume(root string) (Outcome, error) {
	project := a.flipProject(root)
	record := project.ReadFlip()
	if !jsjson.Truthy(jsjson.Path(record, "awaiting_human")) {
		return Outcome{Superseded, "not paused"}, nil
	}
	if !a.supervisorRunning(root) {
		if err := a.StartSupervisor(root); err != nil {
			// Its output can name any local path; the page gets the step, the log the error.
			return Outcome{Rejected, "could not start the supervisor"}, &Error{"start the supervisor", err}
		}
	}
	door, _, err := project.ClearGate(record, true)
	if err != nil {
		return a.rejected(root, &Error{"resume", err})
	}
	return Outcome{Applied, fmt.Sprintf("resuming: %s conductor relaunches within a minute", jsjson.TemplateString(door))}, nil
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

// projectPlaceholder stands for the project root in a detail sent to the page.
const projectPlaceholder = "<project>"

// rejected is the outcome of a failed step in the project at root: its error with the root and
// the home directory redacted (it may name local paths) and cut to the ack's cap, and the error
// itself for the caller's log.
func (a Applier) rejected(root string, err error) (Outcome, error) {
	detail := incident.Redact(a.Home, strings.ReplaceAll(err.Error(), root, projectPlaceholder))
	return Outcome{Rejected, cutUTF16(detail, maxDetailUnits)}, err
}

// cutUTF16 keeps at most n UTF-16 code units of s, never half a surrogate pair.
func cutUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		units += utf16.RuneLen(r)
		if units > n {
			return s[:i]
		}
	}
	return s
}
