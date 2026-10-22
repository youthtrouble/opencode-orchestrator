// Package supervise is the pure supervision policy of lib/supervise.mjs: classify a worker
// lane and decide whether to wake the coordinator, with the clock injected (epoch seconds).
//
// It carries the O-01 fixes of the JS:
//   - F1: a definite delivery failure does not suppress later checks.
//   - F2: two-phase intent (intent_persisted -> cli_invoked -> outcome), so a crash before
//     the CLI call is retried.
//   - F4: quiet workers re-alert with back-off instead of exactly once.
//   - F8: events are pruned.
//
// Worker states, events and acks are JSON documents shared with the JS supervisor, so they
// are read as jsjson values with JS semantics.
package supervise

import (
	"math"
	"strconv"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// cadence is the check interval in seconds per worker breadth.
var cadence = map[string]int64{"focused": 120, "standard": 300, "broad": 600}

const defaultBreadth = "standard"

// IsTerminal reports whether a worker status is final (JS TERMINAL).
func IsTerminal(status string) bool {
	switch status {
	case "finished", "failed", "interrupted", "deferred":
		return true
	}
	return false
}

// QuietRealertSeconds is how often a quiet worker re-alerts (F4).
const QuietRealertSeconds = 1800

// EventTTLSeconds is how long an event is kept before pruning (F8).
const EventTTLSeconds = 7 * 24 * 3600

// Phases that are not worker statuses.
const (
	PhaseRunning       = "running"
	PhaseFinishing     = "finishing"
	PhaseStaleLock     = "stale_lock"
	PhaseProcessExited = "process_exited"
	PhaseUnknown       = "unknown"
)

// Liveness is what the supervisor observed about the worker's process and lane lock.
type Liveness struct {
	Alive       bool
	LockPresent bool
}

// Observation is one look at a worker lane.
type Observation struct {
	Run string
	// Lane is worker.lane verbatim (normally a string, but any JSON value); HasLane is false
	// when the worker has none (JS undefined, which JSON.stringify omits).
	Lane       any
	HasLane    bool
	Phase      string
	Breadth    string
	Interval   int64
	ProgressAt int64 // epoch seconds of the last sign of progress
	Quiet      bool
	QuietFor   int64
}

// Observe classifies a worker state file at time now (epoch seconds).
func Observe(worker *jsjson.Object, now int64, l Liveness) Observation {
	breadth, _ := jsjson.Path(worker, "breadth").(string)
	if _, known := cadence[breadth]; !known {
		breadth = defaultBreadth
	}
	interval := cadence[breadth]
	progress := now
	for _, key := range []string{"last_progress_at", "updated_at", "started_at"} {
		if sec, ok := toSec(jsjson.Path(worker, key)); ok {
			progress = sec
			break
		}
	}
	phase := phaseOf(jsjson.OrText(jsjson.Path(worker, "status"), "unknown"), l)
	quietFor := now - progress
	lane, hasLane := jsjson.Lookup(worker, "lane")
	return Observation{
		Run:        runOf(worker),
		Lane:       lane,
		HasLane:    hasLane,
		Phase:      phase,
		Breadth:    breadth,
		Interval:   interval,
		ProgressAt: progress,
		Quiet:      phase == PhaseRunning && quietFor >= max(600, interval*3),
		QuietFor:   quietFor,
	}
}

func phaseOf(status string, l Liveness) string {
	switch {
	case IsTerminal(status) && l.Alive:
		return PhaseFinishing
	case IsTerminal(status) && l.LockPresent:
		return PhaseStaleLock
	case IsTerminal(status):
		return status
	case status == "running" || status == "starting":
		if l.Alive {
			return PhaseRunning
		}
		return PhaseProcessExited
	}
	return PhaseUnknown
}

// LaneText is the lane as the JS texts print it (`${obs.lane}`): "undefined" when absent.
func (o Observation) LaneText() string {
	return jsjson.TemplateStringOf(o.Lane, o.HasLane)
}

// runOf is the run id, or "<pid>:<lane>" for workers that predate run ids.
func runOf(worker *jsjson.Object) string {
	if runID := jsjson.Path(worker, "run_id"); jsjson.Truthy(runID) {
		return jsjson.TemplateString(runID)
	}
	return jsjson.TemplateStringOf(jsjson.Lookup(worker, "pid")) + ":" + jsjson.TemplateStringOf(jsjson.Lookup(worker, "lane"))
}

// toSec parses an ISO timestamp to whole epoch seconds; ok is false when it is falsy or
// unparsable.
func toSec(iso any) (sec int64, ok bool) {
	text, _ := iso.(string)
	ms, parsed := jsjson.ParseDate(text)
	if !parsed {
		return 0, false
	}
	return int64(math.Floor(float64(ms) / 1000)), true
}

// State is the lane's supervisor state as chooseEvent sees it: the run last recorded for the
// lane ("" when none or not a string), its next_check as the JS value the state file holds
// (nil when none; the supervisor passes it through unchanged, so it may be any JSON value),
// and the shared events map.
type State struct {
	Run       string
	NextCheck any
	Events    *jsjson.Object
}

// Event is a wake-up to emit now.
type Event struct {
	ID     string
	Reason string
}

// ChooseEvent decides whether to emit an event now; nil means stay silent. acks maps event
// ids to a truthy value once the coordinator acknowledged them.
func ChooseEvent(obs Observation, state State, acks *jsjson.Object, now int64) *Event {
	events := state.Events
	notAcked := func(id string) bool { return !jsjson.Truthy(jsjson.Path(acks, id)) }
	emitOnce := func(id, reason string) *Event {
		ev := jsjson.Path(events, id)
		if !jsjson.Truthy(ev) || (notAcked(id) && retryable(ev, now)) {
			return &Event{ID: id, Reason: reason}
		}
		return nil
	}

	if IsTerminal(obs.Phase) || obs.Phase == PhaseProcessExited || obs.Phase == PhaseStaleLock {
		return emitOnce(obs.Run+":"+obs.Phase, obs.Phase)
	}
	if obs.Phase != PhaseRunning {
		return nil
	}

	runEvents := eventsOfRun(events, obs.Run)
	// Re-arm an intent that never reached the CLI (crash between persist and send).
	for _, id := range runEvents {
		if ev := jsjson.Path(events, id); notAcked(id) && status(ev) == "intent_persisted" {
			reason, _ := jsjson.Path(ev, "reason").(string)
			return &Event{ID: id, Reason: reason}
		}
	}
	// At most one unacknowledged delivered progress reminder per run; failed deliveries do
	// not count (F1).
	for _, id := range runEvents {
		switch status(jsjson.Path(events, id)) {
		case "queued", "delivery_uncertain", "cli_invoked":
			if notAcked(id) {
				return nil
			}
		}
	}

	if obs.Quiet {
		bucket := obs.QuietFor / QuietRealertSeconds // F4: re-alert every 30 min of continued quiet
		return emitOnce(obs.Run+":quiet:"+itoa(obs.ProgressAt)+":"+itoa(bucket), "quiet")
	}
	var due any = float64(now + obs.Interval)
	if state.Run == obs.Run && jsjson.Truthy(state.NextCheck) {
		due = state.NextCheck
	}
	if jsjson.GreaterOrEqual(float64(now), due) {
		return &Event{ID: obs.Run + ":check:" + jsjson.TemplateString(due), Reason: "check"}
	}
	return nil
}

// eventsOfRun lists the ids of the run's events in JS key order.
func eventsOfRun(events *jsjson.Object, run string) []string {
	if events == nil {
		return nil
	}
	var ids []string
	for _, id := range events.Keys() {
		if strings.HasPrefix(id, run+":") {
			ids = append(ids, id)
		}
	}
	return ids
}

func status(ev any) string {
	s, _ := jsjson.Path(ev, "status").(string)
	return s
}

// retryable: an intent that was persisted but never sent, or a definite delivery failure
// whose retry time has come (no retry_after means never).
func retryable(ev any, now int64) bool {
	switch status(ev) {
	case "intent_persisted":
		return true
	case "delivery_failed":
		return float64(now) >= retryAfterOf(ev)
	}
	return false
}

// retryAfterOf is `ev.retry_after ?? Infinity`; a non-numeric value compares like NaN.
func retryAfterOf(ev any) float64 {
	switch v := jsjson.Path(ev, "retry_after").(type) {
	case nil:
		return math.Inf(1)
	case float64:
		return v
	}
	return math.NaN()
}

// PruneEvents keeps the events created within EventTTLSeconds of now, in their order; an
// event without a parsable created_at counts as created now.
func PruneEvents(events *jsjson.Object, now int64) *jsjson.Object {
	out := jsjson.NewObject()
	if events == nil {
		return out
	}
	for _, id := range events.Keys() {
		ev, _ := events.Get(id)
		created, ok := toSec(jsjson.Path(ev, "created_at"))
		if !ok {
			created = now
		}
		if now-created <= EventTTLSeconds {
			out.Set(id, ev)
		}
	}
	return out
}

// ReminderText is the wake message sent to the coordinator for an event.
func ReminderText(id, reason string, obs Observation, projectRoot string) string {
	return strings.Join([]string{
		`Orchestrator supervisor event ` + id + `: worker lane "` + obs.LaneText() + `" is ` + obs.Phase + ` (` + reason + `; ` + obs.Breadth + ` cadence).`,
		`Read ` + projectRoot + `/.orchestrator/workers/` + obs.LaneText() + `.json, the mailbox (` + cmdref.Command("mailbox") + ` list --unacked-by coordinator) and any released report.`,
		`Acknowledge receipt with: ` + cmdref.Command("supervisor") + ` ack --event ` + id + `. Receipt is not acceptance.`,
		`Then act: review released work independently and send findings back to the author via the mailbox, or dispatch the next bounded brief. Never duplicate a running worker, approve your own code, or bypass permissions. A quiet worker may be thinking; inspect evidence before calling it stuck.`,
	}, " ")
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
