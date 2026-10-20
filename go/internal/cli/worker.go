package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/keepawake"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

var (
	workerValueKeys = []string{"brief", "harness", "lane", "model", "effort", "sandbox", "permission-mode", "allowed-tools", "resume", "breadth", "cwd", "max-turns", "timeout-min", "log-path", "run-id", "agent"}
	workerFlagKeys  = []string{"detach", "force", "help"}
)

// errResolveArgBoolean is path.resolve(true), the JS for a bare --cwd.
var errResolveArgBoolean = &jsTypeError{"ERR_INVALID_ARG_TYPE", cliargs.ErrPathArgBoolean.Error()}

// Worker implements `orch worker start|status|stop` (bin/worker.mjs), byte-identical to the JS
// except for the parity exceptions in go/README.md (stop signals only pids of at least 2 that
// hold the lane's lock, PE-14 and PE-23).
func Worker(args []string, stdio Stdio) int {
	argv := decodeArgv(args) // process.argv.slice(2): parsed here, and passed on by --detach
	opts, positional := cliargs.ParseArgs(argv, workerValueKeys, workerFlagKeys)
	root, err := projectRoot(opts)
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
	return workerStart(project, opts, argv, stdio)
}

// workerStart validates the start, then runs it in the foreground with the bridges of this
// executable, or, with --detach, launches this executable as its detached runner. argv is the
// unparsed command line as the JS reads it, which the runner receives.
func workerStart(project worker.Project, opts cliargs.Opts, argv []string, stdio Stdio) int {
	launch, err := project.PrepareStart(opts)
	if err != nil {
		return workerFailed(stdio, err)
	}
	self, err := os.Executable()
	if err != nil {
		return workerCrash(stdio, err)
	}
	if opts.Truthy("detach") {
		return workerDetach(launch, self, argv, stdio)
	}
	code, err := launch.Foreground(worker.Runtime{
		Self:   self,
		Pid:    os.Getpid(),
		Stdout: stdio.Out,
		Stderr: stdio.Err,
		Now:    time.Now,
		NewID:  mailbox.NewUUID,
		Notify: func(c chan<- os.Signal) { signal.Notify(c, syscall.SIGINT, syscall.SIGTERM) },
	})
	if err != nil {
		return workerCrash(stdio, err)
	}
	return code
}

// workerDetach launches the detached runner, kept awake unless the registry turns it off.
func workerDetach(launch *worker.Launch, self string, argv []string, stdio Stdio) int {
	code, err := launch.Detach(worker.DetachRuntime{
		Self:   self,
		Argv:   argv,
		Stdout: stdio.Out,
		Stderr: stdio.Err,
		NewID:  mailbox.NewUUID,
		KeepAwake: func(cmd string, args []string) (string, []string) {
			return keepawake.Wrap(cmd, args, worker.KeepAwakeEnabled(os.Getenv))
		},
	})
	if err != nil {
		return workerCrash(stdio, err)
	}
	return code
}

// workerFailed prints why a worker command did not run: a refusal (*worker.UsageError) on
// stderr with exit 2, else the exception the JS dies of.
func workerFailed(stdio Stdio, err error) int {
	var refused *worker.UsageError
	if errors.As(err, &refused) {
		fmt.Fprintln(stdio.Err, refused.Message)
		return 2
	}
	return workerCrash(stdio, err)
}

// workerCrash prints the error line of the exception a worker dies of: the worker's own
// TypeErrors carry their class, the rest are Errors.
func workerCrash(stdio Stdio, err error) int {
	var thrown *worker.JSError
	if errors.As(err, &thrown) {
		fmt.Fprintln(stdio.Err, thrown.Error())
		return 1
	}
	return crash(stdio, err)
}

// projectRoot is `path.resolve(opts.cwd || process.cwd())`, the project root of worker and
// flip: resolving "" gives the working directory too, and a bare --cwd is Node's TypeError.
func projectRoot(opts cliargs.Opts) (string, error) {
	if opts.IsFlag("cwd") {
		return "", errResolveArgBoolean
	}
	cwd, _ := opts.Value("cwd")
	return nodefs.Resolve(cwd)
}

func workerStatus(project worker.Project, lane any, stdio Stdio) int {
	lanes, err := project.Status(lane)
	if err != nil {
		return workerFailed(stdio, err)
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
		return workerFailed(stdio, err)
	}
	return printLine(stdio, result)
}
