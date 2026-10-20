package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/session"
)

var sessionOptions = map[string]cliargs.SessionOptions{
	"beat": {Values: []string{"door", "state", "now", "slice", "progress", "session", "cwd"}, Flags: []string{"hook"}},
	"show": {Values: []string{"cwd"}},
}

// sessionUsageError is a bad command line: exit 2, or 1 in hook mode, where Claude Code reads
// exit 2 as "block the prompt".
type sessionUsageError struct{ message string }

func (e *sessionUsageError) Error() string { return e.message }

// Session implements `orch session beat|show`, byte-identical to bin/session.mjs.
func Session(args []string, stdio Stdio) int {
	args = decodeArgv(args)
	if len(args) == 0 || sessionOptions[args[0]].Values == nil {
		fmt.Fprintln(stdio.Err, "commands: beat | show")
		return 2
	}
	if args[0] == "show" {
		return sessionShow(args[1:], stdio)
	}
	return sessionBeatCommand(args[1:], stdio)
}

// sessionBeatCommand runs beat and turns its error into the one-line report and exit code:
// a usage error exits 2 outside hook mode, every other reported failure 1.
func sessionBeatCommand(rest []string, stdio Stdio) int {
	hookMode := slices.Contains(rest, "--hook")
	opts, err := cliargs.ParseSession(rest, sessionOptions["beat"])
	switch {
	case err != nil:
		err = &sessionUsageError{err.Error()}
	case hookMode:
		err = sessionHookBeat(opts, stdio)
	default:
		err = sessionBeat(opts, stdio)
	}
	var usage *sessionUsageError
	var failure *session.Error
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage) && !hookMode:
		fmt.Fprintf(stdio.Err, "session beat: %s\n", err)
		return 2
	case errors.As(err, &usage), errors.As(err, &failure):
		fmt.Fprintf(stdio.Err, "session beat: %s\n", err)
		return 1
	}
	return crash(stdio, err)
}

func sessionBeat(opts cliargs.Opts, stdio Stdio) error {
	beat, err := beatOf(opts)
	if err != nil {
		return err
	}
	root, err := nodefs.Resolve(valueOrCwd(opts, ""))
	if err != nil {
		return err
	}
	return writeBeat(root, door(opts), beat, stdio)
}

// sessionHookBeat is beat --hook: the hook's event sets the state, so --state is refused
// rather than silently overridden; an event a heartbeat does not follow writes nothing.
func sessionHookBeat(opts cliargs.Opts, stdio Stdio) error {
	if _, given := opts["state"]; given {
		return &sessionUsageError{"--state cannot be combined with --hook"}
	}
	beat, err := beatOf(opts)
	if err != nil {
		return err
	}
	text, err := cliargs.ReadStdinText(stdio.In)
	if err != nil {
		return &session.Error{Message: "could not read the hook input: " + nodefs.Code(err)}
	}
	hook, err := session.ParseHookInput(text)
	if err != nil {
		return err
	}
	state, followed := session.HookEventState(hook.Event)
	if !followed {
		return nil
	}
	dir := valueOrCwd(opts, hook.Cwd)
	if dir == "" {
		return &session.Error{Message: "hook input has no cwd"}
	}
	// Checked before any filesystem call, whose own NUL error differs between Node and Go.
	if strings.ContainsRune(dir, 0) {
		return &session.Error{Message: "hook input cwd contains a NUL character"}
	}
	root, err := nodefs.Resolve(dir)
	if err != nil {
		return err
	}
	if err := requireHookProject(root); err != nil {
		return err
	}
	beat.State = state
	if hook.SessionID != nil {
		beat.SessionID = session.To(*hook.SessionID)
	}
	return writeBeat(root, door(opts), beat, stdio)
}

// valueOrCwd is `opts.cwd || fallback`. An empty result means the working directory to
// Resolve, which sessionHookBeat checks for first.
func valueOrCwd(opts cliargs.Opts, fallback string) string {
	if cwd, _ := opts.Value("cwd"); cwd != "" {
		return cwd
	}
	return fallback
}

func door(opts cliargs.Opts) string {
	value, _ := opts.Value("door")
	return value
}

// requireHookProject refuses a root without .orchestrator/: a chat in a directory that is not a
// project must not create project state.
func requireHookProject(root string) error {
	err := paths.RequireProject(root, "--cwd")
	var notAProject *paths.NotAProjectError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &notAProject):
		return &session.Error{Message: root + " has no .orchestrator/ directory"}
	}
	return &session.Error{Message: fmt.Sprintf("could not check %s/.orchestrator: %s", root, nodefs.Code(err))}
}

// beatOf validates the beat options. A text option given as "" clears its field; one not
// given keeps it. --progress, when given, must be N/M.
func beatOf(opts cliargs.Opts) (session.Beat, error) {
	if !session.IsDoor(door(opts)) {
		return session.Beat{}, &sessionUsageError{"--door opencode|claude required"}
	}
	beat := session.Beat{State: "working", SessionID: textChange(opts, "session"), Now: textChange(opts, "now"), Slice: textChange(opts, "slice")}
	if state, given := opts.Value("state"); given {
		if !session.IsState(state) {
			return session.Beat{}, &sessionUsageError{"--state must be " + strings.Join(session.States, "|")}
		}
		beat.State = state
	}
	if text, given := opts.Value("progress"); given {
		progress, ok := session.ParseProgress(text)
		if !ok {
			return session.Beat{}, &sessionUsageError{"--progress must be N/M"}
		}
		beat.Progress = session.To(progress)
	}
	return beat, nil
}

func textChange(opts cliargs.Opts, key string) session.Change[string] {
	switch text, given := opts.Value(key); {
	case !given:
		return session.Change[string]{}
	case text == "":
		return session.Clear[string]()
	default:
		return session.To(text)
	}
}

func writeBeat(root, door string, beat session.Beat, stdio Stdio) error {
	replaced, err := session.Write(root, door, beat, time.Now())
	if err != nil {
		return err
	}
	if replaced {
		fmt.Fprintf(stdio.Err, "session beat: replaced .orchestrator/sessions/%s.json, which was not a JSON object\n", door)
	}
	return nil
}

func sessionShow(rest []string, stdio Stdio) int {
	opts, err := cliargs.ParseSession(rest, sessionOptions["show"])
	if err != nil {
		fmt.Fprintf(stdio.Err, "session show: %s\n", err)
		return 2
	}
	root, err := nodefs.Resolve(valueOrCwd(opts, ""))
	if err != nil {
		return crash(stdio, err)
	}
	if code, ok := requireProject(stdio, root, "--cwd"); !ok {
		return code
	}
	sessions, skipped := session.Read(root)
	for _, reason := range skipped {
		fmt.Fprintf(stdio.Err, "session show: skipped %s\n", reason)
	}
	data, err := jsjson.MarshalIndent(sessions, "  ")
	if err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}
