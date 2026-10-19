package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// Starter is what `start` needs to launch a detached `serve`. The CLI wires the real ones;
// tests inject them, so no unit test starts a daemon.
type Starter struct {
	Self      string                                             // this executable
	Alive     func(pid any) bool                                 // worker.Alive
	KeepAwake func(cmd string, args []string) (string, []string) // keepawake.Wrap, enabled per registry
	Detach    func(procs.Detached) (int, error)                  // procs.Detached.Start
}

// Start is `start`: unless a live supervisor holds the lock, remove the stop file and launch
// `<self> supervisor serve --cwd <root>` in a session of its own (kept awake when enabled),
// its output appended to the supervisor log. The result is the JSON line start prints (exit
// 0); the error is the one the JS dies of (exit 1).
func (p Project) Start(st Starter) (*jsjson.Object, error) {
	if err := nodefs.MkdirAll(filepath.Dir(p.LogPath)); err != nil {
		return nil, err
	}
	if holder := p.lockHolder(); st.Alive(holder) {
		running := jsjson.NewObject()
		running.Set("started", false)
		running.Set("reason", "already running")
		running.Set("pid", holder)
		return running, nil
	}
	_ = syscall.Unlink(p.StopPath)
	log, err := os.OpenFile(p.LogPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return nil, nodefs.FileError(err, "open", p.LogPath)
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
