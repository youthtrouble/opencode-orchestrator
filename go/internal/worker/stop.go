package worker

import (
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// KillError is the uncaught error of process.kill when the stop signal cannot be sent; the
// JS prints it as `Error: kill <CODE>`.
type KillError struct{ Code string }

func (e *KillError) Error() string { return "kill " + e.Code }

// Stop is `worker.mjs stop --lane L`: when the lane's runner is live, SIGINT to that pid's
// process group, or to the pid alone when it leads no group, then {lane, stopped: true, pid}
// with pid as stored. Otherwise {lane, stopped: false, reason}. An absent lane (nil) reads
// `undefined.json` and is left out of the output, as JSON.stringify drops undefined.
//
// PE-14: only a pid that coerces to an integer >= 2 is signalled. The JS also signals pid 1,
// true, "1" or [1] (kill(-1): every process the user may signal) and a negative pid (process
// |pid|); orch reports those as no live worker.
//
// PE-23: only the runner that holds the lane is signalled: the kernel must report a record
// lock on the lane's lock (ownlock.Probe), held by the state's pid. A running worker holds that
// lock for its whole run; the kernel drops it when the runner exits, SIGKILL included, so a
// finished or killed runner's pid, which the system may since have given to an unrelated
// process, is never signalled, whatever the lock file's text says. The JS signals any live pid
// in the state. The residual window is the microseconds between the probe (F_GETLK) and the
// kill: the runner can exit and its pid be reused in between (pidfd is Linux-only and macOS
// has no equivalent).
//
// C6: the runner holds the lane, not its bridge's process group. A runner killed with SIGKILL
// frees the lane even if its bridge's group lives on; stop then reports no live worker and
// leaves that group alone, as it did for a dead runner pid before.
//
// A lane that CheckLane refuses is a *UsageError, before anything is read.
func (p Project) Stop(lane any) (*jsjson.Object, error) {
	if err := CheckLane(lane); err != nil {
		return nil, err
	}
	state := p.ReadState(lane)
	pid := jsjson.Path(state, "pid")
	out := jsjson.NewObject()
	if lane != nil {
		out.Set("lane", lane)
	}
	target, signalable := signalablePid(pid)
	// A falsy state has no pid, so it is not signalable either.
	if !signalable || !p.laneHeldBy(lane, target) {
		out.Set("stopped", false)
		out.Set("reason", "no live worker")
		return out, nil
	}
	if err := p.kill(-target, syscall.SIGINT); err != nil {
		if err := p.kill(target, syscall.SIGINT); err != nil {
			return nil, &KillError{Code: killCode(err)}
		}
	}
	out.Set("stopped", true)
	out.Set("pid", pid)
	return out, nil
}

// signalablePid is the pid stop may signal under PE-14: an integer of at least 2 once coerced
// as process.kill coerces it.
func signalablePid(pid any) (int, bool) {
	n, ok := coercePid(pid)
	return n, ok && n >= 2
}

// laneHeldBy reports whether the kernel says pid holds the lane's lock. A lock that cannot be
// probed is held by no one: no runner can be shown to own the lane, so nothing is signalled,
// and stop's output stays the JS's "no live worker".
func (p Project) laneHeldBy(lane any, pid int) bool {
	held, holder, err := ownlock.Probe(p.LockPath(lane))
	return err == nil && held && holder == pid
}

// killCode is the err.code of a failed process.kill.
func killCode(err error) string {
	if code := nodefs.Code(err); code != "" {
		return code
	}
	return err.Error()
}
