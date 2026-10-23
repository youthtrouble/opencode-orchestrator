package supervisor

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// serveInterval is the wait after each tick (setTimeout(loop, 5000)): the period is the tick's
// own time plus this.
const serveInterval = 5 * time.Second

const alreadyServing = "a supervisor already owns this project"

// Serve is `serve`: claim the project's lock, then tick every serveInterval until the stop
// file appears, recording the outcome of each tick in the state file. It returns the exit
// code and, with code 1, the error the JS dies of (nil when the lock is held by another
// supervisor, which Serve reports on stderr itself).
//
// PE-30: the lock is a kernel record lock (ownlock.Acquire) this process holds for its life,
// so exactly one of any number of concurrent serves claims it, and a lock file no process
// holds (a serve killed with SIGKILL, a pre-S13 pid text) is claimed in place. As in the JS,
// serve has no signal handlers of its own: a killed or crashed serve leaves its lock file,
// which the kernel stops holding when the process exits. While a child that a tick waits for
// runs, procs.RunSync relays SIGINT, SIGTERM and SIGHUP to the child's process group, and
// serve dies of the signal once the child is reaped (at most the 10 s grace later, PE-22).
// The stop file is never removed here (start removes it).
func (s *Supervisor) Serve(stderr io.Writer) (int, error) {
	if err := nodefs.MkdirAll(s.Dir); err != nil {
		return 1, err
	}
	lock, err := ownlock.Acquire(s.LockPath, s.Pid)
	if errors.Is(err, ownlock.ErrHeld) {
		fmt.Fprintln(stderr, alreadyServing)
		return 1, nil
	}
	if err != nil {
		return 1, err
	}
	s.lock = lock
	// An array state is read as {} (PE-16); null and primitives fail at the first loop, as
	// the JS fails assigning to them.
	state := asObject(readJSON(s.StatePath, emptyObject))
	for {
		if nodefs.Exists(s.StopPath) {
			return s.stopServing(state)
		}
		if err := s.serveTick(state); err != nil {
			return 1, err
		}
		s.Sleep(serveInterval)
	}
}

// serveTick is one loop's `try { tickAll; writeJson(STATE) ; delete storage_error } catch
// { storage_error, storage_failures + 1 }`. A failed tick writes nothing: its storage_error is
// persisted by the next successful write, then deleted in memory, and storage_failures never
// resets. The error is the one the catch block itself throws on a state that is no object.
func (s *Supervisor) serveTick(state any) error {
	object, isObject := state.(*jsjson.Object)
	if !isObject {
		// tickAll throws assigning state.events; the catch then throws assigning storage_error.
		return jsjson.CannotAssign(state, "storage_error")
	}
	err := s.Tick(object)
	if err == nil {
		err = writeJSON(s.StatePath, object)
	}
	if err == nil {
		object.Delete("storage_error")
		return nil
	}
	object.Set("storage_error", errorCode(err))
	failures, _ := object.Get("storage_failures")
	object.Set("storage_failures", plusOne(failures))
	return nil
}

// stopServing records the stop in the state, releases the lock and exits 0. A state that
// cannot be written is the error the JS dies of, with the lock left. A lock that cannot be
// released (its path replaced by a process outside the ownlock contract) is the error.
func (s *Supervisor) stopServing(state any) (int, error) {
	object, isObject := state.(*jsjson.Object)
	if !isObject {
		return 1, jsjson.CannotAssign(state, "status")
	}
	object.Set("status", "stopped")
	object.Set("stopped_at", jsjson.ISOString(s.Now()))
	if err := writeJSON(s.StatePath, object); err != nil {
		return 1, err
	}
	if err := s.lock.Release(); err != nil {
		return 1, err
	}
	return 0, nil
}

// plusOne is `(v || 0) + 1` on a JSON value: a number adds, true counts as 1, and any other
// truthy value (a hand-edited string, object or array) concatenates "1" to its String(v).
func plusOne(v any) any {
	if !jsjson.Truthy(v) {
		return float64(1)
	}
	switch v := v.(type) {
	case float64:
		return v + 1
	case bool:
		return float64(2)
	}
	return jsjson.TemplateString(v) + "1"
}
