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
// launch or pause). Every read-modify-write is serialized by Update.
func (p Project) Gate(opts cliargs.Opts) (*jsjson.Object, error) {
	if opts.Truthy("clear") {
		return p.clearGateAnswer(!opts.Truthy("no-relaunch"))
	}
	active := opts.Or("active", flip.DoorOpencode)
	if err := p.Update(func(prev Record) (any, error) { return p.GateRecord(prev.Value, opts), nil }); err != nil {
		return nil, err
	}
	p.PostGateHandoff(opts)
	return doorAnswer("awaiting_human", active), nil
}

// PostGateHandoff notifies the human after a gate transaction has released its lock.
// Mailbox failures are optional, as for the other flip handoffs.
func (p Project) PostGateHandoff(opts cliargs.Opts) {
	active := opts.Or("active", flip.DoorOpencode)
	reason, ok := opts.Or("reason", "approval").(string)
	if !ok {
		reason = "approval"
	}
	p.postIgnoringErrors(mailbox.Draft{
		From: conductor(active), To: "human", Kind: "handoff",
		Subject: "awaiting human: " + jsstr.Slice(reason, 0, subjectReasonUnits),
		Body:    "Supervisor will not relaunch until cleared: " + cmdref.FlipGateClear() + " (or a reply to the gate's question, or kickoff/flip run).",
		Refs:    []string{stateRef},
	})
}

// clearGateAnswer clears the gate and returns {awaiting_human: false}, plus relaunch: <door>
// when a relaunch was queued.
func (p Project) clearGateAnswer(relaunch bool) (*jsjson.Object, error) {
	door, queued, err := p.ClearGate(relaunch)
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

// ClearGate reads and clears the current gate transactionally. Explicit human and kickoff
// clears may remove a gate; callers must never pass a stale pre-read record.
func (p Project) ClearGate(relaunch bool) (door any, queued bool, err error) {
	err = p.Update(func(prev Record) (any, error) {
		next, d, q := p.ClearGateRecord(prev.Value, relaunch)
		door, queued = d, q
		return next, nil
	})
	return
}

// ClearGateRecord transforms state without writing it, for callers already inside Update.
// A queued handover is kept. Kickoff (relaunch false) also removes pauses and queued launches.
func (p Project) ClearGateRecord(prev any, relaunch bool) (next any, door any, queued bool) {
	if !jsjson.Truthy(prev) {
		prev = jsjson.NewObject()
	}
	if record, ok := prev.(*jsjson.Object); ok {
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
	return prev, door, queued
}

// GateRecord transforms state without writing it. Non-object JSON becomes an empty object
// so a hand-edited primitive cannot prevent a human gate (PE-34).
func (p Project) GateRecord(prev any, opts cliargs.Opts) *jsjson.Object {
	record, ok := prev.(*jsjson.Object)
	if !ok {
		record = jsjson.NewObject()
	}
	record.Set("awaiting_human", p.awaitingHuman(opts, opts.Or("active", flip.DoorOpencode)))
	record.Delete("paused")
	return record
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
	reason, ok := opts.Or("reason", "human approval required").(string)
	if !ok {
		reason = "human approval required"
	}
	gate.Set("reason", reason)
	gate.Set("question", opts.Or("question", nil))
	return gate
}
