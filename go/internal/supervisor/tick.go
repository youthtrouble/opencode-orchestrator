package supervisor

import (
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/supervise"
)

// Deps are the process, clock and log dependencies of a tick. The CLI wires the real ones;
// tests inject fakes, so no test spawns a process or writes the real incident log.
type Deps struct {
	Now      func() time.Time          // Date.now()
	Pid      int                       // process.pid, recorded in the state
	Self     string                    // this executable, re-run for worker, flip and claude-exec
	Alive    func(pid any) bool        // the JS alive(pid): worker.Alive
	RunSync  func(SyncSpec) SyncResult // spawnSync: procs.RunSync with a grace
	Spawn    Spawner                   // spawn(..., {detached, stdio: "ignore"}).unref()
	Mailbox  func() *mailbox.Mailbox   // the project mailbox
	Incident func(incident.Draft)      // records and ignores failures: incident.TryRecord
	Sleep    func(time.Duration)       // serve's wait between ticks: time.Sleep
}

// SyncSpec is a spawnSync call: Name run with Args in Dir (the supervisor's own working
// directory when ""), outputs captured, killed with SIGTERM after a positive Timeout. The
// supervisor leaves Grace to the RunSync it is given (the CLI sets it).
type SyncSpec = procs.SyncSpec

// SyncResult is a spawnSync result.
type SyncResult = procs.SyncResult

// Spawner starts name with args in dir in a session of its own with its stdio on /dev/null,
// does not wait for it, and reaps it when it exits (procs.Detached.StartReaped). The error
// is a failure to start it.
type Spawner func(name string, args []string, dir string) error

// Supervisor watches the lanes of one project.
type Supervisor struct {
	Project
	Deps
	// deliver wakes the coordinator through one configured channel: deliverChannel, which
	// tests of the tick replace.
	deliver func(channel any, text string) *jsjson.Object
	// lock is the project's lock while Serve holds it.
	lock *ownlock.Lock
}

// New returns the supervisor of project with deps.
func New(project Project, deps Deps) *Supervisor {
	s := &Supervisor{Project: project, Deps: deps}
	s.deliver = s.deliverChannel
	return s
}

// The notes a tick records instead of throwing on hand-edited input (PE-50, PE-51).
const (
	configNotObject = "supervisor.json is not an object; read as {}"
	workerNotObject = "worker file is not an object; lane skipped"
)

// tick is the values one tickAll call reads once.
type tick struct {
	config any            // an object or an array (PE-16), never a primitive (PE-50)
	acks   *jsjson.Object // nil when the file holds null or no object (PE-16)
	now    int64          // epoch seconds
	nowISO string         // iso(now)
	events *jsjson.Object // state.events after pruning
	lanes  any            // state.lanes: an object, or a truthy primitive the JS cannot assign to
}

// Tick is the JS tickAll(state): look at every worker lane, record each lane's phase in
// state.lanes, re-dispatch a deferred lane whose window has reset, emit the due event of a
// lane through the two-phase intent, then stamp the state. state is mutated in place as the
// JS mutates it; the error is the one the JS throws, after which the rest of the tick is
// skipped and every earlier mutation and file write stays.
func (s *Supervisor) Tick(state *jsjson.Object) error {
	t := tick{
		config: s.readConfig(state),
		now:    unixSeconds(s.Now()),
	}
	t.nowISO = jsjson.ISOString(time.Unix(t.now, 0))
	t.acks, _ = readJSON(s.AcksPath, emptyObject).(*jsjson.Object)
	t.events = pruneEvents(jsjson.Path(state, "events"), t.now)
	state.Set("events", t.events)
	t.lanes = asObject(orEmpty(jsjson.Path(state, "lanes")))
	state.Set("lanes", t.lanes)

	lanes, err := s.workers.Lanes()
	if err != nil {
		return err
	}
	pruneVanishedLanes(t.lanes, lanes)
	for _, lane := range lanes {
		if err := s.tickLane(state, &t, lane); err != nil {
			return err
		}
	}
	if err := s.pruneRedispatch(state); err != nil {
		return err
	}
	if err := s.evaluateFlip(state, &t); err != nil {
		return err
	}
	state.Set("checked_at", t.nowISO)
	state.Set("pid", float64(s.Pid))
	state.Set("status", "running")
	return nil
}

// readConfig is supervisor.json read once per tick. A value that is not an object (`null`, a
// primitive) reads as {} and is noted in the state's config_error until it is fixed (PE-50;
// the JS throws at whichever read of it comes first).
func (s *Supervisor) readConfig(state *jsjson.Object) any {
	config := readJSON(s.ConfigPath, emptyObject)
	switch config.(type) {
	case *jsjson.Object, []any:
		state.Delete("config_error")
		return config
	}
	state.Set("config_error", configNotObject)
	return jsjson.NewObject()
}

// pruneEvents is `pruneEvents(state.events || {}, now)` over Object.entries of any value,
// with null entries dropped (PE-52; the JS reads `ev.created_at` of a null one and throws).
func pruneEvents(events any, now int64) *jsjson.Object {
	entries := jsjson.Spread(orEmpty(events))
	for _, id := range entries.Keys() {
		if ev, _ := entries.Get(id); ev == nil {
			entries.Delete(id)
		}
	}
	return supervise.PruneEvents(entries, now)
}

// pruneVanishedLanes removes the state.lanes entries whose worker file is gone (PE-52; the JS
// keeps them forever). A primitive lanes value is left for the lane loop to throw on.
func pruneVanishedLanes(entries any, lanes []string) {
	object, isObject := entries.(*jsjson.Object)
	if !isObject {
		return
	}
	present := make(map[string]bool, len(lanes))
	for _, lane := range lanes {
		present[lane] = true
	}
	for _, lane := range object.Keys() {
		if !present[lane] {
			object.Delete(lane)
		}
	}
}

// tickLane is one iteration of tickAll's lane loop.
func (s *Supervisor) tickLane(state *jsjson.Object, t *tick, lane string) error {
	workerPath := filepath.Join(s.workers.WorkersDir, lane+".json")
	file := asObject(readJSON(workerPath, func() any { return nil }))
	if !jsjson.Truthy(file) {
		return nil
	}
	worker, isObject := file.(*jsjson.Object)
	if !isObject {
		return skipLane(t, lane)
	}
	worker.Set("lane", lane)

	// PE-30: the lane's lock counts only while the kernel says a runner holds it; a lock file
	// a killed runner left is no stale_lock.
	lockHeld, _, err := ownlock.Probe(filepath.Join(s.workers.WorkersDir, lane+".lock"))
	if err != nil {
		return err
	}
	obs := supervise.Observe(worker, t.now, supervise.Liveness{
		Alive:       s.Alive(jsjson.Path(worker, "pid")),
		LockPresent: lockHeld,
	})
	laneState := jsjson.Path(t.lanes, lane)
	run := runValue(worker, obs)
	sameRun := strictEquals(jsjson.Path(laneState, "run"), run)
	nextCheck := jsjson.Path(laneState, "next_check")
	ev := supervise.ChooseEvent(obs, supervise.State{Run: runIf(sameRun, obs), NextCheck: nextCheck, Events: t.events}, t.acks, t.now)
	switch {
	case obs.Phase != supervise.PhaseRunning:
		// PE-59: only a running lane has checks ahead; an ended one would show a frozen time.
		nextCheck = nil
	case !sameRun || !jsjson.Truthy(nextCheck):
		nextCheck = float64(t.now + obs.Interval)
	}
	// A running lane's next_check moves only when emit sends an event, so it stays frozen
	// while a delivered reminder is unacknowledged (mirrored JS reminder behaviour).
	entry, err := laneEntry(run, obs, t.nowISO, nextCheck)
	if err != nil {
		return err
	}
	lanes, isObject := t.lanes.(*jsjson.Object)
	if !isObject {
		return jsjson.CannotAssign(t.lanes, lane)
	}
	setProperty(lanes, lane, entry)

	due, err := s.redispatchDue(obs, t.config, worker, state, t)
	if err != nil {
		return err
	}
	if due {
		return s.redispatch(lane, worker, jsjson.TemplateString(run), state, t)
	}
	if ev == nil {
		return nil
	}
	return s.emit(state, t, lane, obs, entry, ev)
}

// skipLane records in the lane's entry that its worker file holds a truthy primitive, and
// leaves the other lanes to tick (PE-51; the JS throws assigning its lane and aborts the tick).
func skipLane(t *tick, lane string) error {
	lanes, isObject := t.lanes.(*jsjson.Object)
	if !isObject {
		return jsjson.CannotAssign(t.lanes, lane)
	}
	entry := jsjson.NewObject()
	entry.Set("error", workerNotObject)
	entry.Set("checked_at", t.nowISO)
	setProperty(lanes, lane, entry)
	return nil
}

// runValue is the JS obs.run as a value: `worker.run_id || `${worker.pid}:${worker.lane}“.
// A hand-edited run_id that is not a string (e.g. 5) stays that value; obs.Run is its text,
// which is what the event ids are built from.
func runValue(worker *jsjson.Object, obs supervise.Observation) any {
	if runID := jsjson.Path(worker, "run_id"); jsjson.Truthy(runID) {
		return runID
	}
	return obs.Run
}

// runIf is the run chooseEvent compares with obs.run: the observed one when the lane's
// recorded run is strictly equal to it, else "" (never equal: obs.Run is never empty).
func runIf(sameRun bool, obs supervise.Observation) string {
	if sameRun {
		return obs.Run
	}
	return ""
}

// strictEquals is JS `a === b` for two values parsed from different JSON documents: objects
// and arrays are distinct objects, so never equal.
func strictEquals(a, b any) bool {
	for _, v := range []any{a, b} {
		switch v.(type) {
		case *jsjson.Object, []any:
			return false
		}
	}
	return a == b
}

// laneEntry is the lane's new state.lanes entry. nextCheck is a JS value (a hand-edited
// next_check is passed through), so its ISO form may throw; nil (no check ahead) stays null.
func laneEntry(run any, obs supervise.Observation, checkedAt string, nextCheck any) (*jsjson.Object, error) {
	var nextCheckAt any
	if nextCheck != nil {
		at, err := isoSeconds(nextCheck)
		if err != nil {
			return nil, err
		}
		nextCheckAt = at
	}
	entry := jsjson.NewObject()
	entry.Set("run", run)
	entry.Set("phase", obs.Phase)
	entry.Set("breadth", obs.Breadth)
	entry.Set("quiet", obs.Quiet)
	entry.Set("checked_at", checkedAt)
	entry.Set("next_check", nextCheck)
	entry.Set("next_check_at", nextCheckAt)
	return entry, nil
}

// emit sends the lane's event: persist the intent, post the wake to the mailbox, mark it
// cli_invoked, wake the configured channels, record the outcome and log it. A mailbox failure
// is swallowed (the channels may still work); the incident log never blocks.
func (s *Supervisor) emit(state *jsjson.Object, t *tick, lane string, obs supervise.Observation, entry *jsjson.Object, ev *supervise.Event) error {
	text := supervise.ReminderText(ev.ID, ev.Reason, obs, s.Root)
	event := jsjson.NewObject()
	event.Set("status", "intent_persisted")
	event.Set("reason", ev.Reason)
	event.Set("lane", lane)
	event.Set("created_at", t.nowISO)
	event.Set("retry_after", float64(t.now+obs.Interval))
	t.events.Set(ev.ID, event)
	if err := writeJSON(s.StatePath, state); err != nil {
		return err
	}
	var mailboxID any
	if msg, err := s.Mailbox().Post(mailbox.Draft{From: "supervisor", To: "coordinator", Kind: "wake", Subject: lane + ": " + ev.Reason, Body: text, Thread: ev.ID}); err == nil {
		mailboxID = jsjson.Path(msg, "id")
	}
	event.Set("status", "cli_invoked")
	event.Set("mailbox_id", mailboxID)
	if err := writeJSON(s.StatePath, state); err != nil {
		return err
	}

	results, err := s.wake(t.config, text)
	if err != nil {
		return err
	}
	outcome := deliveryOutcome(results, mailboxID)
	event.Set("status", outcome)
	event.Set("channels", results)
	if obs.Phase == supervise.PhaseRunning {
		entry.Set("next_check", float64(t.now+obs.Interval))
		entry.Set("next_check_at", jsjson.ISOString(time.Unix(t.now+obs.Interval, 0)))
	}

	resultsJSON, err := jsjson.Marshal(results)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s %s %s\n", t.nowISO, ev.ID, outcome, resultsJSON)
	if err := nodefs.AppendPrivateFile(s.LogPath, []byte(line)); err != nil {
		return err
	}
	if outcome == "delivery_failed" {
		s.Incident(incident.Draft{Component: "supervisor", Kind: "bug", Summary: "wake delivery failed for " + ev.Reason, Detail: string(resultsJSON), Project: s.Root})
	}
	if ev.Reason == supervise.PhaseStaleLock || ev.Reason == supervise.PhaseProcessExited {
		s.Incident(incident.Draft{Component: "supervisor", Kind: "crash", Summary: "worker lane " + lane + ": " + ev.Reason, Project: s.Root})
	}
	return nil
}

// errWakeNotArray is V8's TypeError for `(config.wake || []).map(...)` on a truthy wake that
// is not an array (probed with Node v26.5.1).
var errWakeNotArray = &jsjson.TypeError{Message: "(config.wake || []).map is not a function"}

// wake is `(config.wake || []).map((ch) => ({type: ch.type, ...deliver(ch, text)}))`.
func (s *Supervisor) wake(config any, text string) ([]any, error) {
	wake := jsjson.Path(config, "wake")
	if !jsjson.Truthy(wake) {
		return []any{}, nil
	}
	channels, isArray := wake.([]any)
	if !isArray {
		return nil, errWakeNotArray
	}
	results := make([]any, 0, len(channels))
	for _, channel := range channels {
		if channel == nil {
			return nil, jsjson.CannotReadOfNull("type")
		}
		result := jsjson.NewObject()
		if channelType, present := jsjson.Lookup(channel, "type"); present {
			result.Set("type", channelType)
		}
		delivered := s.deliver(channel, text)
		for _, key := range delivered.Keys() {
			value, _ := delivered.Get(key)
			result.Set(key, value)
		}
		results = append(results, result)
	}
	return results, nil
}

// deliveryOutcome is the event status after the wake: queued when any channel queued it or
// the mailbox took it, else uncertain when any channel may have, else failed.
func deliveryOutcome(results []any, mailboxID any) string {
	anyStatus := func(status string) bool {
		for _, result := range results {
			if jsjson.Path(result, "status") == status {
				return true
			}
		}
		return false
	}
	switch {
	case anyStatus("queued") || jsjson.Truthy(mailboxID):
		return "queued"
	case anyStatus("delivery_uncertain"):
		return "delivery_uncertain"
	}
	return "delivery_failed"
}

// orEmpty is `v || {}`.
func orEmpty(v any) any {
	if jsjson.Truthy(v) {
		return v
	}
	return jsjson.NewObject()
}

// unixSeconds is the JS nowSec(): Math.floor(Date.now() / 1000).
func unixSeconds(now time.Time) int64 {
	return int64(math.Floor(float64(now.UnixMilli()) / 1000))
}

// maxTimeMs is the largest time value a JS Date holds (TimeClip).
const maxTimeMs = 8.64e15

// isoSeconds is the JS iso(s) = new Date(s * 1000).toISOString() for any JS value s: NaN or a
// time beyond ±8.64e15 ms throws RangeError: Invalid time value; years outside 0-9999 are
// written with a sign and six digits.
func isoSeconds(s any) (string, error) {
	ms := toNumber(s) * 1000
	if math.IsNaN(ms) || math.Abs(ms) > maxTimeMs {
		return "", jsjson.ErrInvalidTime
	}
	t := time.UnixMilli(int64(ms)).UTC() // TimeClip truncates toward zero, as the conversion does
	year := t.Year()
	if year >= 0 && year <= 9999 {
		return jsjson.ISOString(t), nil
	}
	sign := "+"
	if year < 0 {
		sign, year = "-", -year
	}
	return fmt.Sprintf("%s%06d-%s", sign, year, t.Format("01-02T15:04:05.000Z")), nil
}

// toNumber is JS Number(v) for any parsed JSON value: an object or array through its string
// form.
func toNumber(v any) float64 {
	switch v.(type) {
	case []any, *jsjson.Object:
		return jsjson.ToNumber(jsjson.TemplateString(v))
	}
	return jsjson.ToNumber(v)
}
