//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// supervisorHeldByThisTest locks the project's supervisor with this test process's pid, which
// is only ever sent signal 0: `supervisor start` answers "already running" and starts nothing.
func supervisorHeldByThisTest() seed {
	return seedProjectFile(".orchestrator/supervisor.lock", strconv.Itoa(os.Getpid())+"\n")
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
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	c := newFlipCaches(t)
	brief := seedProjectFile("a.md", "# a\n")
	briefs := seeds(brief, seedProjectFile("b.md", "# b\n"), seedProjectFile("c.md", "# c\n"))
	dryRun := []string{"--brief", "a.md", "--dry-run"}
	// preflightOnly runs the whole preflight, then dies of the unknown door before any launch.
	preflightOnly := []string{"--brief", "a.md", "--door", "gemini"}
	gate := seedFlip(`{"from":"opencode","to":"claude","at":"2026-01-01T00:00:00.000Z","awaiting_human":{"reason":"push?"}}`)
	usageLine := cmdref.KickoffUsage()
	supervisorLine := `supervisor: {"started":false,"reason":"already running","pid":` + strconv.Itoa(os.Getpid()) + `}`
	const dryRunSupervisorLine = "supervisor: not started (dry-run)"
	door := func(door, vendor, home, mode string) string {
		return "door: " + door + " (" + vendor + " green; home " + home + ") " + mode
	}
	gitFile := "gitdir: /elsewhere\n" // a worktree's .git: the exclude step dies of ENOTDIR (Q-S7-2)
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
		{name: "preflight/git-file", args: preflightOnly, seed: seeds(brief, c.green, withGit(&gitFile)), crash: crashC1, errorLine: "Error: ENOTDIR: not a directory, mkdir '$PROJECT/.git/info'", stdout: supervisorLine},
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
	// start (its log directory is a file) or whose pause was not recorded (flip.json is a
	// directory) fails with exit 1 instead (C3-F2).
	notDeferred := []struct {
		name, failed string
		seed         seed
		briefSaved   bool
	}{
		{"both-red/supervisor-fails", "the supervisor did not start", seedProjectFile(".orchestrator/logs", "not a directory\n"), false},
		{"both-red/pause-fails", "the pause was not recorded", seedProjectDir(".orchestrator/flip.json"), true},
	}
	for _, nc := range notDeferred {
		t.Run(nc.name, func(t *testing.T) {
			t.Parallel()
			inv := h.kickoffInvocation(kickoffCase{args: []string{"--brief", "a.md"}, seed: seeds(brief, c.bothRed, nc.seed)})
			h.compareKickoffNotDeferred(t, inv, nc.failed, nc.briefSaved)
		})
	}
	t.Run("dry-run-makes-no-changes", func(t *testing.T) {
		t.Parallel()
		h.testKickoffDryRunMakesNoChanges(t, seeds(brief, c.green, gate, seedExclude("*.log\n")))
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
		if err := crashError(inv.crash, inv.errorLine, js, orch); err != nil {
			t.Error(err)
		}
		js.stderr, orch.stderr = "", ""
	}
	compareOutcomes(t, js, orch)
}

// compareKickoffNotDeferred checks that each runtime failed a both-red kickoff with exit 1,
// ending its stderr with the line naming what failed, printing no pause message and saving
// the kickoff brief only when briefSaved, then compares the pair. The failing child's own
// error text differs between the runtimes (a Node stack, an orch error line), so the
// supervisor's answer, the last thing on stdout, and the stderr before the last line are left
// out of the comparison, as is the temp file the JS leaves beside a flip.json it could not
// replace (PE-21).
func (h *harness) compareKickoffNotDeferred(t *testing.T, inv invocation, failed string, briefSaved bool) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	want := "Both vendors are red, but " + failed + ": nothing will relaunch at the reset. Fix it and run kickoff again.\n"
	for side, o := range map[string]*outcome{"js": &js, "orch": &orch} {
		_, saved := o.files["project/briefs/conductor-kickoff.md"]
		if o.exitCode != 1 || !strings.HasSuffix(o.stderr, "\n"+want) && o.stderr != want || strings.Contains(o.stdout, "paused") || saved != briefSaved {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, brief saved %v; want exit 1 ending %q, no pause and brief saved %v", side, o.exitCode, o.stdout, o.stderr, saved, want, briefSaved)
		}
		o.stderr = want
		if before, _, found := strings.Cut(o.stdout, "\nsupervisor: "); found {
			o.stdout = before + "\nsupervisor: <answer>\n"
		}
		delete(o.files, "project/flip.json.tmp")
	}
	compareOutcomes(t, js, orch)
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
		extraFiles:   []string{".git/info/exclude"},
		// a case may make .git a file
		extraFilesMayBeBlocked: true,
		normalize: func(s string) string {
			return normalizeFlip(time.Now().Add(-time.Minute))(normalizeGeneratedAt(s))
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

// testKickoffDryRunMakesNoChanges asserts that a headless, unattended dry run with a usable door
// leaves the project as seeded: no policy, the human gate kept, no supervisor state, no
// conductor brief and .git/info/exclude untouched. It then compares the pair.
func (h *harness) testKickoffDryRunMakesNoChanges(t *testing.T, s seed) {
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
		return o
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

// isProjectFile reports whether an outcome's file key is in the project: its .orchestrator/
// directory or an extra file such as .git/info/exclude.
func isProjectFile(key string) bool {
	return strings.HasPrefix(key, "project/") || strings.HasPrefix(key, "extra/")
}
