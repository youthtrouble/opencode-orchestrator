package supervisor

import (
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
func (s *Supervisor) redispatchDue(obs supervise.Observation, config any, worker *jsjson.Object) (bool, error) {
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
	return !jsjson.Truthy(jsjson.Path(worker, "redispatched_at")), nil
}

// redispatch relaunches the lane's brief as a detached worker and waits for the launcher,
// then records redispatched_at in the worker file (after the launch, so it may overwrite the
// new runner's first state, as in the JS) and posts a note with the launcher's output. An
// argument Node would reject, a failed write or a failed post aborts the tick.
func (s *Supervisor) redispatch(lane string, worker *jsjson.Object, workerPath string) error {
	args, err := spawnArgs(redispatchArgs(lane, worker, s.Root)...)
	if err != nil {
		return err
	}
	result := s.RunSync(SyncSpec{Name: s.Self, Args: args, Dir: s.Root})
	worker.Set("redispatched_at", jsjson.ISOString(s.Now()))
	if err := writeJSON(workerPath, worker); err != nil {
		return err
	}
	_, err = s.Mailbox().Post(mailbox.Draft{
		From:    "supervisor",
		To:      "coordinator",
		Kind:    "note",
		Subject: lane + " re-dispatched after budget reset",
		Body:    outputText(result, noteBodyUnits),
	})
	return err
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
