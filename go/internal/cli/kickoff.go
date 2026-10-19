package cli

import (
	"errors"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/kickoff"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// Kickoff implements `orch kickoff --brief FILE [--brief FILE2 ...] [--door auto|claude|opencode]
// [--headless] [--unattended] [--cwd DIR] [--dry-run]`, bin/kickoff.mjs. A routing failure
// prints `orch kickoff: <error>` and exits 1 (PE-2); the JS TypeErrors are crashes with the
// JS message.
func Kickoff(args []string, stdio Stdio) int {
	parsed := kickoff.ParseArgv(decodeArgv(args))
	root, err := projectRoot(parsed.Opts)
	if err != nil {
		return crash(stdio, err)
	}
	code, err := kickoff.Run(parsed, kickoff.Deps{
		Root: root,
		Routing: func() (*usage.Routing, error) {
			cfg, err := usageConfig()
			if err != nil {
				return nil, err
			}
			return usage.GetRouting(cfg, true)
		},
		Children: kickoffChildren{root: root, stdio: stdio},
		Out:      stdio.Out,
		Err:      stdio.Err,
	})
	var routingErr *kickoff.RoutingError
	switch {
	case errors.As(err, &routingErr):
		return fail(stdio, "kickoff", err)
	case err != nil:
		return crash(stdio, err)
	}
	return code
}

// kickoffChildren runs kickoff's children as the JS does: orch itself where the JS spawns node
// on a sibling script (from this working directory, with the inherited environment), and the
// door's TUI in the project root with this process's stdio.
type kickoffChildren struct {
	root  string
	stdio Stdio
}

// Self gives no status and no output when orch cannot find itself, as spawnSync's error
// result does.
func (c kickoffChildren) Self(args []string) procs.SyncResult {
	self, err := os.Executable()
	if err != nil {
		return procs.SyncResult{Err: err}
	}
	return procs.RunSync(procs.SyncSpec{Name: self, Args: args, Grace: launcherGrace})
}

func (c kickoffChildren) SelfInherited(args []string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	child, err := procs.Start(procs.Spec{Name: self, Args: args, Stdout: c.stdio.Out, Stderr: c.stdio.Err, Grace: launcherGrace})
	if err != nil {
		return
	}
	_, _ = child.Wait() // spawnSync's result is not read
}

func (c kickoffChildren) Interactive(name string, args []string) (*int, error) {
	child, err := procs.Start(procs.Spec{Name: name, Args: args, Dir: c.root, Stdin: c.stdio.In, Stdout: c.stdio.Out, Stderr: c.stdio.Err, Grace: launcherGrace})
	if err != nil {
		return nil, err
	}
	return child.Wait()
}
