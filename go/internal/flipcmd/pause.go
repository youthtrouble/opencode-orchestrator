package flipcmd

import (
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
)

// Pause records that the conductor stopped because both vendors are red, so the supervisor
// relaunches a door once a window is usable again, and returns the line to print. It creates
// .orchestrator/, then rewrites flip.json as {...previous, paused: {at, door, reason[, brief]}}
// (brief is a non-empty --brief, the brief `flip run` relaunches from; a both-red kickoff
// passes its own): every other key is kept, an existing paused keeps its position, a string previous value spreads
// into index keys and a number or true into nothing. It posts a handoff to the supervisor.
// Unreadable state is a StateError; other errors are the transaction or filesystem failure.
func (p Project) Pause(opts cliargs.Opts) (*jsjson.Object, error) {
	active := opts.Or("active", flip.DoorOpencode)
	err := p.Update(func(prev Record) (any, error) {
		record := jsjson.Spread(prev.Value)
		paused := jsjson.NewObject()
		paused.Set("at", jsjson.ISOString(p.Now()))
		paused.Set("door", active)
		paused.Set("reason", opts.Or("reason", "both vendors red"))
		if brief, isText := opts.JS("brief").(string); isText && brief != "" {
			paused.Set("brief", brief)
		}
		record.Set("paused", paused)
		return record, nil
	})
	if err != nil {
		return nil, err
	}
	p.postIgnoringErrors(mailbox.Draft{
		From:    conductor(active),
		To:      "supervisor",
		Kind:    "handoff",
		Subject: "conductor paused: both vendors red",
		Body:    "Supervisor will relaunch when a window resets.",
		Refs:    []string{stateRef},
	})
	return doorAnswer("paused", active), nil
}
