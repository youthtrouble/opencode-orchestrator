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
// --clear it removes the gate instead and queues the gated conductor's relaunch, unless
// --no-relaunch (kickoff, which launches the conductor itself and so also drops any queued
// launch or pause). flip.json is read before .orchestrator/ is created.
func (p Project) Gate(opts cliargs.Opts) (*jsjson.Object, error) {
	prev := p.ReadFlip()
	if !jsjson.Truthy(prev) {
		prev = jsjson.NewObject()
	}
	if err := p.makeStateDir(); err != nil {
		return nil, err
	}
	if opts.Truthy("clear") {
		return p.clearGateAnswer(prev, !opts.Truthy("no-relaunch"))
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
			Body:    "Supervisor will not relaunch until cleared: " + cmdref.FlipGateClear() + " (or a reply to the gate's question, or kickoff/flip run).",
			Refs:    []string{stateRef},
		})
	}
	return doorAnswer("awaiting_human", active), nil
}

// clearGateAnswer clears the gate and returns {awaiting_human: false}, plus relaunch: <door>
// when a relaunch was queued.
func (p Project) clearGateAnswer(prev any, relaunch bool) (*jsjson.Object, error) {
	door, queued, err := p.ClearGate(prev, relaunch)
	if err != nil {
		return nil, err
	}
	cleared := jsjson.NewObject()
	cleared.Set("awaiting_human", false)
	if queued {
		cleared.Set("relaunch", door)
	}
	return cleared, nil
}

// ClearGate deletes awaiting_human from prev (nothing to delete on any other shape) and, when
// a gate was set and relaunch is on, makes sure a pending_launch is queued: the supervisor's
// pending-launch branch starts that conductor once the lane is idle and unlocked, and `flip
// run` drops the record, so it fires once. A truthy pending_launch already queued (a flip that
// found the lane locked before the gate) is kept rather than replaced by the gated door: it is
// the handover the conductor asked for, and the supervisor launches it all the same. Otherwise
// it queues {to, from, reason, resume, at} for the gated door (door || "opencode"). With
// relaunch off (kickoff, which launches the conductor itself) an object also loses any
// pending_launch and paused, so the supervisor cannot start an older conductor first, or a
// second one beside an interactive kickoff. prev is written back whatever its shape. It returns the door the supervisor will launch and whether
// there is one (a truthy pending_launch.to).
func (p Project) ClearGate(prev any, relaunch bool) (door any, queued bool, err error) {
	record, isObject := prev.(*jsjson.Object)
	if isObject {
		gate := jsjson.Path(record, "awaiting_human")
		record.Delete("awaiting_human")
		if !relaunch {
			record.Delete("pending_launch")
			record.Delete("paused")
		}
		if jsjson.Truthy(gate) && relaunch {
			if !jsjson.Truthy(jsjson.Path(record, "pending_launch")) {
				record.Set("pending_launch", p.gateRelaunch(gatedDoor(gate)))
			}
			door = jsjson.Path(record, "pending_launch", "to")
			queued = jsjson.Truthy(door)
		}
	}
	if err := p.writeFlip(prev); err != nil {
		return nil, false, err
	}
	return door, queued, nil
}

// gatedDoor is the door a gate stopped, `gate.door || "opencode"`.
func gatedDoor(gate any) any {
	if door := jsjson.Path(gate, "door"); jsjson.Truthy(door) {
		return door
	}
	return flip.DoorOpencode
}

// gateRelaunch is the pending_launch a cleared gate queues: the gated door relaunches itself.
func (p Project) gateRelaunch(door any) *jsjson.Object {
	pending := jsjson.NewObject()
	pending.Set("to", door)
	pending.Set("from", door)
	pending.Set("reason", "human gate cleared")
	pending.Set("resume", nil)
	pending.Set("at", jsjson.ISOString(p.Now()))
	return pending
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
