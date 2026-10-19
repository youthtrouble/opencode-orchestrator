package cli

import (
	"fmt"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/supervisor"
)

var supervisorValueKeys = []string{"cwd", "event", "by"}

// Supervisor implements `orch supervisor status|stop|ack` (bin/supervisor.mjs), byte-identical
// to the JS except for the parity exceptions in go/README.md (hand-edited non-object shapes,
// PE-16). start and serve land in S8c. As in the JS, an unknown command prints nothing and
// exits 0.
func Supervisor(args []string, stdio Stdio) int {
	opts, positional := cliargs.ParseArgs(decodeArgv(args), supervisorValueKeys, nil)
	root, err := projectRoot(opts) // before any command, as the JS resolves it at the top
	if err != nil {
		return crash(stdio, err)
	}
	project := supervisor.NewProject(root)
	command := ""
	if len(positional) > 0 {
		command = positional[0]
	}
	switch command {
	case "status":
		return printJSON(stdio, "supervisor", project.Status(), "  ")
	case "stop":
		line, err := project.Stop()
		if err != nil {
			return crash(stdio, err)
		}
		fmt.Fprintln(stdio.Out, line)
	case "ack":
		return supervisorAck(project, opts, stdio)
	case "start", "serve":
		fmt.Fprintf(stdio.Err, "orch supervisor %s: not implemented yet (S8c)\n", command)
		return 2
	case "":
		fmt.Fprintln(stdio.Err, "commands: start | serve | status | stop | ack --event ID")
		return 2
	}
	return 0
}

func supervisorAck(project supervisor.Project, opts cliargs.Opts, stdio Stdio) int {
	result, err := project.Ack(opts.JS("event"), opts.Or("by", "coordinator"), time.Now())
	switch {
	case err != nil:
		return crash(stdio, err)
	case result.Refused:
		fmt.Fprintln(stdio.Err, result.Line)
		return 1
	}
	fmt.Fprintln(stdio.Out, result.Line)
	return 0
}
