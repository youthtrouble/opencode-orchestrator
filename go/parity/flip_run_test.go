//go:build parity

package parity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// The conductor lane's lock and state as a run finds them.
const (
	conductorLock  = ".orchestrator/workers/conductor.lock"
	conductorState = ".orchestrator/workers/conductor.json"
)

// heldByThisTest locks the conductor lane with this test process's pid, which is only ever
// sent signal 0: both launchers refuse with "lane locked", so the run records a pending launch
// and nothing is launched.
func heldByThisTest() seed {
	return seedProjectFile(conductorLock, strconv.Itoa(os.Getpid()))
}

// flipRunInvocation is a `run` whose launch the lane lock refuses (plus the case's seed).
func (h *harness) flipRunInvocation(args []string, s seed, crash crashClass, errorLine string) invocation {
	all := heldByThisTest()
	if s != nil {
		all = seeds(all, s)
	}
	return h.flipInvocation(flipCase{args: append([]string{"run"}, args...), seed: all, crash: crash, errorLine: errorLine})
}

// withGit replaces the sandbox's .git directory with a file holding content (a worktree's or
// a submodule's .git), or removes it when content is nil.
func withGit(content *string) seed {
	return func(t *testing.T, sb sandbox) {
		git := filepath.Join(sb.project, ".git")
		if err := os.Remove(git); err != nil {
			t.Fatal(err)
		}
		if content != nil {
			writeSeedFile(t, git, *content)
		}
	}
}

func seedExclude(content string) seed { return seedProjectFile(".git/info/exclude", content) }

// testFlipRun compares `flip run`: refusals, the pending launch (the matrix of what a run
// writes before its launch), the crashes before the launch, full launches of a conductor that
// runs against the fakes, and flip.json read across runtimes.
func (h *harness) testFlipRun(t *testing.T) {
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	claude := []string{"--to", "claude"}
	refusals := []struct {
		name string
		args []string
	}{
		{"usage/to-absent", nil},
		{"usage/to-gemini", []string{"--to", "gemini"}},
		{"usage/to-bare", []string{"--to"}},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.flipRunInvocation(c.args, nil, noCrash, "")) })
	}
	t.Run("usage/cwd-bare", func(t *testing.T) {
		h.comparePair(t, h.flipRunInvocation(append(claude, "--cwd"), nil, crashC1, cwdBoolean))
	})

	seeded := `{"to":"claude","awaiting_human":{"door":"opencode","at":"2026-01-02T00:00:00.000Z"},"paused":{"door":"claude","at":"2026-01-01T00:00:00.000Z"},"at":"2026-01-03T00:00:00.000Z","from":"opencode"}`
	pending := []struct {
		name string
		args []string
		seed seed
	}{
		{"pending/fresh", claude, nil},
		{"pending/opencode", []string{"--to", "opencode"}, nil},
		{"pending/with-conductor-state", claude, seedProjectFile(conductorState, `{"lane":"conductor","status":"running","pid":`+strconv.Itoa(os.Getpid())+`}`)},
		{"pending/reason-and-resume", []string{"--to", "opencode", "--from", "claude", "--reason", "R", "--resume", "S"}, nil},
		{"pending/from-bare", append(claude, "--from"), nil},
		{"pending/reason-bare", append(claude, "--reason"), nil},
		{"pending/resume-bare", append(claude, "--resume"), nil},
		{"pending/force", append(claude, "--force"), nil},
		{"pending/mailbox-is-file", claude, seedProjectFile(".orchestrator/mailbox", "x")},

		{"previous/record", claude, seedFlip(`{"at":"2026-01-01T00:00:00.000Z","from":"claude","to":"opencode","reason":null,"previous":null}`)},
		{"previous/paused-only", claude, seedFlip(`{"paused":{"at":"2026-01-01T00:00:00.000Z","door":"claude","reason":"r"}}`)},
		{"previous/gate-and-pause-cleared", claude, seedFlip(seeded)},
		{"previous/string", claude, seedFlip(`"abc"`)},
		{"previous/array", claude, seedFlip(`[1]`)},
		{"previous/zero", claude, seedFlip(`0`)},
		{"previous/null", claude, seedFlip(`null`)},
		{"previous/unparsable", claude, seedFlip(`{"at":`)},
		{"previous/odd-values", claude, seedFlip(`{"at":5,"to":true}`)},

		{"exclude/empty", claude, seedExclude("")},
		{"exclude/no-newline", claude, seedExclude("foo")},
		{"exclude/newline", claude, seedExclude("foo\n")},
		{"exclude/carriage-return", claude, seedExclude("foo\r")},
		{"exclude/bare-name", claude, seedExclude("a\n.orchestrator\n")},
		{"exclude/name-with-slash", claude, seedExclude(".orchestrator/")},
		{"exclude/crlf", claude, seedExclude("a\r\n.orchestrator/\r\n")},
		// Git ends lines only at \n: both runtimes append after a CR or U+2028 "line".
		{"exclude/line-separator", claude, seedExclude("a\u2028.orchestrator/\u2028b")},
		{"exclude/lone-carriage-return", claude, seedExclude("a\r.orchestrator/\rb")},
		{"exclude/leading-space", claude, seedExclude(" .orchestrator/\n")},
		{"exclude/double-slash", claude, seedExclude(".orchestrator//")},
		{"exclude/invalid-utf8-at-the-end", claude, seedExclude("foo\xff")},
		{"exclude/no-git", claude, withGit(nil)},
	}
	for _, c := range pending {
		t.Run(c.name, func(t *testing.T) { h.compareFlipPending(t, h.flipRunInvocation(c.args, c.seed, noCrash, "")) })
	}

	crashes := []struct {
		name, errorLine string
		seed            seed
		blocksExclude   bool // the exclude is a directory or lies under a file
		pe21            bool // errorLine is the JS's open failure; orch fails at its rename
	}{
		{"crash/git-is-a-file", "Error: ENOTDIR: not a directory, mkdir '$PROJECT/.git/info'", withGit(text("gitdir: elsewhere\n")), true, false},
		{"crash/exclude-is-a-directory", "Error: EISDIR: illegal operation on a directory, read", seedProjectDir(".git/info/exclude"), true, false},
		{"crash/PE-21-brief-is-a-directory", "Error: EISDIR: illegal operation on a directory, open '$PROJECT/.orchestrator/briefs/conductor-continue.md'",
			seedProjectDir(".orchestrator/briefs/conductor-continue.md"), false, true},
		{"crash/PE-21-flip-is-a-directory", "Error: EISDIR: illegal operation on a directory, open '$PROJECT/.orchestrator/flip.json'", seedProjectDir(".orchestrator/flip.json"), false, true},
	}
	for _, c := range crashes {
		t.Run(c.name, func(t *testing.T) {
			inv := h.flipRunInvocation(claude, c.seed, crashC1, c.errorLine)
			inv.extraFilesMayBeBlocked = c.blocksExclude
			if c.pe21 {
				h.comparePE21OpenVsRename(t, inv)
				return
			}
			h.comparePair(t, inv)
		})
	}

	t.Run("launch", h.testFlipLaunches)
	t.Run("cross-runtime", h.testFlipRunCrossRuntime)
}

// compareFlipPending compares a run both runtimes must leave pending, which proves neither
// launched a conductor.
func (h *harness) compareFlipPending(t *testing.T, inv invocation) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 0 || !strings.HasPrefix(o.stdout, `{"flipped":false,"pending":true,`) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want a pending launch", side, o.exitCode, o.stdout, o.stderr)
		}
	}
	compareOutcomes(t, js, orch)
}

// flipLaunchCase is a run that launches the conductor worker, whose runner runs to its end
// against the bridge's fake as scripted by run.
type flipLaunchCase struct {
	name   string
	args   []string // after "run"
	bridge bridge
	run    bridgeCase
}

// testFlipLaunches compares full launches: the run's answer, which quotes the launcher's line,
// and everything the settled runner wrote.
func (h *harness) testFlipLaunches(t *testing.T) {
	ok := bridgeCase{fixture: "ok"}
	stale := seedProjectFile(conductorLock, strconv.Itoa(reapedPid(t)))
	cases := []flipLaunchCase{
		{"claude", []string{"--to", "claude"}, claudeBridge, ok},
		{"opencode-handback", []string{"--to", "opencode", "--from", "claude", "--reason", "handback: home door opencode is stably green again"},
			opencodeBridge, bridgeCase{script: opencodeRunOK}},
		{"claude-resume", []string{"--to", "claude", "--resume", "00000000-0000-4000-8000-0000000000aa"}, claudeBridge, ok},
		{"claude-force-anthropic-red", []string{"--to", "claude", "--force"}, claudeBridge, bridgeCase{fixture: "ok", usage: "anthropic-red"}},
		{"claude-deferred-anthropic-red", []string{"--to", "claude"}, claudeBridge, bridgeCase{usage: "anthropic-red"}},
		{"claude-stale-lock", []string{"--to", "claude"}, claudeBridge, bridgeCase{fixture: "ok", seed: stale}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			js, orch := h.runFlipLaunchPair(t, c)
			for side, o := range map[string]outcome{"js": js, "orch": orch} {
				if o.exitCode != 0 || !strings.HasPrefix(o.stdout, `{"flipped":true,`) {
					t.Errorf("%s: exit %d, stdout %q, stderr %q; want a launch", side, o.exitCode, o.stdout, o.stderr)
				}
			}
			compareOutcomes(t, js, orch)
		})
	}
	t.Run("PE-2/launcher-crash", h.testFlipLauncherCrash)
}

// flipLaunchInvocation is the worker invocation of the case's bridge with flip run as the
// command; the brief is the one run writes.
func (h *harness) flipLaunchInvocation(t *testing.T, c flipLaunchCase) invocation {
	t.Helper()
	inv := h.workerStartInvocation(t, workerStartCase{bridge: c.bridge, run: c.run, brief: text("")})
	args := append([]string{"run"}, c.args...)
	inv.js = append([]string{h.node, filepath.Join(h.orchJS, "bin", "flip.mjs")}, args...)
	inv.orch = append([]string{h.orchBin, "flip"}, args...)
	inv.extraFiles = []string{".git/info/exclude"}
	worker, fresh := inv.normalize, normalizeFlip(time.Now())
	inv.normalize = func(s string) string { return fresh(worker(s)) }
	return inv
}

// runFlipLaunchPair runs the case in each runtime's own sandbox, settling each runner.
func (h *harness) runFlipLaunchPair(t *testing.T, c flipLaunchCase) (js, orch outcome) {
	t.Helper()
	inv := h.flipLaunchInvocation(t, c)
	launched := workerDetachCase{launchLine: flipLauncherLine}
	runners := strings.NewReplacer(h.runnerPrefixes(t)...)
	js = h.runDetached(t, inv, nil, inv.js, launched, runners)
	orch = h.runDetached(t, inv, nil, inv.orch, launched, runners)
	return js, orch
}

// flipLauncherLine is the launcher's line a successful run quotes in its answer. It must be
// whole: the answer keeps 400 UTF-16 units of it.
func flipLauncherLine(t *testing.T, stdout string) string {
	t.Helper()
	var answer struct {
		Launcher string `json:"launcher"`
	}
	if err := json.Unmarshal([]byte(stdout), &answer); err != nil {
		t.Fatalf("run answer %q: %v", stdout, err)
	}
	if jsstr.Len(answer.Launcher) >= 400 {
		t.Errorf("the launcher's line may have been cut: %q", answer.Launcher)
	}
	return answer.Launcher // a cut line fails to parse, which fails the case
}

// testFlipLauncherCrash asserts PE-2 (extended) in run's answer when the launcher dies before
// starting a runner (.orchestrator/logs is a file): the JS quotes Node's crash block (the
// throwing location, here inside node:fs, its source excerpt, the error line and the stack),
// orch the error line alone. Everything else is compared.
func (h *harness) testFlipLauncherCrash(t *testing.T) {
	const errorLine = "Error: EEXIST: file already exists, mkdir '$PROJECT/.orchestrator/logs'"
	c := flipLaunchCase{args: []string{"--to", "claude"}, bridge: claudeBridge, run: bridgeCase{seed: seedProjectFile(".orchestrator/logs", "")}}
	js, orch := h.runFlipLaunchPair(t, c)
	launcher := func(side string, o *outcome) string {
		var answer struct {
			Flipped  bool   `json:"flipped"`
			Launcher string `json:"launcher"`
		}
		if err := json.Unmarshal([]byte(o.stdout), &answer); err != nil || answer.Flipped || o.exitCode != 1 {
			t.Fatalf("%s: exit %d, stdout %q (%v); want flipped false, exit 1", side, o.exitCode, o.stdout, err)
		}
		quoted, err := jsjson.Marshal(answer.Launcher) // as the runtimes quote it
		if err != nil {
			t.Fatal(err)
		}
		o.stdout = strings.Replace(o.stdout, string(quoted), `"<PE-2>"`, 1)
		return answer.Launcher
	}
	jsLauncher, orchLauncher := launcher("js", &js), launcher("orch", &orch)
	if orchLauncher != errorLine {
		t.Errorf("orch launcher %q, want the error line %q", orchLauncher, errorLine)
	}
	if !slices.Contains(strings.Split(jsLauncher, "\n"), errorLine) || !strings.Contains(jsLauncher, "\n    at ") {
		t.Errorf("js launcher %q: want Node's crash block holding the line %q and a stack", jsLauncher, errorLine)
	}
	compareOutcomes(t, js, orch)
}

// testFlipRunCrossRuntime has each runtime's run record a pending launch and both runtimes'
// status read it.
func (h *harness) testFlipRunCrossRuntime(t *testing.T) {
	writers := map[string][]string{
		"js":   {h.node, filepath.Join(h.orchJS, "bin", "flip.mjs")},
		"orch": {h.orchBin, "flip"},
	}
	for writer, argv := range writers {
		t.Run("written-by-"+writer, func(t *testing.T) {
			sb := h.newSandbox(t)
			heldByThisTest()(t, sb)
			run := []string{"run", "--to", "opencode", "--from", "claude", "--reason", "R", "--resume", "S"}
			written := h.run(t, sb, append(append([]string{}, argv...), run...), nil, []string{noPostRunRefresh})
			content := written.files["project/flip.json"]
			if written.exitCode != 0 || !strings.Contains(content, `"pending_launch": {`) {
				t.Fatalf("%s run: exit %d, stdout %q, stderr %q, flip.json %q", writer, written.exitCode, written.stdout, written.stderr, content)
			}
			h.comparePair(t, h.flipInvocation(flipCase{args: []string{"status"}, seed: seedFlip(content)}))
		})
	}
}
