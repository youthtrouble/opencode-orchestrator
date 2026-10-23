package worker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// UsageError is a start the JS refuses before claiming its lane: it prints Message on stderr
// and exits 2.
type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }

// JSError is an exception the JS leaves uncaught whose class is not a plain Error: Node prints
// "<Class>: <Message>" (amid a stack) and exits 1.
type JSError struct{ Class, Message string }

func (e *JSError) Error() string { return e.Class + ": " + e.Message }

// errBriefArgBoolean is path.resolve(root, true), the JS for a bare --brief.
var errBriefArgBoolean = &JSError{Class: "TypeError [ERR_INVALID_ARG_TYPE]", Message: `The "paths[1]" argument must be of type string. Received type boolean (true)`}

// Harnesses are the harnesses a worker can run, each through its `orch <harness>-exec` bridge.
var Harnesses = []string{"codex", "claude", "opencode"}

// ExitLaneLocked is the exit code of a start whose lane is already locked.
const ExitLaneLocked = 3

// Launch is a validated `worker.mjs start`: the brief exists and ends in .md, and the harness
// is known.
type Launch struct {
	project Project
	opts    cliargs.Opts
	brief   string // absolute
	lane    any    // the --lane value, or the brief's base name without ".md"
	harness string
	cwd     string // child cwd, independent of the project that owns the lane
}

// PrepareStart is the validation of `worker.mjs start` that follows the usage check
// (worker.mjs l.59-63), in the JS order: the brief is resolved against the project root and
// must exist and end in ".md" (a directory "x.md" passes and fails later, when it is read);
// the lane is --lane or the brief's base name; the harness must be one of Harnesses; the lane
// must pass CheckLane. A refusal is a *UsageError; a bare --brief is the TypeError
// path.resolve throws.
func (p Project) PrepareStart(opts cliargs.Opts) (*Launch, error) {
	if opts.IsFlag("brief") {
		return nil, errBriefArgBoolean
	}
	briefArg, _ := opts.Value("brief")
	brief := resolveAgainst(p.Root, briefArg)
	if !nodefs.Exists(brief) || !strings.HasSuffix(brief, ".md") {
		return nil, &UsageError{"brief must be an existing .md file"}
	}
	lane := opts.Or("lane", strings.TrimSuffix(filepath.Base(briefArg), ".md"))
	harness, _ := opts.Value("harness")
	if opts.IsFlag("harness") || !slices.Contains(Harnesses, harness) {
		return nil, &UsageError{"harness must be codex, claude or opencode"}
	}
	if err := CheckLane(lane); err != nil {
		return nil, err
	}
	cwd := p.Root
	if value, ok := opts.Value("cwd"); opts.Truthy("root") && ok && value != "" {
		var err error
		cwd, err = nodefs.Resolve(value)
		if err != nil {
			return nil, err
		}
	}
	return &Launch{project: p, opts: opts, brief: brief, lane: lane, harness: harness, cwd: cwd}, nil
}

// resolveAgainst is path.resolve(root, p) for an absolute root.
func resolveAgainst(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// Runtime is what a foreground worker takes from the process it runs in. Tests replace each.
type Runtime struct {
	// Self is the executable the bridge runs as: `<Self> <harness>-exec ...` (the JS runs
	// `node <harness>-exec.mjs`).
	Self string
	// Pid is the worker's own pid, written into the lock and the state.
	Pid            int
	Stdout, Stderr io.Writer
	Now            func() time.Time
	NewID          func() string // crypto.randomUUID
	// Notify starts delivering SIGINT and SIGTERM to c, as installing the JS handlers does
	// (signal.Notify); nothing stops the delivery before the process exits.
	Notify func(c chan<- os.Signal)
}

// Foreground is `worker.mjs start` without --detach (l.86-148): it reads the brief, claims the
// lane's lock, publishes the state, runs the bridge with the brief as its prompt, relays the
// bridge's progress, records how it ended and prints {lane, status, session_id, exit_code}. The
// exit code is 0 for a finished or deferred run, 1 otherwise, ExitLaneLocked when the lane is
// held.
//
// The lane is claimed with ownlock.Acquire, a kernel record lock this runner process holds for
// its whole run (C6: the runner owns the lane, not its bridge's process group). A lock file
// left by a runner that died is held by no one and is reclaimed here (PE-28; the JS refuses any
// existing file, so such a lane stays blocked until a person deletes it). The lock is released
// on every path once claimed, before the final line, so the lane is free when the line is read;
// the JS leaves it when the run dies between its claim and its try block.
//
// A returned error is the exception the JS dies of (exit 1, after the output so far). An
// unreadable brief fails before the lock is claimed, so it leaves neither lock nor state, and
// wins over a held lane. A failure creating the workers directory leaves nothing either; a
// failure before the bridge starts leaves the "starting" state, as in the JS.
func (l *Launch) Foreground(rt Runtime) (int, error) {
	p := l.project
	prompt, err := l.prompt()
	if err != nil {
		return 1, err
	}
	if err := nodefs.MkdirPrivate(p.WorkersDir); err != nil {
		return 1, err
	}
	lock, err := ownlock.Acquire(p.LockPath(l.lane), rt.Pid)
	if err != nil {
		// As the JS treats any failure of its exclusive create, any failure to claim is a
		// held lane, so a launcher always sees exit 3 for a lane it cannot run.
		return l.refuseLocked(rt.Stderr)
	}
	r := &run{launch: l, rt: rt, state: p.NewState(l.initialState(rt), rt.Now)}
	code, err := releasingAfter(lock, rt.Stderr, func() (any, error) {
		r.state.Publish(jsjson.NewObject())
		// Signals are captured from here on; runBridge starts handling them once the bridge
		// exists.
		signals := make(chan os.Signal, 2)
		rt.Notify(signals)
		return r.runBridge(prompt, signals)
	})
	if err != nil {
		return 1, err
	}
	return r.report(code)
}

// releasingAfter runs the claimed part of a run and then releases the lane's lock, on every
// path. A failed Release does not change how the run ended: the kernel lock goes with the
// descriptor, or at the latest with the process, so the lane is never left owned; at worst the
// lock file stays behind, which the next start reclaims. The failure is reported on stderr, not
// ignored as the JS ignores a failed unlink.
func releasingAfter(lock *ownlock.Lock, stderr io.Writer, claimed func() (any, error)) (any, error) {
	defer func() {
		if err := lock.Release(); err != nil {
			fmt.Fprintf(stderr, "orch worker: releasing the lane lock: %v\n", err)
		}
	}()
	return claimed()
}

// laneLockedHint is the advice the lane-locked refusal carries.
//
// PE-28: the JS tells people to remove the lock once the process is gone; a lock is never
// removed by hand (C8), since the next start reclaims a dead runner's lock itself.
const laneLockedHint = "inspect worker status; the lane frees itself when its runner exits"

// refuseLocked prints the lane-locked refusal of a foreground start: the previous state's
// status, pid and whether that pid is alive, and the hint.
func (l *Launch) refuseLocked(stderr io.Writer) (int, error) {
	return l.printLaneLocked(stderr, previousWorker(l.project.ReadState(l.lane), Alive), laneLockedHint)
}

// printLaneLocked prints {error: "lane locked", lane, previous[, hint]} on stderr (hint is left
// out when "") and returns ExitLaneLocked.
func (l *Launch) printLaneLocked(stderr io.Writer, previous any, hint string) (int, error) {
	refusal := jsjson.NewObject()
	refusal.Set("error", "lane locked")
	refusal.Set("lane", l.lane)
	refusal.Set("previous", previous)
	if hint != "" {
		refusal.Set("hint", hint)
	}
	data, err := jsjson.Marshal(refusal)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stderr, "%s\n", data)
	return ExitLaneLocked, nil
}

// previousWorker is `prev && {status: prev.status, pid: prev.pid, alive: <alive>}` with alive
// given by isAlive(prev.pid); an undefined status or pid is left out, as JSON.stringify drops
// it, and a falsy state is printed as itself.
func previousWorker(prev any, isAlive func(pid any) bool) any {
	if !jsjson.Truthy(prev) {
		return prev
	}
	out := jsjson.NewObject()
	for _, key := range []string{"status", "pid"} {
		if value, present := jsjson.Lookup(prev, key); present {
			out.Set(key, value)
		}
	}
	out.Set("alive", isAlive(jsjson.Path(prev, "pid")))
	return out
}

// initialState is the state object before its first publish, in the JS key order.
func (l *Launch) initialState(rt Runtime) *jsjson.Object {
	o := l.opts
	runID := o.JS("run-id")
	if !jsjson.Truthy(runID) {
		runID = rt.NewID()
	}
	state := jsjson.NewObject()
	state.Set("lane", l.lane)
	state.Set("status", "starting")
	state.Set("pid", float64(rt.Pid))
	state.Set("harness", l.harness)
	state.Set("brief", l.briefPath())
	state.Set("breadth", o.Or("breadth", "standard"))
	state.Set("run_id", runID)
	state.Set("session_id", o.Or("resume", nil))
	state.Set("started_at", jsjson.ISOString(rt.Now()))
	state.Set("log_path", o.Or("log-path", nil))
	state.Set("model", o.Or("model", nil))
	return state
}

// briefPath is the brief relative to the project root, as the state and the preamble name it.
func (l *Launch) briefPath() string {
	rel, _ := nodefs.Relative(l.project.Root, l.brief) // both absolute: cannot fail
	return rel
}

// prompt is the brief alone for the conductor lane, else the worker preamble, a separator and
// the brief (l.86-96). The brief is read as UTF-8 (a directory fails with EISDIR).
func (l *Launch) prompt() (string, error) {
	data, err := nodefs.ReadFile(l.brief)
	if err != nil {
		return "", err
	}
	brief := jsstr.DecodeUTF8(data)
	if l.lane == "conductor" {
		return brief, nil
	}
	return l.preamble() + "\n\n--- BRIEF ---\n" + brief, nil
}

// preamble tells a worker how to behave in the orchestrated project, verbatim from
// worker.mjs l.87-95.
func (l *Launch) preamble() string {
	lane := jsjson.TemplateString(l.lane)
	return strings.Join([]string{
		"You are a worker in an orchestrated multi-agent project at " + l.project.Root + ".",
		"First read " + l.briefPath() + " (your bounded brief) and .orchestrator/STATE.md if it exists.",
		"Check the mailbox for messages addressed to you: " + cmdref.Command("mailbox") + " list --to " + lane + " --unacked-by " + lane,
		"Do only the brief. When done, write your report where the brief says (default .orchestrator/reports/" + lane + ".md) including: outcome, files changed, exact checks run with results, what is NOT done, open questions.",
		"Create a source manifest for the files you touched: " + cmdref.Command("manifest") + " create --task " + lane + " --author " + l.harness + " --out .orchestrator/manifests/" + lane + ".json <paths>",
		"Finally post a release message: " + cmdref.Command("mailbox") + " post --from " + lane + " --to coordinator --kind release --subject \"" + lane + " released\" --ref .orchestrator/reports/" + lane + ".md --ref .orchestrator/manifests/" + lane + ".json --body \"<3-line summary>\"",
		`If blocked, post kind blocked instead and stop. Be honest: unrun checks are "not run", unmet criteria mean "partial". Never approve your own work, never bypass permissions, never touch another lane's files.`,
	}, "\n")
}

// bridgeArgs is the bridge command line after the executable (l.105-114): option values are
// passed as their JS text (a bare key's true as "true"). --effort reaches opencode-exec too,
// which has no such option, so its value becomes the prompt there, as in the JS.
func (l *Launch) bridgeArgs() []string {
	o := l.opts
	text := func(key string) string { return jsjson.TemplateString(o.JS(key)) }
	cwd := l.project.Root
	if l.cwd != "" {
		cwd = l.cwd
	}
	args := []string{l.harness + "-exec", "--cwd", cwd, "--timeout-min", jsjson.TemplateString(o.Or("timeout-min", "120"))}
	if cwd != l.project.Root {
		args = append(args, "--artifact-root", l.project.Root)
	}
	for _, key := range []string{"model", "effort", "resume"} {
		if o.Truthy(key) {
			args = append(args, "--"+key, text(key))
		}
	}
	if o.Truthy("force") {
		args = append(args, "--force")
	}
	if o.Truthy("agent") && l.harness != "codex" {
		args = append(args, "--agent", text("agent"))
	}
	switch l.harness {
	case "codex":
		args = append(args, "--sandbox", jsjson.TemplateString(o.Or("sandbox", "workspace-write")))
	case "claude":
		args = append(args, "--permission-mode", jsjson.TemplateString(o.Or("permission-mode", "auto")))
		for _, key := range []string{"allowed-tools", "max-turns"} {
			if o.Truthy(key) {
				args = append(args, "--"+key, text(key))
			}
		}
	}
	return args
}
