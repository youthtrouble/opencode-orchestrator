// Package kickoff starts a run as bin/kickoff.mjs does: check the briefs, refresh the usage,
// pick the door by live budget, run the preflight (policy, gate, supervisor, git exclude) and
// launch the conductor, headless through `orch worker start --detach` or interactive in this
// terminal. The orchestrator commands it runs are self-execs of orch, as the JS spawns node
// on its sibling scripts; every child is reached through Children so each branch can be
// tested without processes.
package kickoff

import (
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
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// exitBothRed is the exit code when neither vendor can run the conductor (EX_TEMPFAIL).
const exitBothRed = 75

// briefFile is where a headless kickoff writes the conductor's prompt, relative to the root.
var briefFile = filepath.Join(".orchestrator", "briefs", "conductor-kickoff.md")

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
	// stderr (spawnSync with stdio inherit).
	SelfInherited(args []string)
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

// Run performs the kickoff and returns the exit code. An error is what the JS dies of: a
// *RoutingError, the TypeError of an unknown door, a file error or a launch that could not
// start.
func Run(args Args, d Deps) (int, error) {
	if args.Opts.Truthy("help") || len(args.Briefs) == 0 {
		fmt.Fprintln(d.Out, cmdref.KickoffUsage())
		if len(args.Briefs) > 0 {
			return 0, nil
		}
		return 2, nil
	}
	for _, brief := range args.Briefs {
		if !nodefs.Exists(resolveIn(d.Root, brief)) {
			fmt.Fprintf(d.Err, "brief not found: %s\n", brief)
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
	if !chosen {
		return bothRed(args.Opts, home, d), nil
	}
	if err := preflight(args.Opts, d); err != nil {
		return 0, err
	}
	prompt := cmdref.KickoffPrompt(args.Briefs[0], args.Briefs[1:])
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

// chooseDoor is the JS door choice: a given --door other than "auto" is taken unvalidated (a
// bare --door is the text "true", which no door matches); otherwise the home door if its
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

// bothRed handles a run neither vendor can take: a dry run only says so; a real one pauses the
// home door through `orch flip pause` (whose answer reaches this stdout) and says so.
func bothRed(opts cliargs.Opts, home string, d Deps) int {
	if opts.Truthy("dry-run") {
		fmt.Fprintln(d.Out, "dry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset.")
		return exitBothRed
	}
	d.Children.SelfInherited([]string{"flip", "pause", "--active", home, "--cwd", d.Root})
	fmt.Fprintln(d.Out, "Both vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do.")
	return exitBothRed
}

// preflight runs in the JS order, for a dry run too: --unattended applies the unattended
// policy, a human gate is cleared (a human is running this), the supervisor is started and
// its answer printed, and .orchestrator/ is excluded from git. The policy and gate outputs
// are discarded.
func preflight(opts cliargs.Opts, d Deps) error {
	if opts.Truthy("unattended") {
		d.Children.Self([]string{"policy", "unattended", "--cwd", d.Root})
	}
	d.Children.Self([]string{"flip", "gate", "--clear", "--cwd", d.Root})
	supervisor := d.Children.Self([]string{"supervisor", "start", "--cwd", d.Root})
	fmt.Fprintf(d.Out, "supervisor: %s\n", jsstr.Slice(trim(stdoutOrStderr(supervisor)), 0, 120))
	_, err := flipcmd.EnsureGitExclude(d.Root)
	return err
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

// launchHeadless writes the prompt as the conductor's brief and starts a detached conductor
// lane through `orch worker start`, printing its answer and the watch hint. The exit code is
// the worker's, or 1 when it gave none.
func launchHeadless(door, prompt string, d Deps) (int, error) {
	path := filepath.Join(d.Root, briefFile)
	if err := nodefs.MkdirAll(filepath.Dir(path)); err != nil {
		return 0, err
	}
	if err := nodefs.WriteFile(path, []byte(prompt+"\n")); err != nil {
		return 0, err
	}
	args := []string{"worker", "start", "--brief", briefFile, "--lane", "conductor", "--harness", door, "--agent", "conductor", "--breadth", "broad", "--cwd", d.Root, "--detach"}
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
