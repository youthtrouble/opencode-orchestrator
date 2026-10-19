// Command orch is the Go runtime of the orchestrator: `orch <subcommand> [args]`, one
// subcommand per orchestrator/bin/*.mjs script. The exit code is the subcommand's return.
package main

import (
	"fmt"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cli"
)

// commands maps subcommand names to implementations. json-echo is hidden from the usage text:
// it exists for the parity harness only.
var commands = map[string]cli.Command{
	"version":       cli.Version,
	"usage":         cli.Usage,
	"routing":       cli.Routing,
	"budget":        cli.Budget,
	"effort":        cli.Effort,
	"mailbox":       cli.Mailbox,
	"manifest":      cli.Manifest,
	"incident":      cli.Incident,
	"policy":        cli.Policy,
	"claude-exec":   cli.ClaudeExec,
	"codex-exec":    cli.CodexExec,
	"opencode-exec": cli.OpencodeExec,
	"worker":        cli.Worker,
	"flip":          cli.Flip,
	"supervisor":    cli.Supervisor,
	"doctor":        cli.Doctor,
	"json-echo":     cli.JSONEcho,
}

const usage = "usage: orch <command> [args]\ncommands: version, usage, routing, budget, effort, mailbox, manifest, incident, policy, claude-exec, codex-exec, opencode-exec, worker, flip, supervisor, doctor"

func main() {
	os.Exit(run(os.Args[1:], cli.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}

func run(args []string, stdio cli.Stdio) int {
	if len(args) == 0 {
		fmt.Fprintln(stdio.Err, usage)
		return 2
	}
	command, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stdio.Err, "orch: unknown command %q\n%s\n", args[0], usage)
		return 2
	}
	return command(args[1:], stdio)
}
