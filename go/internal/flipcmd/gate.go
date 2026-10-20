package flipcmd

import (
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
)

// subjectReasonUnits is how many UTF-16 units of the reason the human's message subject keeps.
const subjectReasonUnits = 80

// Gate records that the headless conductor stopped at a human gate, which the supervisor
// must not relaunch past until a human clears it, and returns the line to print. With
// --clear it removes the gate instead. flip.json is read before .orchestrator/ is created.
func (p Project) Gate(opts cliargs.Opts) (*jsjson.Object, error) {
	prev := p.ReadFlip()
	if !jsjson.Truthy(prev) {
		prev = jsjson.NewObject()
	}
	if err := p.makeStateDir(); err != nil {
		return nil, err
	}
	if opts.Truthy("clear") {
		return p.clearGate(prev)
	}
	active := opts.Or("active", flip.DoorOpencode)
	if _, isArray := prev.([]any); isArray {
		prev = jsjson.NewObject() // an array would lose the gate when written back as JSON
	}
	if err := markAwaitingHuman(prev, p.awaitingHuman(opts, active)); err != nil {
		return nil, err
	}
	if err := p.writeFlip(prev); err != nil {
		return nil, err
	}
	// `(opts.reason || "approval").slice(0, 80)` throws inside the post's try for a bare
	// --reason (true has no slice), so no message is posted then.
	if reason, isText := opts.Or("reason", "approval").(string); isText {
		p.postIgnoringErrors(mailbox.Draft{
			From:    conductor(active),
			To:      "human",
			Kind:    "handoff",
			Subject: "awaiting human: " + jsstr.Slice(reason, 0, subjectReasonUnits),
			Body:    "Supervisor will not relaunch until cleared: " + cmdref.FlipGateClear() + " (or kickoff/flip run).",
			Refs:    []string{stateRef},
		})
	}
	return doorAnswer("awaiting_human", active), nil
}

// clearGate deletes awaiting_human (nothing to delete on any other shape) and writes prev
// back as it is, whatever its shape.
func (p Project) clearGate(prev any) (*jsjson.Object, error) {
	if record, isObject := prev.(*jsjson.Object); isObject {
		record.Delete("awaiting_human")
	}
	if err := p.writeFlip(prev); err != nil {
		return nil, err
	}
	cleared := jsjson.NewObject()
	cleared.Set("awaiting_human", false)
	return cleared, nil
}

// awaitingHuman is the gate record {at, door, reason, question}.
func (p Project) awaitingHuman(opts cliargs.Opts, active any) *jsjson.Object {
	gate := jsjson.NewObject()
	gate.Set("at", jsjson.ISOString(p.Now()))
	gate.Set("door", active)
	gate.Set("reason", opts.Or("reason", "human approval required"))
	gate.Set("question", opts.Or("question", nil))
	return gate
}

// markAwaitingHuman is `prev.awaiting_human = gate; delete prev.paused` on a truthy non-array
// prev: an object takes the gate (an existing key keeps its position) and loses paused; a
// primitive throws.
func markAwaitingHuman(prev any, gate *jsjson.Object) error {
	switch record := prev.(type) {
	case *jsjson.Object:
		record.Set("awaiting_human", gate)
		record.Delete("paused")
	default:
		return jsjson.CannotAssign(prev, "awaiting_human")
	}
	return nil
}
