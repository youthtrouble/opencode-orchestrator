package worker

import (
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// KillError is the uncaught error of process.kill when the stop signal cannot be sent; the
// JS prints it as `Error: kill <CODE>`.
type KillError struct{ Code string }

func (e *KillError) Error() string { return "kill " + e.Code }

// Stop is `worker.mjs stop --lane L`: when the lane's state names a live pid, SIGINT to that
// pid's process group, or to the pid alone when it leads no group, then
// {lane, stopped: true, pid} with pid as stored. Otherwise {lane, stopped: false, reason}. An
// absent lane (nil) reads `undefined.json` and is left out of the output, as JSON.stringify
// drops undefined.
//
// PE-14: only a pid that coerces to an integer >= 2 is signalled. The JS also signals pid 1,
// true, "1" or [1] (kill(-1): every process the user may signal) and a negative pid (process
// |pid|); orch reports those as no live worker.
func (p Project) Stop(lane any) (*jsjson.Object, error) {
	state := p.ReadState(lane)
	pid := jsjson.Path(state, "pid")
	out := jsjson.NewObject()
	if lane != nil {
		out.Set("lane", lane)
	}
	target, signalable := signalablePid(pid)
	if !jsjson.Truthy(state) || !Alive(pid) || !signalable {
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

// killCode is the err.code of a failed process.kill.
func killCode(err error) string {
	if code := nodefs.Code(err); code != "" {
		return code
	}
	return err.Error()
}
