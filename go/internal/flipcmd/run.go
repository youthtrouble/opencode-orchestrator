package flipcmd

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// ErrToRequired is run's refusal of a --to that is not a door (exit 2).
var ErrToRequired = errors.New("--to opencode|claude required")

// Launcher runs `worker start <args>` to completion (the JS spawnSync of worker.mjs) and
// returns its exit status, nil when it was killed by a signal or could not be started, and
// its outputs ("" when it could not be started).
type Launcher func(args []string) (status *int, stdout, stderr string)

// launcherUnits is how many UTF-16 units of the launcher's output run's answer keeps.
const launcherUnits = 400

// pendingNote is the note of a launch left to the supervisor.
const pendingNote = "lane locked by the current conductor; supervisor will launch after release"

// Run hands the project over to the conductor at --to (bin/flip.mjs l.72-101) and returns the
// line to print and the exit code. It first reads state under Update and refuses an
// unreadable record or, with --unless-gated, a human gate. It adds .orchestrator/ to the repository's
// info/exclude, writes the continue brief, replaces flip.json with the flip record
// (clearing paused and awaiting_human), posts a handoff (failures ignored) and launches the
// conductor through launch as worker lane "conductor". When the record it replaces holds a
// brief a kickoff saved (see savedBrief), that brief is launched and named in the handoff
// instead, and no continue brief is written.
//
// When the lane is locked, usually by the calling conductor itself, the launch is left to the
// supervisor: flip.json gains pending_launch, which keeps the kickoff's brief for the
// supervisor's retry, and the answer is pending, exit 0. Otherwise
// the answer carries the launcher's output and the exit is 0 when the launcher exited 0, else 1.
// Errors include ErrToRequired, StateError, and transaction/filesystem failures; the exit
// code is then unused. The transaction is released before posting or launching.
func (p Project) Run(opts cliargs.Opts, launch Launcher) (*jsjson.Object, int, error) {
	to, isText := opts.JS("to").(string)
	if !isText || (to != flip.DoorOpencode && to != flip.DoorClaude) {
		return nil, 0, ErrToRequired
	}
	from := opts.Or("from", flip.OtherDoor(to))
	reason := opts.Or("reason", nil)
	// The brief and the handoff body say why with a default text; flip.json keeps null.
	reasonOrDefault := opts.Or("reason", "vendor budget flip")
	var kickoffBrief, brief string
	gated := false
	err := p.Update(func(prev Record) (any, error) {
		if opts.Truthy("unless-gated") && jsjson.Truthy(jsjson.Path(prev.Value, "awaiting_human")) {
			gated = true
			return nil, ErrNoUpdate
		}
		if _, err := EnsureGitExclude(p.Root); err != nil {
			return nil, err
		}
		kickoffBrief = p.savedBrief(prev.Value)
		brief = kickoffBrief
		if brief == "" {
			var err error
			brief, err = p.writeContinueBrief(to, from, reasonOrDefault)
			if err != nil {
				return nil, err
			}
		}
		record := jsjson.NewObject()
		record.Set("at", jsjson.ISOString(p.Now()))
		record.Set("from", from)
		record.Set("to", to)
		record.Set("reason", reason)
		record.Set("previous", previousFlip(prev.Value))
		return record, nil
	})
	if err != nil {
		return nil, 0, err
	}
	if gated {
		answer := jsjson.NewObject()
		answer.Set("flipped", false)
		answer.Set("reason", "awaiting human")
		return answer, 1, nil
	}
	p.postIgnoringErrors(mailbox.Draft{
		From:    conductor(from),
		To:      conductor(to),
		Kind:    "handoff",
		Subject: "conductor flip " + jsjson.TemplateString(from) + " -> " + to,
		Body:    reasonOrDefault,
		Refs:    []string{stateRef, brief},
	})
	status, stdout, stderr := launch(p.conductorLaunchArgs(opts, to, brief))
	out := stdout
	if out == "" {
		out = stderr
	}
	out = strings.Trim(out, jsstr.WhiteSpace)
	if (status == nil || *status != 0) && strings.Contains(out, "lane locked") {
		return p.leaveLaunchPending(opts, to, from, reason, kickoffBrief)
	}
	launched := status != nil && *status == 0
	answer := jsjson.NewObject()
	answer.Set("flipped", launched)
	answer.Set("to", to)
	answer.Set("from", from)
	answer.Set("launcher", jsstr.Slice(out, 0, launcherUnits))
	if launched {
		return answer, 0, nil
	}
	return answer, 1, nil
}

// savedBrief is the brief a kickoff saved: the one its pause names (`flip pause --brief`),
// else the one a pending launch kept, or "". Only an object record has one, and a brief whose
// file is gone (fs.existsSync of path.resolve(root, brief)) falls back to the continue brief so
// the relaunch still starts a conductor.
func (p Project) savedBrief(prev any) string {
	for _, holder := range []string{"paused", "pending_launch"} {
		brief, isText := jsjson.Path(prev, holder, "brief").(string)
		if isText && brief != "" && nodefs.Exists(resolveFrom(p.Root, brief)) {
			return brief
		}
	}
	return ""
}

// resolveFrom mirrors path.resolve(root, name) for an absolute root.
func resolveFrom(root, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(root, name)
}

// writeContinueBrief creates .orchestrator/briefs/ and writes the brief the launched
// conductor starts from, returning its path relative to the project (path.relative). The brief
// is replaced atomically and owner-only, never written through a symlink (PE-21).
func (p Project) writeContinueBrief(to string, from, reason any) (string, error) {
	dir := filepath.Join(p.Root, ".orchestrator", "briefs")
	if err := nodefs.MkdirAll(dir); err != nil {
		return "", err
	}
	brief := filepath.Join(dir, "conductor-continue.md")
	text := cmdref.ConductorContinueBrief(to, jsjson.TemplateString(from), jsjson.TemplateString(reason))
	if err := nodefs.WritePrivateFile(brief, []byte(text)); err != nil {
		return "", err
	}
	return nodefs.Relative(p.Root, brief)
}

// previousFlip is `prev ? {at: prev.at, to: prev.to} : null`: an object's own at and to
// (an absent key is left out, null is kept); any other truthy value has no at or to that
// JSON keeps (a string's or an array's at is a function), so it gives {}.
func previousFlip(prev any) any {
	if !jsjson.Truthy(prev) {
		return nil
	}
	previous := jsjson.NewObject()
	for _, key := range []string{"at", "to"} {
		if value, present := jsjson.Lookup(prev, key); present {
			previous.Set(key, value)
		}
	}
	return previous
}

// conductorLaunchArgs are the arguments after `worker start` of the detached conductor
// worker. The CLI requires a value for --resume.
func (p Project) conductorLaunchArgs(opts cliargs.Opts, to, brief string) []string {
	args := []string{"start", "--brief", brief, "--lane", "conductor", "--harness", to, "--agent", "conductor",
		"--breadth", "broad", "--cwd", p.Root, "--detach"}
	if to == flip.DoorClaude {
		args = append(args, "--permission-mode", "auto")
	}
	if opts.Truthy("resume") {
		args = append(args, "--resume", jsjson.TemplateString(opts.JS("resume")))
	}
	if opts.Truthy("force") {
		args = append(args, "--force")
	}
	return args
}

// leaveLaunchPending rewrites flip.json as {...record, pending_launch: {to, from, reason,
// resume, at, brief}}, which the supervisor launches once the lane is released, and returns the
// pending answer. It merges into current state so a gate installed during launch survives.
// brief is the kickoff's, left out when it is "".
func (p Project) leaveLaunchPending(opts cliargs.Opts, to string, from, reason any, kickoffBrief string) (*jsjson.Object, int, error) {
	pending := jsjson.NewObject()
	pending.Set("to", to)
	pending.Set("from", from)
	pending.Set("reason", reason)
	pending.Set("resume", opts.Or("resume", nil))
	pending.Set("at", jsjson.ISOString(p.Now()))
	if kickoffBrief != "" {
		pending.Set("brief", kickoffBrief)
	}
	if err := p.Update(func(prev Record) (any, error) {
		withPending := jsjson.Spread(prev.Value)
		withPending.Set("pending_launch", pending)
		return withPending, nil
	}); err != nil {
		return nil, 0, err
	}
	answer := jsjson.NewObject()
	answer.Set("flipped", false)
	answer.Set("pending", true)
	answer.Set("to", to)
	answer.Set("from", from)
	answer.Set("note", pendingNote)
	return answer, 0, nil
}
