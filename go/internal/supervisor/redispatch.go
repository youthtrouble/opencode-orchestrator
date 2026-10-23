package supervisor

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/supervise"
)

// redispatchGraceMs is how long after resume_after a deferred lane is re-dispatched.
const redispatchGraceMs = 60_000

// noteBodyUnits is how many UTF-16 units of the launcher's output the note keeps.
const noteBodyUnits = 500

// redispatchDue is the JS condition for re-dispatching a budget-deferred lane: deferred,
// not opted out (`config.auto_redispatch_deferred !== false`; a null config throws there), a
// resume_after whose Date.parse plus a minute has passed (the clock is read again), and not
// re-dispatched before.
func (s *Supervisor) redispatchDue(obs supervise.Observation, config any, worker *jsjson.Object, state *jsjson.Object, t *tick) (bool, error) {
	if obs.Phase != "deferred" {
		return false, nil
	}
	if config == nil {
		return false, jsjson.CannotReadOfNull("auto_redispatch_deferred")
	}
	if jsjson.Path(config, "auto_redispatch_deferred") == false {
		return false, nil
	}
	resumeAfter := jsjson.Path(worker, "resume_after")
	if !jsjson.Truthy(resumeAfter) {
		return false, nil
	}
	resumeMs, parsed := jsjson.ParseDate(jsjson.TemplateString(resumeAfter))
	if !parsed || resumeMs+redispatchGraceMs > s.Now().UnixMilli() {
		return false, nil
	}
	return !legacyRedispatched(worker) && recordEligible(state, jsjson.TemplateString(runValue(worker, obs)), t.now), nil
}

// redispatch runs the launcher before recording its outcome in supervisor state. The
// worker file belongs exclusively to its runner, including when it publishes during RunSync.
func (s *Supervisor) redispatch(lane string, worker *jsjson.Object, key string, state *jsjson.Object, t *tick) error {
	args, err := spawnArgs(redispatchArgs(lane, worker, s.Root)...)
	if err != nil {
		return err
	}
	result := s.RunSync(SyncSpec{Name: s.Self, Args: args, Dir: s.Root})
	runID := launcherRunID(result)
	stopped := recordRedispatch(state, key, t, runID)
	if stopped {
		recipient := "coordinator"
		if lane == "conductor" {
			recipient = "human"
		}
		s.noteRelaunchStopped(t, lane, recipient, result, args)
		return nil
	}
	subject := lane + " re-dispatched after budget reset"
	if runID == nil {
		subject = fmt.Sprintf("%s re-dispatch failed (attempt %v of %d)", lane, jsjson.Path(state, "redispatch", key, "attempts"), maxRedispatchAttempts)
	}
	s.postRedispatchNote(t, mailbox.Draft{
		From: "supervisor", To: "coordinator", Kind: "note",
		Subject: subject, Body: outputText(result, noteBodyUnits),
	})
	return nil
}

const maxRedispatchAttempts = 5

// legacyRedispatched reads the stamp older supervisors wrote into a lane file. It means the
// lane's restart or redispatch is done, so it gates only those (read-only compatibility).
func legacyRedispatched(worker any) bool {
	return jsjson.Truthy(jsjson.Path(worker, "redispatched_at"))
}

// recordEligible is the attempt-record gate shared by deferred lanes, decided conductor
// relaunches and pending launches: not confirmed, under the attempt cap and past backoff.
func recordEligible(state *jsjson.Object, key string, now int64) bool {
	record := jsjson.Path(state, "redispatch", key)
	if jsjson.Path(record, "confirmed_run_id") != nil {
		return false
	}
	attempts := toNumber(jsjson.Path(record, "attempts"))
	if attempts >= maxRedispatchAttempts {
		return false
	}
	if attempts == 0 {
		return true
	}
	last, ok := jsjson.ParseDate(jsjson.TemplateString(jsjson.Path(record, "last_attempt_at")))
	backoff := math.Min(60*math.Pow(2, attempts-1), 30*60)
	return ok && float64(now)-float64(last)/1000 >= backoff
}

// recordRedispatch returns true only for the transition to the fifth failure. It is
// called before logging or posting, so storage errors cannot erase retry bookkeeping.
func recordRedispatch(state *jsjson.Object, key string, t *tick, runID any) bool {
	records, ok := jsjson.Path(state, "redispatch").(*jsjson.Object)
	if !ok {
		records = jsjson.NewObject()
		state.Set("redispatch", records)
	}
	attempts := toNumber(jsjson.Path(records, key, "attempts")) + 1
	record := jsjson.NewObject()
	record.Set("attempts", attempts)
	record.Set("last_attempt_at", t.nowISO)
	record.Set("confirmed_run_id", runID)
	records.Set(key, record)
	return runID == nil && attempts == maxRedispatchAttempts
}

// launcherRunID requires the runner pid as well as the run id: detach may return
// exit zero and a run id even when its runner never started.
func launcherRunID(result SyncResult) any {
	if result.Status == nil || *result.Status != 0 || result.Err != nil {
		return nil
	}
	return launchedRunID(lastJSONLine(result.Stdout))
}

func launchedRunID(value any) any {
	object, ok := value.(*jsjson.Object)
	if !ok {
		return nil
	}
	runID, hasRun := jsjson.Path(object, "run_id").(string)
	_, hasPID := jsjson.Path(object, "runner_pid").(float64)
	if !hasRun || !hasPID {
		return nil
	}
	return runID
}

// flipLaunchOutcome separates a refused/deferred run from a launch attempt. A launch is
// confirmed by the answer's own run_id and runner_pid (PE-63); its launcher quote is display
// text cut at 400 units and is not read.
func flipLaunchOutcome(result SyncResult) (runID any, attempted bool) {
	answer := lastJSONLine(result.Stdout)
	if jsjson.Path(answer, "flipped") == false &&
		(jsjson.Path(answer, "pending") == true || jsjson.Path(answer, "reason") == "awaiting human") {
		return nil, false
	}
	if result.Status == nil || *result.Status != 0 || result.Err != nil || jsjson.Path(answer, "flipped") != true {
		return nil, true
	}
	return launchedRunID(answer), true
}

const conductorNoneKey = "conductor:none"

func conductorRunKey(lane any) string {
	worker, ok := lane.(*jsjson.Object)
	if !ok {
		return conductorNoneKey
	}
	worker = jsjson.Spread(worker)
	worker.Set("lane", "conductor")
	return workerRunKey(worker)
}

func workerRunKey(worker *jsjson.Object) string {
	obs := supervise.Observe(worker, 0, supervise.Liveness{})
	return jsjson.TemplateString(runValue(worker, obs))
}

func pendingRunKey(pending any) string {
	return "pending:" + jsjson.TemplateStringOf(jsjson.Lookup(pending, "at"))
}

func (s *Supervisor) postRedispatchNote(t *tick, draft mailbox.Draft) {
	if _, err := s.Mailbox().Post(draft); err != nil {
		// The attempt is already in memory. A broken diagnostic sink must not abort a tick.
		_ = s.log(t, "redispatch note post failed: "+err.Error())
	}
}

func (s *Supervisor) noteRelaunchStopped(t *tick, lane, recipient string, result SyncResult, args []string) {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = arg
		if arg == "" || strings.ContainsAny(arg, " \t\n\r'\"`$\\;|&<>()*?[]{}!#~") {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
	}
	s.postRedispatchNote(t, mailbox.Draft{
		From: "supervisor", To: recipient, Kind: "blocked",
		Subject: lane + " relaunch stopped after 5 failed attempts",
		Body:    fmt.Sprintf("%s\nRetry by hand: orch %s", outputText(result, noteBodyUnits), strings.Join(quoted, " ")),
	})
}

// pruneRedispatch re-reads the lane files after the loop: a launcher may have
// published a replacement run while the supervisor was waiting for it.
func (s *Supervisor) pruneRedispatch(state *jsjson.Object) error {
	records, ok := jsjson.Path(state, "redispatch").(*jsjson.Object)
	if !ok {
		return nil
	}
	lanes, err := s.workers.Lanes()
	if err != nil {
		return err
	}
	current := make(map[string]bool, len(lanes)+1)
	// Match evaluateFlip's read, including arrays being interpreted as empty objects.
	current[conductorRunKey(asObject(readJSON(s.conductorPath(), func() any { return nil })))] = true
	for _, lane := range lanes {
		worker, ok := readJSON(filepath.Join(s.workers.WorkersDir, lane+".json"), func() any { return nil }).(*jsjson.Object)
		if ok {
			worker.Set("lane", lane)
			current[workerRunKey(worker)] = true
		}
	}
	// A missing lane can still have a pending handoff. Reading flip state here
	// performs no write and evaluateFlip retains its fail-closed gate handling.
	flip := readJSON(s.flipPath(), func() any { return nil })
	if pending := jsjson.Path(flip, "pending_launch"); jsjson.Truthy(pending) {
		current[pendingRunKey(pending)] = true
	}
	for _, key := range records.Keys() {
		if !current[key] {
			records.Delete(key)
		}
	}
	return nil
}

// redispatchArgs is the JS worker.mjs argv as `orch worker` arguments, as JS values for
// spawnArgs. Only the brief, harness, lane, breadth, model and session are carried over
// (mirrored).
func redispatchArgs(lane string, worker *jsjson.Object, root string) []any {
	args := []any{"worker", "start",
		"--brief", property(worker, "brief"),
		"--harness", property(worker, "harness"),
		"--lane", lane,
		"--breadth", or(jsjson.Path(worker, "breadth"), "standard"),
		"--cwd", root,
		"--detach",
	}
	if model := jsjson.Path(worker, "model"); jsjson.Truthy(model) {
		args = append(args, "--model", model)
	}
	if session := jsjson.Path(worker, "session_id"); jsjson.Truthy(session) {
		args = append(args, "--resume", session)
	}
	return args
}
