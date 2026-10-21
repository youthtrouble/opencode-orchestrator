// Package snapshot builds what orch remote pushes for a project, purely from the project's
// local .orchestrator/ state (flip.json, the conductor lane, the session heartbeats and the
// mailbox) and the cached usage reading. It reads and never writes.
//
// Privacy (plan D11): free text is redacted and cut to the schema caps; events carry only
// headlines; only open questions to the human carry a body; paths are never sent, a project is
// identified by an opaque key and its directory name.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

// How many questions and events a snapshot carries, the schema's maxItems.
const (
	maxQuestions = 20
	maxEvents    = 10
)

// MaxCIRuns is how many CI runs a snapshot carries, the schema's maxItems.
const MaxCIRuns = 5

// CIProvider gives a project's latest CI runs, newest first, with schema-valid status,
// conclusion and at. Build still redacts and caps what it gets.
type CIProvider interface {
	LatestRuns(project discover.Project) []wire.CiRun
}

// NoCI provides no runs: CI reporting is off.
type NoCI struct{}

// LatestRuns is always empty.
func (NoCI) LatestRuns(discover.Project) []wire.CiRun { return nil }

// Env is what Build needs besides the project. Tests fix every field.
type Env struct {
	MachineID string
	Home      string             // redacted out of free text
	Now       time.Time          // the clock that ages heartbeats
	Alive     func(pid any) bool // whether a lane's pid is a live process (worker.Alive)
	CI        CIProvider         // the project's latest CI runs; nil is NoCI
}

// ciProvider is env's CI provider, NoCI when none is set.
func (env Env) ciProvider() CIProvider {
	if env.CI == nil {
		return NoCI{}
	}
	return env.CI
}

// Build reads project's state and returns its snapshot. Unreadable state is an *InputError.
func Build(project discover.Project, env Env) (wire.ProjectSnapshot, error) {
	in, err := readInputs(project.Root, env.Alive)
	if err != nil {
		return wire.ProjectSnapshot{}, err
	}
	return assemble(project, in, env)
}

func assemble(project discover.Project, in inputs, env Env) (wire.ProjectSnapshot, error) {
	clean := cleaner{home: env.Home}
	questions, blocking := openQuestions(in.messages, clean)
	state, reason := deriveState(in, blocking, env.Now)
	snapshot := wire.ProjectSnapshot{
		Key:         Key(env.MachineID, project.RealPath),
		Name:        clean.text(project.Name, maxNameUnits),
		State:       state,
		StateReason: clean.text(reason, maxReasonUnits),
		Door:        doorOf(in),
		Run:         runOf(in, env.Now),
		CI:          ciRuns(env.ciProvider().LatestRuns(project), clean),
		Events:      recentEvents(in.messages, clean),
		Questions:   questions,
	}
	if beat := in.session; beat != nil {
		snapshot.Slice = clean.optional(beat.slice, maxSliceUnits)
		snapshot.Now = clean.optional(beat.now, maxNowUnits)
		snapshot.Progress = beat.progress
	}
	hash, err := hashOf(snapshot)
	if err != nil {
		return wire.ProjectSnapshot{}, err
	}
	snapshot.SnapshotHash = hash
	return snapshot, nil
}

// Key is the project's opaque identity on the Worker: the first 32 hex digits of
// sha256("work-trains/v1\0" + machineID + "\0" + realPath). It is stable per machine and
// path and reveals neither.
func Key(machineID, realPath string) string {
	sum := sha256.Sum256([]byte("work-trains/v1\x00" + machineID + "\x00" + realPath))
	return hex.EncodeToString(sum[:])[:32]
}

// hashOf is the sha256 of the snapshot's JSON with an empty snapshot_hash: an opaque change
// token the Worker compares, never recomputes. A snapshot holds only strings, ints, bools and
// structs of them, so Marshal should not fail; if it does, the json error is wrapped as is.
func hashOf(snapshot wire.ProjectSnapshot) (string, error) {
	snapshot.SnapshotHash = ""
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("snapshot hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// openQuestions are the newest questions to the human (or everyone) that the human has not
// acked, newest first, capped at maxQuestions, and how many of ALL of them are blocking: an
// old blocking question still makes the project need you when newer ones fill the list. A
// question posted before the recommended/blocking contract has no recommendation and never
// blocks.
func openQuestions(messages []message, clean cleaner) (questions []wire.Question, blocking int) {
	questions = []wire.Question{}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.kind != "question" || (msg.to != "human" && msg.to != "*") || msg.ackedByHuman {
			continue
		}
		question := questionOf(msg, clean)
		if question.Blocking {
			blocking++
		}
		if len(questions) < maxQuestions {
			questions = append(questions, question)
		}
	}
	return questions, blocking
}

func questionOf(msg message, clean cleaner) wire.Question {
	subject := clean.text(msg.subject, maxSubjectUnits)
	if subject == "" {
		subject = "(no subject)" // the schema requires one; hand-edited lines may lack it
	}
	var recommended *string
	if msg.recommended != nil {
		recommended = clean.optional(*msg.recommended, maxRecommendedUnits)
	}
	return wire.Question{
		ID:          msg.id,
		At:          iso(msg.at),
		From:        clean.text(msg.from, maxFromUnits),
		Subject:     subject,
		Body:        clean.text(msg.body, maxBodyUnits),
		Recommended: recommended,
		Blocking:    recommended != nil && msg.blocking,
	}
}

// ciRuns prepares the provider's runs for the wire: names redacted and cut, a url kept only
// when it is a github.com link that redaction leaves intact (a cut or rewritten link is useless),
// and a run whose repo cleans to nothing left out.
func ciRuns(runs []wire.CiRun, clean cleaner) []wire.CiRun {
	sendable := []wire.CiRun{}
	for _, run := range runs {
		if len(sendable) == MaxCIRuns {
			break
		}
		repo := clean.text(run.Repo, maxRepoUnits)
		if repo == "" {
			continue
		}
		sendable = append(sendable, wire.CiRun{
			Repo:       repo,
			Workflow:   clean.text(run.Workflow, maxWorkflowUnits),
			Status:     run.Status,
			Conclusion: run.Conclusion,
			URL:        clean.link(run.URL, maxURLUnits),
			At:         run.At,
		})
	}
	return sendable
}

// recentEvents are the newest messages of any kind, newest first, as headlines: never a body.
func recentEvents(messages []message, clean cleaner) []wire.Event {
	events := []wire.Event{}
	for i := len(messages) - 1; i >= 0 && len(events) < maxEvents; i-- {
		msg := messages[i]
		events = append(events, wire.Event{
			ID:      msg.id,
			At:      iso(msg.at),
			Kind:    clean.text(msg.kind, maxKindUnits),
			From:    clean.text(msg.from, maxFromUnits),
			Subject: clean.text(msg.subject, maxEventUnits),
		})
	}
	return events
}

// NewPushRequest is the push body for these snapshots, sent by machineID running orchVersion
// at sentAt.
func NewPushRequest(machineID, orchVersion string, sentAt time.Time, budget *wire.Budget, projects []wire.ProjectSnapshot) wire.PushRequest {
	if projects == nil {
		projects = []wire.ProjectSnapshot{}
	}
	return wire.PushRequest{
		V:        wire.ProtocolVersion,
		Machine:  wire.Machine{ID: machineID, OrchVersion: orchVersion, SentAt: iso(sentAt)},
		Budget:   budget,
		Projects: projects,
	}
}
