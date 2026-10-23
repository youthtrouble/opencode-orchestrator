// Package kickoff starts a run as bin/kickoff.mjs does: check the briefs, refresh the usage,
// pick the door by live budget, run the preflight (policy, gate, supervisor, git exclude) and
// launch the conductor, headless through `orch worker start --detach` or interactive in this
// terminal. A dry run prints the door and prompt and changes nothing in the project (it still
// refreshes the shared usage cache). The orchestrator commands it runs are self-execs of orch,
// as the JS spawns node on its sibling scripts; every child is reached through Children so
// each branch can be tested without processes.
package kickoff

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// exitBothRed is the exit code when neither vendor can run the conductor (EX_TEMPFAIL).
const exitBothRed = 75

// dryRunSupervisorLine stands in for the supervisor's answer in a dry run, which starts nothing,
// so its output has the shape of a real run's.
const dryRunSupervisorLine = "supervisor: not started (dry-run)"

// notDeferred is the error line of a both-red run that could not set up the relaunch at the
// reset; %s says which step failed.
const notDeferred = "Both vendors are red, but %s: nothing will relaunch at the reset. Fix it and run kickoff again.\n"

// conductorBrief is where a headless or both-red kickoff writes the conductor's prompt,
// relative to the root.
var conductorBrief = filepath.Join(".orchestrator", "briefs", "conductor-kickoff.md")

// Args is kickoff's command line: the --brief values in order and the other options.
type Args struct {
	Briefs []string
	Opts   cliargs.Opts
}

// ParseArgv splits argv as the two JS loops do. A --brief takes the next token when it is
// not empty (so `--brief --brief x` names the brief "--brief"); every --brief and every token
// right after one is then dropped before the options are parsed, taken or not.
func ParseArgv(argv []string) Args {
	var briefs []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--brief" && i+1 < len(argv) && argv[i+1] != "" {
			i++
			briefs = append(briefs, argv[i])
		}
	}
	rest := make([]string, 0, len(argv))
	for i, arg := range argv {
		if arg == "--brief" || (i > 0 && argv[i-1] == "--brief") {
			continue
		}
		rest = append(rest, arg)
	}
	opts, _ := cliargs.ParseArgs(rest, []string{"door", "cwd"}, []string{"headless", "dry-run", "help", "unattended"})
	return Args{Briefs: briefs, Opts: opts}
}

// Children runs kickoff's child processes.
type Children interface {
	// Self runs `orch <args>` to completion with its output captured, as spawnSync of a
	// sibling script with encoding utf8 does.
	Self(args []string) procs.SyncResult
	// SelfInherited runs `orch <args>` to completion writing to this process's stdout and
	// stderr (spawnSync with stdio inherit) and returns its exit status, nil when it could not
	// be started, a signal ended it or its status could not be collected.
	SelfInherited(args []string) *int
	// Interactive runs name with args in the project root with this process's stdin, stdout,
	// stderr and environment and returns its exit code, nil when a signal ended it. A child
	// that cannot be started is a *procs.StartError.
	Interactive(name string, args []string) (*int, error)
}

// Deps is what a kickoff needs from its surroundings. Root is the resolved project root.
type Deps struct {
	Root     string
	Routing  func() (*usage.Routing, error) // getRouting({refresh: true})
	Children Children
	Out, Err io.Writer
}

// RoutingError is a failed getRouting, which the CLI reports as `orch kickoff: <error>` (PE-2)
// where the JS dies of the rejection.
type RoutingError struct{ Err error }

func (e *RoutingError) Error() string { return e.Err.Error() }
func (e *RoutingError) Unwrap() error { return e.Err }

// errGateNotCleared is a preflight whose gate clear was refused, already reported on stderr:
// the kickoff exits 1 with nothing after the clear run (PE-61).
var errGateNotCleared = errors.New("the human gate could not be cleared")

// Run performs the kickoff and returns the exit code. An error is what the JS dies of: a
// *RoutingError, a file error or a launch that could not start. Invalid doors are usage errors.
func Run(args Args, d Deps) (int, error) {
	if args.Opts.Truthy("help") || len(args.Briefs) == 0 {
		fmt.Fprintln(d.Out, cmdref.KickoffUsage())
		if len(args.Briefs) > 0 {
			return 0, nil
		}
		return 2, nil
	}
	if _, present := args.Opts["door"]; present {
		door, valid := args.Opts.Value("door")
		if !valid || (door != "opencode" && door != "claude" && door != "auto") {
			fmt.Fprintln(d.Err, "kickoff: --door must be opencode|claude|auto")
			return 2, nil
		}
	}
	for _, brief := range args.Briefs {
		if !nodefs.Exists(resolveIn(d.Root, brief)) {
			fmt.Fprintf(d.Err, "brief not found: %s (looked in %s; cd into the project or pass --cwd <project dir>)\n", brief, d.Root)
			return 2, nil
		}
	}
	r, err := d.Routing()
	if err != nil {
		return 0, &RoutingError{err}
	}
	home := homeDoor(r.Registry)
	door, chosen := chooseDoor(args.Opts, home, &r.Resolved.Postures)
	fmt.Fprintln(d.Out, promptHead(r.Prompt))
	prompt := cmdref.KickoffPrompt(args.Briefs[0], args.Briefs[1:])
	if !chosen {
		return bothRed(args.Opts, home, prompt, d)
	}
	if args.Opts.Truthy("dry-run") {
		fmt.Fprintln(d.Out, dryRunSupervisorLine)
	} else if _, err := preflight(args.Opts, d); errors.Is(err, errGateNotCleared) {
		return 1, nil
	} else if err != nil {
		return 0, err
	}
	line, err := doorLine(door, home, args.Opts.Truthy("headless"), &r.Resolved.Postures)
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(d.Out, line)
	switch {
	case args.Opts.Truthy("dry-run"):
		fmt.Fprintf(d.Out, "prompt: %s\n", prompt)
		return 0, nil
	case args.Opts.Truthy("headless"):
		return launchHeadless(door, prompt, d)
	}
	return launchInteractive(door, prompt, d)
}

// resolveIn is path.resolve(root, p).
func resolveIn(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// homeDoor is `registry.conductor?.home || "opencode"`.
func homeDoor(registry *jsjson.Object) string {
	if home := jsjson.Path(jsjson.Path(registry, "conductor"), "home"); jsjson.Truthy(home) {
		return jsjson.TemplateString(home)
	}
	return flip.DoorOpencode
}

// chooseDoor takes a validated explicit door; otherwise the home door if its
// vendor is usable, else the other door if usable. chosen is false when neither is.
func chooseDoor(opts cliargs.Opts, home string, postures *routing.Postures) (door string, chosen bool) {
	if given := opts.JS("door"); jsjson.Truthy(given) && given != "auto" {
		return jsjson.TemplateString(given), true
	}
	for _, candidate := range []string{home, flip.OtherDoor(home)} {
		if usable(candidate, postures) {
			return candidate, true
		}
	}
	return "", false
}

// usable reports that the door's vendor is available and not red; a name that is no door has
// no posture.
func usable(door string, postures *routing.Postures) bool {
	vendor, known := flip.DoorVendor[door]
	if !known {
		return false
	}
	p := postures.ByVendor(vendor)
	return p.Available && p.Overall != routing.Red
}

// promptHead is the first two lines of the routing prompt.
func promptHead(prompt string) string {
	lines := strings.Split(prompt, "\n")
	return strings.Join(lines[:min(2, len(lines))], "\n")
}

// bothRed handles a run neither vendor can take: a dry run only says so. A real one runs the
// preflight (so a supervisor is running to relaunch at the reset), saves the prompt as the
// conductor's brief, pauses the home door naming that brief through `orch flip pause` (whose
// answer reaches this stdout), which the supervisor's relaunch (`flip run`) then starts from,
// and says so. The gate clear in the preflight drops any older pause, so it must come first.
// Exit 75 promises that relaunch, so a refused gate clear or a supervisor that did not start
// (both before the brief is written) or a pause that was not recorded is an ordinary failure
// instead: exit 1.
func bothRed(opts cliargs.Opts, home, prompt string, d Deps) (int, error) {
	if opts.Truthy("dry-run") {
		fmt.Fprintln(d.Out, "dry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset.")
		return exitBothRed, nil
	}
	supervising, err := preflight(opts, d)
	if errors.Is(err, errGateNotCleared) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if !supervising {
		fmt.Fprintf(d.Err, notDeferred, "the supervisor did not start")
		return 1, nil
	}
	if err := writeConductorBrief(prompt, d.Root); err != nil {
		return 0, err
	}
	pause := d.Children.SelfInherited([]string{"flip", "pause", "--active", home, "--brief", conductorBrief, "--cwd", d.Root})
	if pause == nil || *pause != 0 {
		fmt.Fprintf(d.Err, notDeferred, "the pause was not recorded")
		return 1, nil
	}
	fmt.Fprintln(d.Out, "Both vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do.")
	return exitBothRed, nil
}

// preflight runs in the JS order, never for a dry run: --unattended applies the unattended
// policy, a human gate is cleared (a human is running this), the supervisor is started and
// its answer printed, and .orchestrator/ is excluded from git. The policy's result is
// discarded, as the JS does. The gate is cleared with --no-relaunch: kickoff starts the
// conductor itself (or, both red, records its own pause), so the clear also drops any queued
// relaunch or pause, which would let the supervisor start an older conductor or a second one
// beside an interactive kickoff. A refused clear stops the preflight with errGateNotCleared
// (see gateCleared). supervising reports that the supervisor is running: `start` exits 0 when
// it started one or found one.
func preflight(opts cliargs.Opts, d Deps) (supervising bool, err error) {
	if opts.Truthy("unattended") {
		d.Children.Self([]string{"policy", "unattended", "--cwd", d.Root})
	}
	gateClear := d.Children.Self([]string{"flip", "gate", "--clear", "--no-relaunch", "--cwd", d.Root})
	if !gateCleared(gateClear, d.Root) {
		fmt.Fprintf(d.Err, "kickoff: %s: %s; nothing was launched\n", errGateNotCleared, childDiagnostic(gateClear))
		return false, errGateNotCleared
	}
	supervisor := d.Children.Self([]string{"supervisor", "start", "--cwd", d.Root})
	fmt.Fprintf(d.Out, "supervisor: %s\n", jsstr.Slice(trim(stdoutOrStderr(supervisor)), 0, 120))
	if _, err := flipcmd.EnsureGitExclude(d.Root); err != nil {
		return false, err
	}
	return supervisor.Status != nil && *supervisor.Status == 0, nil
}

// gateCleared reports that the gate clear left no gate in the way: it exited 0, or it exited 2
// (not a project) and the root still has no .orchestrator/ directory to hold a gate, as in a
// first kickoff. Anything else, an unreadable flip.json (exit 1) included, is a refusal
// (PE-61; the JS ignores the clear's result).
func gateCleared(gateClear procs.SyncResult, root string) bool {
	switch {
	case gateClear.Status == nil:
		return false
	case *gateClear.Status == 0:
		return true
	case *gateClear.Status == 2:
		var notAProject *paths.NotAProjectError
		return errors.As(paths.RequireProject(root, "--cwd"), &notAProject)
	}
	return false
}

// childDiagnostic is the first non-empty line of a child's stderr, else of its stdout, else
// why it gave no output: the start error, its exit status or that it had none.
func childDiagnostic(result procs.SyncResult) string {
	for _, output := range []string{result.Stderr, result.Stdout} {
		for line := range strings.SplitSeq(output, "\n") {
			if line = trim(line); line != "" {
				return line
			}
		}
	}
	switch {
	case result.Err != nil:
		return result.Err.Error()
	case result.Status != nil:
		return fmt.Sprintf("exit status %d", *result.Status)
	}
	return "no exit status"
}

// doorLine is `door: <door> (<vendor> <overall>; home <home>) headless|interactive`. A door
// with no vendor has no posture to read: the JS TypeError.
func doorLine(door, home string, headless bool, postures *routing.Postures) (string, error) {
	vendor, known := flip.DoorVendor[door]
	if !known {
		return "", &jsjson.TypeError{Message: "Cannot read properties of undefined (reading 'overall')"}
	}
	mode := " interactive"
	if headless {
		mode = " headless"
	}
	return fmt.Sprintf("door: %s (%s %s; home %s)%s", door, vendor, postures.ByVendor(vendor).Overall, home, mode), nil
}

// writeConductorBrief writes the prompt as the conductor's brief, creating its directory. The
// brief is replaced atomically and owner-only, never written through a symlink (PE-21).
func writeConductorBrief(prompt, root string) error {
	path := filepath.Join(root, conductorBrief)
	if err := nodefs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return nodefs.WritePrivateFile(path, []byte(prompt+"\n"))
}

// launchHeadless writes the prompt as the conductor's brief and starts a detached conductor
// lane through `orch worker start`, printing its answer and the watch hint. The exit code is
// the worker's, or 1 when it gave none.
func launchHeadless(door, prompt string, d Deps) (int, error) {
	if err := writeConductorBrief(prompt, d.Root); err != nil {
		return 0, err
	}
	args := []string{"worker", "start", "--brief", conductorBrief, "--lane", "conductor", "--harness", door, "--agent", "conductor", "--breadth", "broad", "--cwd", d.Root, "--detach"}
	if door == flip.DoorClaude {
		args = append(args, "--permission-mode", "auto")
	}
	worker := d.Children.Self(args)
	fmt.Fprintln(d.Out, trim(stdoutOrStderr(worker)))
	fmt.Fprintf(d.Out, "watch with: %s\n", cmdref.WatchFollow())
	if worker.Status == nil {
		return 1, nil
	}
	return *worker.Status, nil
}

// launchInteractive runs the door's TUI in this terminal and exits with its code, 0 when a
// signal ended it.
func launchInteractive(door, prompt string, d Deps) (int, error) {
	name, args := "opencode", []string{"--prompt", prompt}
	if door == flip.DoorClaude {
		name, args = "claude", []string{"--agent", "conductor", prompt}
	}
	fmt.Fprintf(d.Out, "launching: %s ... (watch in another terminal: %s)\n", name, cmdref.WatchFollow())
	code, err := d.Children.Interactive(name, args)
	if err != nil {
		return 0, err
	}
	if code == nil {
		return 0, nil
	}
	return *code, nil
}

// stdoutOrStderr is `result.stdout || result.stderr`.
func stdoutOrStderr(result procs.SyncResult) string {
	if result.Stdout != "" {
		return result.Stdout
	}
	return result.Stderr
}

// trim is String.prototype.trim.
func trim(s string) string { return strings.Trim(s, jsstr.WhiteSpace) }
