package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/keepawake"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/supervisor"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

var supervisorValueKeys = []string{"cwd", "event", "by"}

// Supervisor implements `orch supervisor status|stop|ack` (bin/supervisor.mjs), byte-identical
// to the JS except for the parity exceptions in go/README.md (hand-edited non-object shapes,
// PE-16; a serve that outlives a missing opencode, PE-17). As in the JS, an unknown command
// prints nothing and exits 0.
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
	case "status", "stop", "ack":
		if code, ok := requireProject(stdio, root, "--cwd"); !ok {
			return code
		}
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
	case "start":
		return supervisorStart(project, stdio)
	case "serve":
		return supervisorServe(project, stdio)
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

// supervisorStart launches `orch supervisor serve` detached, kept awake unless the registry
// turns it off, and prints the compact launch line.
func supervisorStart(project supervisor.Project, stdio Stdio) int {
	self, err := os.Executable()
	if err != nil {
		return crash(stdio, err)
	}
	launched, err := project.Start(supervisor.Starter{
		Self: self,
		KeepAwake: func(cmd string, args []string) (string, []string) {
			return keepawake.Wrap(cmd, args, worker.KeepAwakeEnabled(os.Getenv))
		},
		Detach: procs.Detached.Start,
	})
	if err != nil {
		return crash(stdio, err)
	}
	data, err := jsjson.Marshal(launched)
	if err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}

// supervisorServe runs the supervisor loop in this process until the stop file appears.
func supervisorServe(project supervisor.Project, stdio Stdio) int {
	self, err := os.Executable()
	if err != nil {
		return crash(stdio, err)
	}
	code, err := supervisor.New(project, supervisorDeps(project.Root, self)).Serve(stdio.Err)
	if err != nil {
		return crash(stdio, err)
	}
	return code
}

// childGrace bounds, for every child serve waits for, the wait before SIGKILL once its
// timeout's SIGTERM was sent and the wait for pipes held by grandchildren (PE-10 ext.).
const childGrace = 10 * time.Second

// supervisorDeps are the real processes, clock, mailbox and incident log of serve.
func supervisorDeps(root, self string) supervisor.Deps {
	return supervisor.Deps{
		Now:   time.Now,
		Pid:   os.Getpid(),
		Self:  self,
		Alive: worker.Alive,
		RunSync: func(spec supervisor.SyncSpec) supervisor.SyncResult {
			spec.Grace = childGrace
			return procs.RunSync(spec)
		},
		Spawn: func(name string, args []string, dir string) error {
			_, err := procs.Detached{Name: name, Args: args, Dir: dir}.StartReaped()
			return err
		},
		Mailbox:  func() *mailbox.Mailbox { return mailbox.New(root) },
		Incident: incident.TryRecord,
		Sleep:    time.Sleep,
	}
}
