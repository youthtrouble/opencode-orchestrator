package supervisor

import (
	"errors"
	"path/filepath"
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// Starter is what `start` needs to launch a detached `serve`. The CLI wires the real ones;
// tests inject them, so no unit test starts a daemon.
type Starter struct {
	Self      string                                             // this executable
	KeepAwake func(cmd string, args []string) (string, []string) // keepawake.Wrap, enabled per registry
	Detach    func(procs.Detached) (int, error)                  // procs.Detached.Start
}

// Start is `start`: unless a supervisor holds the lock, remove the stop file and launch
// `<self> supervisor serve --cwd <root>` in a session of its own (kept awake when enabled),
// its output appended to the supervisor log. The result is the JSON line start prints (exit
// 0); the error is the one the JS dies of (exit 1), or a lock that cannot be probed.
//
// PE-30: held means the kernel says a process holds the lock (ownlock.Probe), and the pid
// reported is the kernel's holder; a lock file no process holds is a supervisor that is gone,
// whatever pid its text names. The probe is advisory: serve's Acquire decides, so of two
// starts that both probe "free", one serve claims the project and the other exits 1.
func (p Project) Start(st Starter) (*jsjson.Object, error) {
	if err := nodefs.MkdirPrivate(filepath.Dir(p.LogPath)); err != nil {
		return nil, err
	}
	held, holder, err := ownlock.Probe(p.LockPath)
	if err != nil {
		return nil, err
	}
	if held {
		running := jsjson.NewObject()
		running.Set("started", false)
		running.Set("reason", "already running")
		running.Set("pid", float64(holder))
		return running, nil
	}
	_ = syscall.Unlink(p.StopPath)
	// Owner-only and never through a planted symlink (PE-21): the JS opens it 0666 less the umask.
	log, err := nodefs.OpenPrivateAppend(p.LogPath)
	if err != nil {
		return nil, err
	}
	defer log.Close() // serve holds its own copy of the descriptor
	cmd, args := st.KeepAwake(st.Self, []string{"supervisor", "serve", "--cwd", p.Root})
	pid, err := st.Detach(procs.Detached{Name: cmd, Args: args, Dir: p.Root, Output: log})
	var notStarted *procs.StartError
	if err != nil && !(errors.As(err, &notStarted) && notStarted.Code != "") {
		return nil, err // Node's spawn throws these synchronously
	}
	started := jsjson.NewObject()
	started.Set("started", true)
	if err == nil {
		// A command that cannot be started leaves supervisor_pid out: its async error event
		// never runs before the JS exits.
		started.Set("supervisor_pid", float64(pid))
	}
	started.Set("log", p.LogPath)
	return started, nil
}
