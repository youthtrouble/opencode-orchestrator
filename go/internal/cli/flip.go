package cli

import (
	"errors"
	"fmt"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

var (
	flipValueKeys = []string{"active", "to", "from", "cwd", "reason", "resume", "question"}
	flipFlagKeys  = []string{"force", "clear"}
)

// Flip implements `orch flip decide|pause|gate|status` (bin/flip.mjs), byte-identical to the
// JS except for the parity exceptions in go/README.md (decide reads flip.json's at in the
// ECMAScript date formats only, PE-15). `run` comes in S7b.
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
			return flipCrash(stdio, err)
		}
		return printLine(stdio, answer)
	case "gate":
		answer, err := project.Gate(opts)
		if err != nil {
			return flipCrash(stdio, err)
		}
		return printLine(stdio, answer)
	case "decide":
		return flipDecide(project, opts, stdio)
	case "run":
		fmt.Fprintln(stdio.Err, "orch flip run: not implemented yet (S7b)")
		return 2
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

// flipCrash prints the error line of the exception a flip command dies of: the script's own
// TypeError carries its class, Node's filesystem errors are Errors.
func flipCrash(stdio Stdio, err error) int {
	var typeError *flipcmd.PropertyTypeError
	if errors.As(err, &typeError) {
		fmt.Fprintf(stdio.Err, "TypeError: %v\n", typeError)
		return 1
	}
	return crash(stdio, err)
}
