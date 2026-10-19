package cli

import (
	"fmt"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

var (
	workerValueKeys = []string{"brief", "harness", "lane", "model", "effort", "sandbox", "permission-mode", "allowed-tools", "resume", "breadth", "cwd", "max-turns", "timeout-min", "log-path", "run-id", "agent"}
	workerFlagKeys  = []string{"detach", "force", "help"}
)

// errResolveArgBoolean is path.resolve(true), the JS for a bare --cwd.
var errResolveArgBoolean = &jsTypeError{"ERR_INVALID_ARG_TYPE", cliargs.ErrPathArgBoolean.Error()}

// errWorkerStartPending is what `orch worker start` prints until slice S6b ports it.
const errWorkerStartPending = "orch worker start: not implemented yet (S6b)"

// Worker implements `orch worker status|stop` (bin/worker.mjs), byte-identical to the JS
// except that stop signals only pids of at least 2 (PE-14). `start` is not ported yet: given
// the brief and harness it would run, it prints errWorkerStartPending and exits 2.
func Worker(args []string, stdio Stdio) int {
	opts, positional := cliargs.ParseArgs(decodeArgv(args), workerValueKeys, workerFlagKeys)
	root, err := workerRoot(opts)
	if err != nil {
		return crash(stdio, err)
	}
	project := worker.NewProject(root)
	command := ""
	if len(positional) > 0 {
		command = positional[0]
	}
	switch {
	case command == "status":
		return workerStatus(project, opts.JS("lane"), stdio)
	case command == "stop":
		return workerStop(project, opts.JS("lane"), stdio)
	case command != "start" || !opts.Truthy("brief") || !opts.Truthy("harness"):
		fmt.Fprintf(stdio.Err, "usage: %s start --brief FILE --harness codex|claude [--lane L] [...] | status | stop --lane L\n", cmdref.ScriptFile("worker"))
		return 2
	}
	fmt.Fprintln(stdio.Err, errWorkerStartPending)
	return 2
}

// workerRoot is `path.resolve(opts.cwd || process.cwd())`: resolving "" gives the working
// directory too.
func workerRoot(opts cliargs.Opts) (string, error) {
	if opts.IsFlag("cwd") {
		return "", errResolveArgBoolean
	}
	cwd, _ := opts.Value("cwd")
	return nodefs.Resolve(cwd)
}

func workerStatus(project worker.Project, lane any, stdio Stdio) int {
	lanes, err := project.Status(lane)
	if err != nil {
		return crash(stdio, err)
	}
	data, err := jsjson.MarshalIndent(lanes, "  ")
	if err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}

func workerStop(project worker.Project, lane any, stdio Stdio) int {
	result, err := project.Stop(lane)
	if err != nil {
		return crash(stdio, err)
	}
	return printLine(stdio, result)
}
