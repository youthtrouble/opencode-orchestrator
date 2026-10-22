package supervisor

import (
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// Lines the commands print.
const (
	stopRequested = "stop requested; workers are unaffected"
	acknowledged  = "acknowledged (receipt, not acceptance)"
	unknownEvent  = "unknown event id"
)

// Status is `status`: the state file's JSON value as it is (null, a number, an array
// included), or {status: "not_started"} when it cannot be read or parsed. It never writes.
func (p Project) Status() any {
	return readJSON(p.StatePath, func() any {
		notStarted := jsjson.NewObject()
		notStarted.Set("status", "not_started")
		return notStarted
	})
}

// Stop is `stop`: create .orchestrator/ and an empty stop file, which the serving supervisor
// sees at its next loop. It does not check whether a supervisor runs.
func (p Project) Stop() (string, error) {
	if err := nodefs.MkdirAll(p.Dir); err != nil {
		return "", err
	}
	if err := nodefs.WriteFile(p.StopPath, nil); err != nil {
		return "", err
	}
	return stopRequested, nil
}

// AckResult is what `ack` prints: Line on stdout with exit 0, or on stderr with exit 1 when
// Refused.
type AckResult struct {
	Line    string
	Refused bool
}

// Ack is `ack --event ID [--by NAME]`: record that the coordinator received the event, as
// {by, at} under the event id in the acks file. event is the JS option value (nil when
// absent, true when bare) and by is `opts.by || "coordinator"`. The event must be truthy in
// the state's own events (PE-39). The acks read-modify-write is serialized across
// processes and goroutines on a separate, persistent lock file.
func (p Project) Ack(event, by any, now time.Time) (AckResult, error) {
	if !jsjson.Truthy(event) {
		return AckResult{unknownEvent, true}, nil
	}
	id := jsjson.TemplateString(event)
	state := readJSON(p.StatePath, emptyObject)
	if state == nil {
		return AckResult{}, jsjson.CannotReadOfNull("events")
	}
	if !hasTruthyProperty(jsjson.Path(state, "events"), id) {
		return AckResult{unknownEvent, true}, nil
	}
	err := ownlock.Transaction(p.AcksPath+".lock", func() error {
		acks := asObject(readJSON(p.AcksPath, emptyObject))
		record, isObject := acks.(*jsjson.Object)
		if !isObject {
			return jsjson.CannotAssign(acks, id)
		}
		ack := jsjson.NewObject()
		ack.Set("by", by)
		ack.Set("at", jsjson.ISOString(now))
		setProperty(record, id, ack)
		return writeJSON(p.AcksPath, record)
	})
	if err != nil {
		return AckResult{}, err
	}
	return AckResult{Line: acknowledged}, nil
}

// hasTruthyProperty accepts only truthy own event keys (PE-39). A value that is
// not an object reads as {} (PE-16).
func hasTruthyProperty(events any, key string) bool {
	if object, isObject := events.(*jsjson.Object); isObject {
		if value, own := object.Get(key); own {
			return jsjson.Truthy(value)
		}
	}
	return false
}

// setProperty is `object[key] = value` for an object value. Assigning __proto__ without an
// own key of that name calls Object.prototype's setter, which replaces the prototype and adds
// no key (probed with Node v26.5.1: the file is rewritten unchanged).
func setProperty(object *jsjson.Object, key string, value *jsjson.Object) {
	if _, own := object.Get(key); key == "__proto__" && !own {
		return
	}
	object.Set(key, value)
}
