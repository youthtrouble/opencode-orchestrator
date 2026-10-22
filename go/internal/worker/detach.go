package worker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
)

// DetachRuntime is what a detached launch takes from the process it runs in. Tests replace
// each.
type DetachRuntime struct {
	// Self is the executable the runner runs as: `<Self> worker <args> --log-path L --run-id R
	// --cwd ROOT` (the JS runs `node worker.mjs <args> ...`).
	Self string
	// Argv is the unparsed command line after `orch worker`, decoded as the JS reads
	// process.argv.slice(2).
	Argv           []string
	Stdout, Stderr io.Writer
	NewID          func() string // crypto.randomUUID
	// KeepAwake wraps the runner's command line so the machine stays awake while it runs
	// (keepAwakeWrap with the registry's runtime.keep_awake), or returns it unchanged.
	KeepAwake func(cmd string, args []string) (string, []string)
}

// Detach is `worker.mjs start --detach` (l.65-84). It refuses a lane whose lock is held,
// synchronously, as flip.mjs relies on: the lane-locked JSON without a hint on stderr, nothing
// on stdout, exit ExitLaneLocked. It then creates the run's log, starts the runner (this
// command without --detach, in the foreground, writing to the log) in a session of its own in
// the project root, and prints {lane, runner_pid, log_path, run_id, keep_awake}, exit 0. A
// runner that cannot be found or executed is not waited for in the JS (its error event never
// runs): runner_pid is left out and the exit is still 0. A returned error is the exception the
// JS dies of (exit 1).
//
// PE-29: the lane is held only while the kernel says a runner holds its lock (ownlock.Probe).
// The JS reads the pid in the lock and unlinks the file when that pid is dead, so two
// launchers could each remove the other's fresh lock and leave two runners on the lane. Here
// the launcher never touches the lock: a dead runner's file is left for the new runner's
// Acquire to reclaim. The Probe is advisory (C9); the runner's Acquire decides. When two
// launchers both find the lane free, both print their launch and exit 0, and the runner that
// loses refuses the lane into its log with exit 3. The loser's run_id is still sound for a
// caller that waits for the lane to be served: the winner started after the loser's Probe, so
// the lane has a runner.
func (l *Launch) Detach(rt DetachRuntime) (int, error) {
	held, _, err := ownlock.Probe(l.project.LockPath(l.lane))
	if err != nil {
		return 1, err
	}
	if held {
		// The JS prints alive: true, the holder's liveness, whatever the state's pid says.
		holderAlive := func(any) bool { return true }
		return l.printLaneLocked(rt.Stderr, previousWorker(l.project.ReadState(l.lane), holderAlive), "")
	}
	if err := nodefs.MkdirPrivate(l.project.LogsDir); err != nil {
		return 1, err
	}
	runID := rt.NewID()
	logPath := filepath.Join(l.project.LogsDir, laneText(l.lane)+"-"+jsstr.Slice(runID, 0, 8)+".log")
	log, err := createLog(logPath)
	if err != nil {
		return 1, err
	}
	defer log.Close() // the runner holds its own copy of the descriptor
	cmd, args := rt.KeepAwake(rt.Self, runnerArgs(rt.Argv, l.project.Root, logPath, runID))
	pid, err := procs.Detached{Name: cmd, Args: args, Dir: l.project.Root, Output: log}.Start()
	var notStarted *procs.StartError
	if err != nil && !(errors.As(err, &notStarted) && notStarted.Code != "") {
		return 1, err // Node's spawn throws these synchronously
	}
	launched := jsjson.NewObject()
	launched.Set("lane", l.lane)
	if err == nil {
		launched.Set("runner_pid", float64(pid))
	}
	launched.Set("log_path", logPath)
	launched.Set("run_id", runID)
	launched.Set("keep_awake", cmd != rt.Self)
	data, err := jsjson.Marshal(launched)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(rt.Stdout, "%s\n", data)
	return 0, nil
}

// createLog creates the run's log for writing only, failing if it exists (openSync(path, "wx",
// 0o600)).
func createLog(path string) (*os.File, error) {
	log, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, nodefs.FileError(err, "open", path)
	}
	return log, nil
}

// runnerArgs is the runner's command line after the executable: `worker`, the raw arguments
// without every token that is exactly "--detach" (even one that was another key's value, so
// that key takes the next token in the runner), then --log-path, --run-id and --cwd with the
// absolute project root, which win in the runner's parse by coming last. The runner already
// runs in the root, so a relative --cwd from the command line would be applied twice (Sol
// S2/R6).
func runnerArgs(argv []string, root, logPath, runID string) []string {
	args := []string{"worker"}
	for _, arg := range argv {
		if arg != "--detach" {
			args = append(args, arg)
		}
	}
	return append(args, "--log-path", logPath, "--run-id", runID, "--cwd", root)
}

// KeepAwakeEnabled is `loadRegistry().runtime?.keep_awake !== false`, where any failure to
// locate, read or parse the registry, or a registry that is not an object, counts as on.
func KeepAwakeEnabled(getenv func(string) string) bool {
	p, err := paths.Resolve(getenv)
	if err != nil {
		return true
	}
	reg, err := registry.Load(p.RegistryFile)
	if err != nil {
		return true
	}
	return jsjson.Path(reg, "runtime", "keep_awake") != false
}
