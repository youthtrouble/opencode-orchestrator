package supervisor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// Cadences of the flip evaluation, in seconds.
const (
	flipCheckEvery      = 60  // between two evaluations
	neverStartedBackoff = 120 // between two launches of a flip target that never started
	relaunchCooldown    = 600 // between two relaunches decided by `flip decide`
)

// logOutputUnits is how many UTF-16 units of a launcher's output a log line keeps.
const logOutputUnits = 200

// conductorView is what one flip evaluation reads of the conductor lane and flip.json.
type conductorView struct {
	lane       any // workers/conductor.json: null when absent, an array read as {} (PE-16)
	flip       any // flip.json: null when absent
	activeDoor any // the door to act from: a JS value, as flip.to and config.home may be any
	idle       bool
	lockFree   bool
}

// evaluateFlip is the JS conductor flip / hand-back block of tickAll: once a minute, when the
// project has a conductor lane or a flip.json, relaunch a conductor that is due. Each spawn
// is a self-exec of `orch flip ...` waited for. Attempt records belong to supervisor state;
// a log append or an argument Node would reject can still abort the tick.
func (s *Supervisor) evaluateFlip(state *jsjson.Object, t *tick) error {
	lane := asObject(readJSON(s.conductorPath(), func() any { return nil }))
	if !jsjson.Truthy(lane) && !nodefs.Exists(s.flipPath()) {
		return nil
	}
	if !elapsed(t.now, jsjson.Path(state, "last_flip_check"), flipCheckEvery) {
		return nil
	}
	state.Set("last_flip_check", float64(t.now))

	record, err := flipcmd.NewProject(s.Root).Read()
	if err != nil {
		var stateErr *flipcmd.StateError
		if !errors.As(err, &stateErr) {
			return err
		}
		gate := jsjson.NewObject()
		gate.Set("reason", stateErr.GateReason())
		return s.noteAwaitingHuman(state, t, gate)
	}
	c := conductorView{lane: lane, flip: record.Value}
	activeDoor, err := s.activeDoor(c, t.config)
	if err != nil {
		return err
	}
	c.activeDoor = activeDoor
	c.idle = !jsjson.Truthy(lane) || !s.Alive(jsjson.Path(lane, "pid"))
	// PE-30: the conductor lane is locked only while the kernel says a runner holds its lock,
	// so a lock file a killed runner left no longer blocks a relaunch.
	lockHeld, _, err := ownlock.Probe(filepath.Join(s.workers.WorkersDir, "conductor.lock"))
	if err != nil {
		return err
	}
	c.lockFree = !lockHeld

	awaitingHuman := jsjson.Path(c.flip, "awaiting_human")
	if err := s.noteAwaitingHuman(state, t, awaitingHuman); err != nil {
		return err
	}
	switch {
	case jsjson.Truthy(awaitingHuman): // a human gate: nothing until it is cleared
		return nil
	case s.flipTargetNeverStarted(state, t, c):
		return s.launchNeverStarted(state, t, c)
	case c.idle && c.lockFree && jsjson.Truthy(jsjson.Path(c.flip, "pending_launch")):
		return s.launchPending(state, t, c)
	case c.idle:
		return s.decideAndRelaunch(state, t, c)
	}
	return nil
}

func (s *Supervisor) conductorPath() string {
	return filepath.Join(s.workers.WorkersDir, "conductor.json")
}

func (s *Supervisor) flipPath() string { return filepath.Join(s.Dir, "flip.json") }

func (s *Supervisor) mailboxPath() string {
	return filepath.Join(s.Dir, "mailbox", "messages.jsonl")
}

// activeDoor is the door of a live, unfinished conductor lane, else `flip?.to || (config.home
// || "opencode")` (config.home throws for a null config only when it is read).
func (s *Supervisor) activeDoor(c conductorView, config any) (any, error) {
	status := jsjson.Path(c.lane, "status")
	if jsjson.Truthy(c.lane) && !isOneOf(status, "finished", "failed", "interrupted", "deferred") && s.Alive(jsjson.Path(c.lane, "pid")) {
		return doorOf(c.lane), nil
	}
	if to := jsjson.Path(c.flip, "to"); jsjson.Truthy(to) {
		return to, nil
	}
	if config == nil {
		return nil, jsjson.CannotReadOfNull("home")
	}
	if home := jsjson.Path(config, "home"); jsjson.Truthy(home) {
		return home, nil
	}
	return "opencode", nil
}

// doorOf is the door a conductor lane runs on: claude for the claude harness, else opencode.
func doorOf(lane any) string {
	if jsjson.Path(lane, "harness") == "claude" {
		return "claude"
	}
	return "opencode"
}

// noteAwaitingHuman logs a human gate once and re-arms the note when the gate is cleared
// (awaiting_human_logged then stays as false).
func (s *Supervisor) noteAwaitingHuman(state *jsjson.Object, t *tick, awaitingHuman any) error {
	logged := jsjson.Truthy(jsjson.Path(state, "awaiting_human_logged"))
	switch {
	case jsjson.Truthy(awaitingHuman) && !logged:
		reason := jsjson.TemplateStringOf(jsjson.Lookup(awaitingHuman, "reason"))
		if err := s.log(t, "conductor awaiting human: "+reason+"; no relaunch until cleared"); err != nil {
			return err
		}
		state.Set("awaiting_human_logged", true)
	case !jsjson.Truthy(awaitingHuman) && logged:
		state.Set("awaiting_human_logged", false)
	}
	return nil
}

// flipTargetNeverStarted: a flip was recorded to a door, but the only lane belongs to the
// other door, has ended since the flip, and nothing holds the lock: the target never started.
func (s *Supervisor) flipTargetNeverStarted(state *jsjson.Object, t *tick, c conductorView) bool {
	to := jsjson.Path(c.flip, "to")
	return jsjson.Truthy(to) &&
		!jsjson.Truthy(jsjson.Path(c.flip, "pending_launch")) &&
		c.idle && c.lockFree && jsjson.Truthy(c.lane) &&
		!strictEquals(doorOf(c.lane), to) &&
		isOneOf(jsjson.Path(c.lane, "status"), "finished", "failed", "interrupted") &&
		dateAtOrAfter(property(c.flip, "at"), property(c.lane, "started_at")) &&
		elapsed(t.now, jsjson.Path(state, "last_relaunch_at"), neverStartedBackoff)
}

func (s *Supervisor) launchNeverStarted(state *jsjson.Object, t *tick, c conductorView) error {
	to := jsjson.Path(c.flip, "to")
	out, err := s.flip(s.Root, "run", "--unless-gated", "--to", to, "--from", doorOf(c.lane), "--reason", "flip target never started", "--cwd", s.Root)
	if err != nil {
		return err
	}
	if err := s.log(t, "flip target never started: launching "+jsjson.TemplateString(to)+": "+out); err != nil {
		return err
	}
	state.Set("last_relaunch_at", float64(t.now))
	return nil
}

// launchPending launches a hand-off recorded while the previous conductor still held the
// lane. Each pending record has its own bounded retry backoff.
func (s *Supervisor) launchPending(state *jsjson.Object, t *tick, c conductorView) error {
	pending := jsjson.Path(c.flip, "pending_launch")
	key := pendingRunKey(pending)
	if !redispatchEligible(state, key, nil, t.now) {
		return nil
	}
	// The log shows pl.from as it is (undefined included); the argument is pl.from || activeDoor.
	from, to := property(pending, "from"), property(pending, "to")
	args := []any{"run", "--unless-gated", "--to", to, "--from", or(jsjson.Path(pending, "from"), c.activeDoor),
		"--reason", or(jsjson.Path(pending, "reason"), "pending launch"), "--cwd", s.Root}
	if resume := jsjson.Path(pending, "resume"); jsjson.Truthy(resume) {
		args = append(args, "--resume", resume)
	}
	result, err := s.flipResult(s.Root, args...)
	if err != nil {
		return err
	}
	runID, attempted := flipLaunchOutcome(result)
	stopped := false
	if attempted {
		stopped = recordRedispatch(state, key, t, runID)
		state.Set("last_relaunch_at", float64(t.now))
	}
	logErr := s.log(t, "pending launch "+argText(from)+"->"+argText(to)+": "+outputText(result, logOutputUnits))
	if stopped {
		argv, err := spawnArgs(append([]any{"flip"}, args...)...)
		if err != nil {
			return errors.Join(logErr, err)
		}
		s.noteRelaunchStopped(t, "conductor", "human", result, argv)
	}
	return logErr
}

// decideAndRelaunch asks `flip decide` what an idle conductor's project should do and, at
// most once per relaunchCooldown, launches the door it names: the target of a flip or
// hand-back, or the active door again when the project was paused or the lane needs a
// restart. The launch result is recorded in supervisor state before diagnostics.
func (s *Supervisor) decideAndRelaunch(state *jsjson.Object, t *tick, c conductorView) error {
	decided, err := s.flipResult("", "decide", "--active", c.activeDoor, "--cwd", s.Root)
	if err != nil {
		return err
	}
	decision := lastJSONLine(decided.Stdout)
	paused := jsjson.Truthy(jsjson.Path(c.flip, "paused"))
	handedOff, err := s.handedOff(c.lane)
	if err != nil {
		return err
	}
	status := jsjson.Path(c.lane, "status")
	key := conductorRunKey(c.lane)
	eligible := redispatchEligible(state, key, c.lane, t.now)
	finishedEarly := jsjson.Truthy(c.lane) && status == "finished" && !handedOff && eligible
	laneNeedsRestart := jsjson.Truthy(c.lane) && (isOneOf(status, "failed", "deferred", "interrupted") || finishedEarly) && eligible

	action := jsjson.Path(decision, "action")
	var target any
	targetIsActive := false
	switch {
	case !jsjson.Truthy(decision):
	case action == "flip" || action == "handback":
		target = jsjson.Path(decision, "to")
		targetIsActive = strictEquals(target, c.activeDoor)
	case action == "stay" && (paused || laneNeedsRestart):
		target, targetIsActive = c.activeDoor, true
	}
	if !eligible || !jsjson.Truthy(target) || !elapsed(t.now, jsjson.Path(state, "last_relaunch_at"), relaunchCooldown) {
		return nil
	}

	var resumeArgs []any
	if session := jsjson.Path(c.lane, "session_id"); targetIsActive && jsjson.Truthy(session) && (doorOf(c.lane) == "claude") == (target == "claude") {
		resumeArgs = []any{"--resume", session}
	}
	reason := jsjson.TemplateString(action) + ": " + jsjson.TemplateStringOf(jsjson.Lookup(decision, "reason"))
	if finishedEarly {
		reason += " (previous conductor exited without handoff)"
	}
	args := append([]any{"run", "--unless-gated", "--to", target, "--from", c.activeDoor, "--reason", reason, "--cwd", s.Root}, resumeArgs...)
	result, err := s.flipResult("", args...)
	if err != nil {
		return err
	}
	runID, attempted := flipLaunchOutcome(result)
	stopped := false
	if attempted {
		stopped = recordRedispatch(state, key, t, runID)
		state.Set("last_relaunch_at", float64(t.now))
	}
	wasPaused := ""
	if paused {
		wasPaused = ", was paused"
	}
	logErr := s.log(t, fmt.Sprintf("relaunch %s->%s (%s%s): %s", jsjson.TemplateString(c.activeDoor), jsjson.TemplateString(target), jsjson.TemplateString(action), wasPaused, outputText(result, logOutputUnits)))
	s.Incident(incident.Draft{
		Component: "flip",
		Kind:      "flip",
		Summary:   fmt.Sprintf("conductor %s %s->%s", jsjson.TemplateString(action), jsjson.TemplateString(c.activeDoor), jsjson.TemplateString(target)),
		Detail:    textOrEmpty(jsjson.Path(decision, "reason")),
		Project:   s.Root,
	})
	if stopped {
		argv, err := spawnArgs(append([]any{"flip"}, args...)...)
		if err != nil {
			return errors.Join(logErr, err)
		}
		s.noteRelaunchStopped(t, "conductor", "human", result, argv)
	}
	return logErr
}

// handedOff reports whether the mailbox holds a handoff posted at or after the conductor
// lane started (a JS `>=` of the two values). A line that does not parse, or whose message
// has no such fields, does not count; a mailbox that exists but cannot be read aborts.
func (s *Supervisor) handedOff(lane any) (bool, error) {
	if !jsjson.Truthy(lane) || !nodefs.Exists(s.mailboxPath()) {
		return false, nil
	}
	data, err := nodefs.ReadFile(s.mailboxPath())
	if err != nil {
		return false, err
	}
	startedAt, hasStart := jsjson.Lookup(lane, "started_at")
	for _, line := range strings.Split(jsstr.DecodeUTF8(data), "\n") {
		message, err := jsjson.Unmarshal([]byte(line))
		if err != nil || jsjson.Path(message, "kind") != "handoff" {
			continue
		}
		// undefined >= anything is false; jsjson's operands are null when absent.
		if at, hasAt := jsjson.Lookup(message, "at"); hasAt && hasStart && jsjson.GreaterOrEqual(at, startedAt) {
			return true, nil
		}
	}
	return false, nil
}

// flip self-execs `orch flip <args>` in dir and returns its trimmed output for the log.
func (s *Supervisor) flip(dir string, args ...any) (string, error) {
	r, err := s.flipResult(dir, args...)
	if err != nil {
		return "", err
	}
	return outputText(r, logOutputUnits), nil
}

// flipResult runs `orch flip <args>` in dir ("" is the supervisor's own working directory, as
// the JS spawnSync without a cwd option); an argument Node rejects is thrown.
func (s *Supervisor) flipResult(dir string, args ...any) (SyncResult, error) {
	argv, err := spawnArgs(append([]any{"flip"}, args...)...)
	if err != nil {
		return SyncResult{}, err
	}
	return s.RunSync(SyncSpec{Name: s.Self, Args: argv, Dir: dir}), nil
}

// lastJSONLine is `JSON.parse(stdout.trim().split("\n").pop())`, nil when that throws.
func lastJSONLine(stdout string) any {
	lines := strings.Split(strings.Trim(stdout, jsstr.WhiteSpace), "\n")
	value, err := jsjson.Unmarshal([]byte(lines[len(lines)-1]))
	if err != nil {
		return nil
	}
	return value
}

// outputText is `(r.stdout || r.stderr || "").trim().slice(0, units)`.
func outputText(r SyncResult, units int) string {
	out := r.Stdout
	if out == "" {
		out = r.Stderr
	}
	return jsstr.Slice(strings.Trim(out, jsstr.WhiteSpace), 0, units)
}

// log appends `<iso(now)> <text>` to the supervisor log; without logs/ it throws ENOENT.
func (s *Supervisor) log(t *tick, text string) error {
	return nodefs.AppendPrivateFile(s.LogPath, []byte(t.nowISO+" "+text+"\n"))
}

// elapsed is `now - (last || 0) >= seconds` in JS arithmetic: a last that is not a number
// is NaN, which never elapses.
func elapsed(now int64, last any, seconds float64) bool {
	if !jsjson.Truthy(last) {
		last = 0.0
	}
	return float64(now)-toNumber(last) >= seconds
}

// dateAtOrAfter is `Date.parse(a) >= Date.parse(b)` (NaN when either does not parse).
func dateAtOrAfter(a, b any) bool {
	aMs, aOK := jsjson.ParseDate(argText(a))
	bMs, bOK := jsjson.ParseDate(argText(b))
	return aOK && bOK && aMs >= bMs
}

// isOneOf is `[...names].includes(v)`.
func isOneOf(v any, names ...string) bool {
	for _, name := range names {
		if v == name {
			return true
		}
	}
	return false
}

// or is `v || fallback`.
func or(v, fallback any) any {
	if jsjson.Truthy(v) {
		return v
	}
	return fallback
}

// textOrEmpty is the incident log's `String(text ?? "")`.
func textOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	return jsjson.TemplateString(v)
}
