//go:build parity

package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// supervisorHeldByThisTest has a helper process hold the project's supervisor lock (CA2), whose
// pid each runtime sees (the JS in the text, orch from the kernel) and only ever sends signal
// 0: `supervisor start` answers "already running" and starts nothing.
func supervisorHeldByThisTest() seed {
	return seedHeldLock(".orchestrator/supervisor.lock")
}

// supervisorHolderLine is the "already running" answer naming a held supervisor lock's pid,
// which differs per runtime's sandbox; withSupervisorHolder writes it as <holder>.
const supervisorHolderLine = `supervisor: {"started":false,"reason":"already running","pid":<holder>}`

var supervisorHolderPid = regexp.MustCompile(`("reason":"already running","pid":)(\d+)`)

// withSupervisorHolder replaces the pid of an "already running" answer with <holder> when a
// lock holder this test started has it.
func withSupervisorHolder(s string) string {
	return supervisorHolderPid.ReplaceAllStringFunc(s, func(m string) string {
		parts := supervisorHolderPid.FindStringSubmatch(m)
		if _, held := lockHolders.Load(parts[2]); held {
			return parts[1] + "<holder>"
		}
		return m
	})
}

// kickoffFeedsFail makes both feeds fail, so each runtime's refresh keeps the seeded cache's
// readings as the last good ones and both route on the same postures.
func kickoffFeedsFail() seed {
	return seedCodex(map[int]string{
		1: `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"parity"}}`,
		3: `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"not logged in"}}`,
	})
}

type kickoffCase struct {
	name      string
	args      []string
	seed      seed
	crash     crashClass
	errorLine string
	// stdout is a line the JS output must hold, so that no case passes by both runtimes
	// failing the same unexpected way.
	stdout string
	// files are outcome files (by key) the JS run must leave with exactly this normalised
	// content.
	files map[string]string
}

// testKickoff compares `orch kickoff` with bin/kickoff.mjs, first in cases that never launch a
// conductor: each stops at the usage, the brief check, the both-red path or a dry run, or
// crashes before the launch. The supervisor lock is held by this test, so neither
// `supervisor start` starts a serve. The launching cases follow (testKickoffLaunches).
func (h *harness) testKickoff(t *testing.T) {
	h.testNestedProjectRoot(t, "kickoff")
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	c := newFlipCaches(t)
	brief := seedProjectFile("a.md", "# a\n")
	briefs := seeds(brief, seedProjectFile("b.md", "# b\n"), seedProjectFile("c.md", "# c\n"))
	dryRun := []string{"--brief", "a.md", "--dry-run"}
	// preflightOnly runs the whole preflight, then dies of the unknown door before any launch.
	preflightOnly := []string{"--brief", "a.md", "--door", "gemini"}
	gate := seedFlip(`{"from":"opencode","to":"claude","at":"2026-01-01T00:00:00.000Z","awaiting_human":{"reason":"push?"}}`)
	usageLine := cmdref.KickoffUsage()
	supervisorLine := supervisorHolderLine
	const dryRunSupervisorLine = "supervisor: not started (dry-run)"
	door := func(door, vendor, home, mode string) string {
		return "door: " + door + " (" + vendor + " green; home " + home + ") " + mode
	}
	gitFile := "gitdir: /elsewhere\n" // a .git file naming no directory: no repository, nothing written (Sol R13)
	opencodeGreen, claudeGreen := door("opencode", "openai", "opencode", "interactive"), door("claude", "anthropic", "opencode", "interactive")
	// A kickoff supersedes every queued launch intent (Sol R9): the gate, a pending launch and a
	// pause all go, the rest of the record stays.
	gatedWithIntent := seedFlip(`{"from":"opencode","to":"claude","at":"2026-01-01T00:00:00.000Z","awaiting_human":{"reason":"push?"},` +
		`"pending_launch":{"to":"opencode","from":"claude","reason":"handback","resume":null,"at":"2026-01-01T00:00:00.000Z"},"paused":{"at":"2026-01-01T00:00:00.000Z","door":"claude","reason":"r"}}`)
	gatedWithIntentCleared := map[string]string{"project/flip.json": "{\n  \"from\": \"opencode\",\n  \"to\": \"claude\",\n  \"at\": \"2026-01-01T00:00:00.000Z\"\n}\n"}
	// A real both-red kickoff saves its prompt as the conductor's brief and pauses naming it, so
	// the supervisor's relaunch at the reset starts from it (Sol R10); an older pause is dropped.
	bothRedMessage := "Both vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do."
	bothRedPaused := map[string]string{
		"project/briefs/conductor-kickoff.md": cmdref.KickoffPrompt("a.md", nil) + "\n",
		"project/flip.json": "{\n  \"paused\": {\n    \"at\": \"<iso>\",\n    \"door\": \"opencode\",\n    \"reason\": \"both vendors red\",\n" +
			"    \"brief\": \".orchestrator/briefs/conductor-kickoff.md\"\n  }\n}\n",
	}
	olderPause := seedFlip(`{"paused":{"at":"2026-01-01T00:00:00.000Z","door":"claude","reason":"r","brief":"old.md"}}`)
	cases := []kickoffCase{
		{name: "usage/no-args", stdout: usageLine},
		{name: "usage/help-without-brief", args: []string{"--help"}, stdout: usageLine},
		{name: "usage/help-with-brief", args: []string{"--help", "--brief", "a.md"}, seed: brief, stdout: usageLine},
		{name: "usage/empty-brief", args: []string{"--brief", "", "--dry-run"}, stdout: usageLine},
		{name: "brief/missing", args: []string{"--brief", "a.md", "--brief", "nope.md"}, seed: brief},
		{name: "brief/brief-not-found-names-root", args: []string{"--brief", "nope.md", "--dry-run"}},
		{name: "brief/brief-named-brief", args: []string{"--brief", "--brief", "a.md"}, seed: brief},
		{name: "brief/directory", args: []string{"--brief", ".git", "--dry-run"}, seed: c.green, stdout: "prompt: " + cmdref.KickoffPrompt(".git", nil)},
		{name: "cwd-bare", args: append(dryRun, "--cwd"), seed: brief, crash: crashC1, errorLine: cwdBoolean},
		{name: "dry-run/home-usable", args: dryRun, seed: seeds(brief, c.green), stdout: opencodeGreen},
		{name: "dry-run/home-red", args: dryRun, seed: seeds(brief, c.openaiRed), stdout: claudeGreen},
		{name: "dry-run/door-claude", args: append(dryRun, "--door", "claude"), seed: seeds(brief, c.green), stdout: claudeGreen},
		{name: "dry-run/door-auto", args: append(dryRun, "--door", "auto"), seed: seeds(brief, c.anthropicRed), stdout: opencodeGreen},
		{name: "dry-run/headless", args: append(dryRun, "--headless"), seed: seeds(brief, c.green), stdout: door("opencode", "openai", "opencode", "headless")},
		{name: "dry-run/side-briefs", args: append(dryRun, "--brief", "b.md", "--brief", "c.md"), seed: seeds(briefs, c.green), stdout: "prompt: " + cmdref.KickoffPrompt("a.md", []string{"b.md", "c.md"})},
		{name: "dry-run/unattended", args: append(dryRun, "--unattended"), seed: seeds(brief, c.green), stdout: dryRunSupervisorLine},
		{name: "dry-run/awaiting-human-kept", args: dryRun, seed: seeds(brief, c.green, gate), stdout: dryRunSupervisorLine},
		{name: "dry-run/git-file", args: dryRun, seed: seeds(brief, c.green, withGit(&gitFile)), stdout: opencodeGreen},
		{name: "preflight/unattended", args: append(preflightOnly, "--unattended"), seed: seeds(brief, c.green), crash: crashC2, stdout: supervisorLine},
		{name: "preflight/awaiting-human-cleared", args: preflightOnly, seed: seeds(brief, c.green, gate), crash: crashC2, stdout: supervisorLine},
		{name: "preflight/exclude-appended", args: preflightOnly, seed: seeds(brief, c.green, seedExclude("*.log")), crash: crashC2, stdout: supervisorLine},
		{name: "preflight/exclude-present", args: preflightOnly, seed: seeds(brief, c.green, seedExclude("*.log\n.orchestrator\n")), crash: crashC2, stdout: supervisorLine},
		{name: "preflight/no-git", args: preflightOnly, seed: seeds(brief, c.green, withGit(nil)), crash: crashC2, stdout: supervisorLine},
		{name: "preflight/gated-record-with-pending-launch", args: preflightOnly, seed: seeds(brief, c.green, gatedWithIntent), crash: crashC2, stdout: supervisorLine, files: gatedWithIntentCleared},
		{name: "preflight/git-file", args: preflightOnly, seed: seeds(brief, c.green, withGit(&gitFile)), crash: crashC2, stdout: supervisorLine},
		{name: "preflight/worktree-layout", args: preflightOnly, seed: seeds(brief, c.green, worktreeLayout()), crash: crashC2, stdout: supervisorLine,
			files: map[string]string{"extra/" + worktreeExclude: ".orchestrator/\n"}},
		{name: "door-gemini", args: append(dryRun, "--door", "gemini"), seed: seeds(brief, c.green), crash: crashC2, stdout: dryRunSupervisorLine},
		{name: "door-bare", args: append(dryRun, "--door"), seed: seeds(brief, c.green), crash: crashC2, stdout: dryRunSupervisorLine},
		{name: "both-red/dry-run", args: dryRun, seed: seeds(brief, c.bothRed), stdout: "dry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset."},
		{name: "both-red/pause", args: []string{"--brief", "a.md"}, seed: seeds(brief, c.bothRed), stdout: supervisorLine + "\n" + `{"paused":true,"door":"opencode"}` + "\n" + bothRedMessage, files: bothRedPaused},
		{name: "both-red/pause-unattended", args: []string{"--brief", "a.md", "--unattended"}, seed: seeds(brief, c.bothRed), stdout: supervisorLine, files: bothRedPaused},
		{name: "both-red/replaces-an-older-pause", args: []string{"--brief", "a.md", "--headless"}, seed: seeds(brief, c.bothRed, olderPause), stdout: bothRedMessage, files: bothRedPaused},
	}
	for _, kc := range cases {
		t.Run(kc.name, func(t *testing.T) {
			t.Parallel()
			h.compareKickoff(t, h.kickoffInvocation(kc), kc.stdout, kc.files)
		})
	}
	// Exit 75 promises a relaunch at the reset, so a both-red kickoff whose supervisor did not
	// start (its log directory is a file) fails with exit 1 instead (C3-F2). The failing
	// supervisor's error is pinned in the answer kickoff prints.
	notDeferred := []notDeferredCase{
		{name: "both-red/supervisor-fails", failed: "the supervisor did not start", seed: seedProjectFile(".orchestrator/logs", "not a directory\n"),
			supervisorError: "Error: EEXIST: file already exists, mkdir '"},
	}
	for _, nc := range notDeferred {
		t.Run(nc.name, func(t *testing.T) {
			t.Parallel()
			inv := h.kickoffInvocation(kickoffCase{args: []string{"--brief", "a.md"}, seed: seeds(brief, c.bothRed, nc.seed)})
			h.compareKickoffNotDeferred(t, inv, nc)
		})
	}
	// PE-61: orch stops a kickoff whose gate clear refuses an unreadable flip.json; the JS
	// ignores the clear. A usable-vendor kickoff (dying of the unknown door after the preflight)
	// with a corrupt flip.json, and a both-red one with flip.json a directory (the JS fails
	// later, at the pause, as C3-F2 pinned it).
	for _, rc := range []refusedClearCase{
		{name: "PE-61/corrupt-flip-json", args: preflightOnly, seed: seedFlip("{not json\n"), code: "INVALID_JSON", jsExit: 1,
			jsStdout: supervisorLine, jsStderr: "TypeError: Cannot read properties of undefined (reading 'overall')",
			jsFiles: map[string]string{"project/flip.json": "{}\n", "extra/.git/info/exclude": ".orchestrator/\n"}},
		{name: "PE-61/both-red-flip-json-directory", args: []string{"--brief", "a.md"}, seed: seeds(c.bothRed, seedProjectDir(".orchestrator/flip.json")), code: "EISDIR", jsExit: 1,
			jsStdout: supervisorLine, jsStderr: "Both vendors are red, but the pause was not recorded: nothing will relaunch at the reset. Fix it and run kickoff again.",
			jsFiles: map[string]string{"project/briefs/conductor-kickoff.md": cmdref.KickoffPrompt("a.md", nil) + "\n"}},
	} {
		t.Run(rc.name, func(t *testing.T) {
			t.Parallel()
			rc.seed = seeds(brief, c.green, rc.seed)
			h.testKickoffRefusedGateClear(t, rc)
		})
	}
	// The guarantee is about the project: like every routing read, a dry run still refreshes the
	// shared usage cache under HOME, by design (Sol R18).
	t.Run("dry-run-changes-nothing-in-the-project", func(t *testing.T) {
		t.Parallel()
		h.testKickoffDryRunChangesNothingInTheProject(t, seeds(brief, c.green, gate, seedExclude("*.log\n")))
	})
	t.Run("dry-run-then-real/worktree-layout", func(t *testing.T) {
		t.Parallel()
		h.testKickoffDryRunThenReal(t, seeds(brief, c.green, worktreeLayout()), dryRun, preflightOnly)
	})
	t.Run("registry-home-claude", func(t *testing.T) {
		t.Parallel()
		root := h.jsTreeWithRegistry(t, h.registryWithHome(t, "claude"))
		inv := h.kickoffInvocation(kickoffCase{args: dryRun, seed: seeds(brief, c.green)})
		inv.js[1] = filepath.Join(root, "bin", "kickoff.mjs")
		inv.env = append(inv.env, "ORCH_HOME="+root, "ORCH_REGISTRY="+filepath.Join(root, "models.json"))
		h.compareKickoff(t, inv, door("claude", "anthropic", "claude", "interactive"), nil)
	})
	t.Run("PE-2/registry-missing", func(t *testing.T) {
		t.Parallel()
		h.testKickoffRegistryMissing(t, seeds(brief, c.green))
	})
	t.Run("launch", h.testKickoffLaunches)
	t.Run("cleanup/unread-serve", h.testKickoffUnreadServeCleanup)
}

// compareKickoff checks that the JS run printed the expected line (or nothing, for "") and
// left the expected files, then compares the pair.
func (h *harness) compareKickoff(t *testing.T, inv invocation, line string, files map[string]string) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	if line == "" && js.stdout != "" || line != "" && !strings.Contains("\n"+js.stdout, "\n"+line+"\n") {
		t.Errorf("js stdout %q, want the line %q (none for \"\")", js.stdout, line)
	}
	for key, want := range files {
		if got := js.files[key]; got != want {
			t.Errorf("js %s = %q, want %q", key, got, want)
		}
	}
	if inv.crash != noCrash {
		if strings.Contains(strings.Join(inv.orch, " "), "--door gemini") || inv.orch[len(inv.orch)-1] == "--door" {
			if js.exitCode != 1 || !strings.Contains(js.stderr, "Cannot read properties of undefined (reading 'overall')") {
				t.Errorf("frozen JS: %+v", js)
			}
			if orch.exitCode != 2 || orch.stdout != "" || orch.stderr != "kickoff: --door must be opencode|claude|auto\n" {
				t.Errorf("PE-57: %+v", orch)
			}
			return
		}
		if err := crashError(inv.crash, inv.errorLine, js, orch); err != nil {
			t.Error(err)
		}
		js.stderr, orch.stderr = "", ""
	}
	compareOutcomes(t, js, orch)
}

// refusedClearCase is a kickoff whose `flip gate --clear` orch refuses with the StateError code
// (PE-61). jsExit, jsStdout, jsStderr (a line of each) and jsFiles (exact contents) pin the
// frozen JS's old behaviour, which goes on past the clear.
type refusedClearCase struct {
	name, code         string
	args               []string
	seed               seed
	jsExit             int
	jsStdout, jsStderr string
	jsFiles            map[string]string
}

// testKickoffRefusedGateClear asserts PE-61: orch exits 1 naming the refused clear, prints no
// supervisor line and leaves every project file as seeded (no supervisor state, exclude,
// brief or pause; the empty flip.json.lock a transaction leaves is not a change). The JS side
// is checked against the pinned old behaviour; the pair is not compared, as the runtimes
// differ by design.
func (h *harness) testKickoffRefusedGateClear(t *testing.T, rc refusedClearCase) {
	t.Helper()
	inv := h.kickoffInvocation(kickoffCase{args: rc.args, seed: rc.seed})
	// Keep PE-61 about the refused clear: PE-57 now rejects the JS's sentinel door earlier.
	for i, arg := range inv.orch {
		if arg == "gemini" {
			inv.orch[i] = "opencode"
		}
	}
	var runs int
	var orchChanged []string
	inv.normalizeWrites = func(o outcome, before map[string]string) outcome {
		o = flipTransactionSidecar(o, before)
		if runs++; runs == 2 { // runPair runs the JS, then orch
			for key, content := range o.files {
				if seeded, ok := before[key]; isProjectFile(key) && (!ok || seeded != content) {
					orchChanged = append(orchChanged, key)
				}
			}
			for key := range before {
				if _, kept := o.files[key]; !kept {
					orchChanged = append(orchChanged, key)
				}
			}
		}
		return withLockHolderPids(o)
	}
	js, orch := h.runPair(t, inv)
	if js.exitCode != rc.jsExit || !strings.Contains("\n"+js.stdout, "\n"+rc.jsStdout+"\n") || !strings.Contains("\n"+js.stderr, "\n"+rc.jsStderr+"\n") {
		t.Errorf("js: exit %d, stdout %q, stderr %q; want the pinned old behaviour: exit %d, the line %q and %q", js.exitCode, js.stdout, js.stderr, rc.jsExit, rc.jsStdout, rc.jsStderr)
	}
	for key, want := range rc.jsFiles {
		if got := js.files[key]; got != want {
			t.Errorf("js %s = %q, want the pinned %q", key, got, want)
		}
	}
	want := "kickoff: the human gate could not be cleared: Error: flip state unreadable: $PROJECT/.orchestrator/flip.json: " + rc.code + "; nothing was launched\n"
	if orch.exitCode != 1 || orch.stderr != want || strings.Contains(orch.stdout, "supervisor:") {
		t.Errorf("orch: exit %d, stdout %q, stderr %q; want 1, no supervisor line and %q", orch.exitCode, orch.stdout, orch.stderr, want)
	}
	if runs != 2 || len(orchChanged) > 0 {
		t.Errorf("orch changed %q (%d runs), want every project file as seeded", orchChanged, runs)
	}
}

// notDeferredCase is a both-red kickoff that must fail with exit 1 because the step named by
// failed did. supervisorError starts the error the failing supervisor's answer holds ("": the
// answer is compared as it is); pauseError starts the error line the failing pause prints on
// stderr before kickoff's own line ("": kickoff's line is all of stderr).
type notDeferredCase struct {
	name, failed                string
	seed                        seed
	briefSaved                  bool
	supervisorError, pauseError string
}

// compareKickoffNotDeferred checks that each runtime failed a both-red kickoff with exit 1,
// ending its stderr with the line naming what failed, printing no pause message and saving
// the kickoff brief only when briefSaved, then compares the pair. The failing child's own
// error differs between the runtimes (a Node stack, an orch error line; PE-2), so each side's
// is checked to be the expected error and then removed: the supervisor's answer, when it
// failed, becomes "<answer>", and the pause's diagnostic is dropped from stderr. Everything
// else is compared, as is every file but the temp the JS leaves beside a flip.json it could
// not replace (PE-32: orch now refuses the unreadable record before a replacement).
func (h *harness) compareKickoffNotDeferred(t *testing.T, inv invocation, nc notDeferredCase) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	want := "Both vendors are red, but " + nc.failed + ": nothing will relaunch at the reset. Fix it and run kickoff again.\n"
	for side, o := range map[string]*outcome{"js": &js, "orch": &orch} {
		_, saved := o.files["project/briefs/conductor-kickoff.md"]
		diagnostic, endsWithWant := strings.CutSuffix(o.stderr, want)
		if o.exitCode != 1 || !endsWithWant || strings.Contains(o.stdout, "paused") || saved != nc.briefSaved {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, brief saved %v; want exit 1 ending %q, no pause and brief saved %v", side, o.exitCode, o.stdout, o.stderr, saved, want, nc.briefSaved)
		}
		expectedPauseError := nc.pauseError
		if side == "orch" && nc.pauseError != "" {
			expectedPauseError = "Error: flip state unreadable: $PROJECT/.orchestrator/flip.json: EISDIR"
		}
		if err := checkChildDiagnostic(side, diagnostic, expectedPauseError); err != "" {
			t.Errorf("pause diagnostic: %s", err)
		}
		o.stderr = want
		if nc.supervisorError != "" {
			o.stdout = replaceFailedSupervisorAnswer(t, side, o.stdout, nc.supervisorError)
		}
		delete(o.files, "project/flip.json.tmp")
	}
	compareOutcomes(t, js, orch)
}

// checkChildDiagnostic reports what is wrong with the stderr a failing child left before
// kickoff's own line: with no expected error it must be empty; orch's is the one error line;
// the JS's is Node's crash output, holding the error line and ending with Node's version.
func checkChildDiagnostic(side, diagnostic, expected string) string {
	switch {
	case expected == "":
		if diagnostic != "" {
			return fmt.Sprintf("%s: unexpected stderr %q", side, diagnostic)
		}
	case side == "orch":
		if !strings.HasPrefix(diagnostic, expected) || strings.Count(diagnostic, "\n") != 1 || !strings.HasSuffix(diagnostic, "\n") {
			return fmt.Sprintf("orch: stderr %q, want one line starting %q", diagnostic, expected)
		}
	default:
		lines := strings.Split(strings.TrimSuffix(diagnostic, "\n"), "\n")
		holdsError := slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, expected) })
		if !holdsError || !strings.HasPrefix(lines[len(lines)-1], "Node.js v") {
			return fmt.Sprintf("js: stderr %q, want Node's crash output with a line starting %q", diagnostic, expected)
		}
	}
	return ""
}

// replaceFailedSupervisorAnswer replaces the supervisor's answer, the last thing on stdout, with
// "<answer>" after checking that it holds the expected error and is no longer than the 120
// UTF-16 units kickoff prints of it, so nothing printed after it can be hidden.
func replaceFailedSupervisorAnswer(t *testing.T, side, stdout, expected string) string {
	t.Helper()
	before, answer, found := strings.Cut(stdout, "\nsupervisor: ")
	answer, endsWithNewline := strings.CutSuffix(answer, "\n")
	if !found || !endsWithNewline || !strings.Contains(answer, expected) || jsstr.Len(answer) > 120 {
		t.Errorf("%s: stdout %q, want it to end with a supervisor answer of at most 120 units holding %q", side, stdout, expected)
	}
	return before + "\nsupervisor: <answer>\n"
}

// kickoffInvocation runs bin/kickoff.mjs and `orch kickoff` in the sandbox project, which is
// also the root every child is given; the supervisor lock is held by this test.
func (h *harness) kickoffInvocation(c kickoffCase) invocation {
	return invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "kickoff.mjs")}, c.args...),
		orch:         append([]string{h.orchBin, "kickoff"}, c.args...),
		env:          []string{noPostRunRefresh},
		seed:         seeds(supervisorHeldByThisTest(), kickoffFeedsFail(), orNothing(c.seed)),
		crash:        c.crash,
		errorLine:    c.errorLine,
		projectPaths: true,
		extraFiles:   []string{".git/info/exclude", worktreeExclude},
		// a case may make .git a file
		extraFilesMayBeBlocked: true,
		normalize: func(s string) string {
			return withSupervisorHolder(normalizeFlip(time.Now().Add(-time.Minute))(normalizeGeneratedAt(s)))
		},
		normalizeWrites: func(o outcome, before map[string]string) outcome {
			if !slices.Contains(c.args, "--dry-run") {
				o = flipTransactionSidecar(o, before)
			}
			return withLockHolderPids(o)
		},
	}
}

// registryWithHome is the JS registry with conductor.home set to door.
func (h *harness) registryWithHome(t *testing.T, door string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.orchJS, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	jsjson.Path(registry, "conductor").(*jsjson.Object).Set("home", door)
	out, err := jsjson.MarshalIndent(registry, "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// testKickoffRegistryMissing asserts PE-2: without models.json the JS dies of the rejected
// getRouting with Node's ENOENT line; orch prints it as `orch kickoff: <error>`. Both exit 1
// after the brief check and before any side effect.
func (h *harness) testKickoffRegistryMissing(t *testing.T, s seed) {
	root := h.jsTreeWithRegistry(t, registryMissing)
	inv := h.kickoffInvocation(kickoffCase{args: []string{"--brief", "a.md", "--dry-run"}, seed: s})
	inv.js[1] = filepath.Join(root, "bin", "kickoff.mjs")
	inv.env = append(inv.env, "ORCH_HOME="+root, "ORCH_REGISTRY="+filepath.Join(root, "models.json"))
	js, orch := h.runPair(t, inv)
	enoent := "ENOENT: no such file or directory, open '" + filepath.Join(root, "models.json") + "'"
	if js.exitCode != 1 || !strings.Contains(js.stderr, "\nError: "+enoent+"\n") {
		t.Errorf("js: exit %d, stderr %q; want 1 and Error: %s", js.exitCode, js.stderr, enoent)
	}
	if want := "orch kickoff: " + enoent + "\n"; orch.exitCode != 1 || orch.stderr != want {
		t.Errorf("orch: exit %d, stderr %q; want 1 and %q", orch.exitCode, orch.stderr, want)
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

// testKickoffDryRunChangesNothingInTheProject asserts that a headless, unattended dry run with
// a usable door leaves the project as seeded: no policy, the human gate kept, no supervisor
// state, no conductor brief and .git/info/exclude untouched. The usage cache under HOME is not
// the project's and is refreshed by design. It then compares the pair.
func (h *harness) testKickoffDryRunChangesNothingInTheProject(t *testing.T, s seed) {
	inv := h.kickoffInvocation(kickoffCase{args: []string{"--brief", "a.md", "--headless", "--unattended", "--dry-run"}, seed: s})
	var changed, unseeded []string
	inv.normalizeWrites = func(o outcome, before map[string]string) outcome {
		for _, key := range []string{"project/flip.json", "extra/.git/info/exclude"} {
			if before[key] == "" {
				unseeded = append(unseeded, key)
			}
		}
		for key, content := range o.files {
			if isProjectFile(key) && before[key] != content {
				changed = append(changed, key)
			}
		}
		for key := range before {
			if _, kept := o.files[key]; !kept {
				changed = append(changed, key)
			}
		}
		return withLockHolderPids(o)
	}
	js, orch := h.runPair(t, inv)
	if len(unseeded) > 0 {
		t.Errorf("%q not seeded, so their changes cannot be seen", unseeded)
	}
	if len(changed) > 0 {
		t.Errorf("a dry run changed %q, want nothing", changed)
	}
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 0 || !strings.Contains(o.stdout, "\nsupervisor: not started (dry-run)\n") {
			t.Errorf("%s: exit %d, stdout %q; want 0 and the dry-run supervisor line", side, o.exitCode, o.stdout)
		}
	}
	compareOutcomes(t, js, orch)
}

// testKickoffDryRunThenReal runs, in one sandbox per runtime, the dry run then the real
// preflight (Sol R13): the dry run must succeed and leave the worktree's exclude alone, and the
// real run must then add the line there. The real runs are compared; both die of the unknown
// door after the preflight (C2).
func (h *harness) testKickoffDryRunThenReal(t *testing.T, s seed, dryRun, real []string) {
	dry := h.kickoffInvocation(kickoffCase{args: dryRun, seed: s})
	then := h.kickoffInvocation(kickoffCase{args: real})
	run := func(side string, dryArgv, realArgv []string) outcome {
		sb := h.newSandbox(t)
		dry.seed(t, sb)
		first := h.run(t, sb, dryArgv, nil, dry.env)
		collectExtraFiles(t, sb, dry, first.files)
		if _, written := first.files["extra/"+worktreeExclude]; first.exitCode != 0 || written {
			t.Errorf("%s dry run: exit %d, exclude written %v, stderr %q; want 0 and no exclude", side, first.exitCode, written, first.stderr)
		}
		o := h.run(t, sb, realArgv, nil, then.env)
		collectExtraFiles(t, sb, then, o.files)
		o = then.normalizeWrites(o, first.files)
		return o.withProjectPlaceholder(t, sb).normalized(then.normalize)
	}
	js, orch := run("js", dry.js, then.js), run("orch", dry.orch, then.orch)
	if got := js.files["extra/"+worktreeExclude]; got != ".orchestrator/\n" {
		t.Errorf("JS exclude %q", got)
	}
	if js.exitCode != 1 || !strings.Contains(js.stderr, "Cannot read properties of undefined (reading 'overall')") {
		t.Errorf("frozen JS: %+v", js)
	}
	if orch.exitCode != 2 || orch.stderr != "kickoff: --door must be opencode|claude|auto\n" || orch.files["extra/"+worktreeExclude] != "" {
		t.Errorf("PE-57: %+v", orch)
	}
}

// isProjectFile reports whether an outcome's file key is in the project: its .orchestrator/
// directory or an extra file such as .git/info/exclude.
func isProjectFile(key string) bool {
	return strings.HasPrefix(key, "project/") || strings.HasPrefix(key, "extra/")
}
