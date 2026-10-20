package supervisor

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// serveInterval is the wait after each tick (setTimeout(loop, 5000)): the period is the tick's
// own time plus this.
const serveInterval = 5 * time.Second

const alreadyServing = "a supervisor already owns this project"

// Serve is `serve`: claim the project's lock, then tick every serveInterval until the stop
// file appears, recording the outcome of each tick in the state file. It returns the exit
// code and, with code 1, the error the JS dies of (nil when the lock is held by a live
// supervisor, which Serve reports on stderr itself).
//
// As in the JS, serve has no signal handlers of its own: a killed serve leaves its lock, which
// the next serve or start reclaims once the pid in it is dead. While a child that a tick waits
// for runs, procs.RunSync relays SIGINT, SIGTERM and SIGHUP to the child's process group, and
// serve dies of the signal once the child is reaped (at most the 10 s grace later, PE-22),
// leaving the lock all the same. The stop file is never removed here (start removes it).
func (s *Supervisor) Serve(stderr io.Writer) (int, error) {
	if err := nodefs.MkdirAll(s.Dir); err != nil {
		return 1, err
	}
	claimed, err := s.claimLock()
	if err != nil {
		return 1, err
	}
	if !claimed {
		fmt.Fprintln(stderr, alreadyServing)
		return 1, nil
	}
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

// claimLock is the JS claimLock(): create the lock with this pid, failing if it exists; when
// that fails for any reason, take the lock over unless the pid in it is alive. claimed is
// false when a live process holds it; err is a failure to overwrite a stale lock.
func (s *Supervisor) claimLock() (claimed bool, err error) {
	pid := []byte(strconv.Itoa(s.Pid))
	if createExclusive(s.LockPath, pid) == nil {
		return true, nil
	}
	if s.Alive(s.lockHolder()) {
		return false, nil
	}
	// Replaced atomically and owner-only, never written through a symlink (PE-21).
	if err := nodefs.WritePrivateFile(s.LockPath, pid); err != nil {
		return false, err
	}
	return true, nil
}

// createExclusive is writeFileSync(path, data, {flag: "wx"}), owner-only (0600) where the JS
// creates it 0666 less the umask (PE-21).
func createExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

// lockHolder is Number(readText(LOCK)): the trimmed lock text as a JS number, 0 when the lock
// cannot be read and NaN when it holds no number (neither is ever alive).
func (p Project) lockHolder() float64 {
	data, err := os.ReadFile(p.LockPath)
	if err != nil {
		return 0
	}
	return jsjson.ToNumber(jsstr.DecodeUTF8(data)) // ToNumber trims as .trim() does
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

// stopServing records the stop in the state, releases the lock (errors ignored) and exits 0.
// A state that cannot be written is the error the JS dies of, with the lock left.
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
	_ = syscall.Unlink(s.LockPath)
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
