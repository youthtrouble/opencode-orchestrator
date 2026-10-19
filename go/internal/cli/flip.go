package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

var (
	flipValueKeys = []string{"active", "to", "from", "cwd", "reason", "resume", "question"}
	flipFlagKeys  = []string{"force", "clear"}
)

// launcherGrace bounds how long `flip run` waits for the launcher's pipes once it has exited
// (PE-10); spawnSync waits for them forever.
const launcherGrace = 10 * time.Second

// Flip implements `orch flip decide|run|pause|gate|status` (bin/flip.mjs), byte-identical to
// the JS except for the parity exceptions in go/README.md (decide reads flip.json's at in the
// ECMAScript date formats only, PE-15; run launches the conductor as `orch worker`).
func Flip(args []string, stdio Stdio) int {
	opts, positional := cliargs.ParseArgs(decodeArgv(args), flipValueKeys, flipFlagKeys)
	root, err := projectRoot(opts) // before any command, as the JS resolves it at the top
	if err != nil {
		return crash(stdio, err)
	}
	project := flipcmd.NewProject(root)
	command := ""
	if len(positional) > 0 {
		command = positional[0]
	}
	switch command {
	case "status":
		return printJSON(stdio, "flip", project.Status(), "  ")
	case "pause":
		answer, err := project.Pause(opts)
		if err != nil {
			return crash(stdio, err)
		}
		return printLine(stdio, answer)
	case "gate":
		answer, err := project.Gate(opts)
		if err != nil {
			return crash(stdio, err)
		}
		return printLine(stdio, answer)
	case "decide":
		return flipDecide(project, opts, stdio)
	case "run":
		return flipRun(project, opts, stdio)
	}
	fmt.Fprintln(stdio.Err, "commands: decide | run | pause | gate [--clear] | status")
	return 2
}

// flipDecide prints the decision. A routing failure exits 1 as `orch usage` reports it (the
// JS dies of the rejection).
func flipDecide(project flipcmd.Project, opts cliargs.Opts, stdio Stdio) int {
	decision, err := project.Decide(opts, func() (*usage.Routing, error) {
		cfg, err := usageConfig()
		if err != nil {
			return nil, err
		}
		return usage.GetRouting(cfg, false)
	})
	switch {
	case errors.Is(err, flipcmd.ErrActiveRequired):
		fmt.Fprintln(stdio.Err, err)
		return 2
	case err != nil:
		return fail(stdio, "flip", err)
	}
	return printLine(stdio, decision.JSON())
}

// flipRun hands the project over and prints the answer.
func flipRun(project flipcmd.Project, opts cliargs.Opts, stdio Stdio) int {
	answer, code, err := project.Run(opts, workerLauncher(project.Root))
	switch {
	case errors.Is(err, flipcmd.ErrToRequired):
		fmt.Fprintln(stdio.Err, err)
		return 2
	case err != nil:
		return crash(stdio, err)
	}
	if printed := printLine(stdio, answer); printed != 0 {
		return printed
	}
	return code
}

// workerLauncher runs `orch worker start ...` from the project root as the JS spawnSync runs
// worker.mjs: this executable, the inherited environment, no input, both outputs captured and
// decoded as UTF-8. A launcher that cannot be started gives no status and no output, as
// spawnSync's error result does.
func workerLauncher(root string) flipcmd.Launcher {
	return func(args []string) (*int, string, string) {
		self, err := os.Executable()
		if err != nil {
			return nil, "", ""
		}
		result := procs.RunSync(procs.SyncSpec{
			Name:  self,
			Args:  append([]string{"worker"}, args...),
			Dir:   root,
			Grace: launcherGrace,
		})
		return result.Status, result.Stdout, result.Stderr
	}
}
