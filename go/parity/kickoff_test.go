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
}

// testKickoff compares `orch kickoff` with bin/kickoff.mjs in cases that never launch a
// conductor: each stops at the usage, the brief check, the both-red path or a dry run, or
// crashes before the launch. The supervisor lock is held by this test, so neither
// `supervisor start` starts a serve.
func (h *harness) testKickoff(t *testing.T) {
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	c := newFlipCaches(t)
	brief := seedProjectFile("a.md", "# a\n")
	briefs := seeds(brief, seedProjectFile("b.md", "# b\n"), seedProjectFile("c.md", "# c\n"))
	dryRun := []string{"--brief", "a.md", "--dry-run"}
	usageLine := cmdref.KickoffUsage()
	supervisorLine := `supervisor: {"started":false,"reason":"already running","pid":` + strconv.Itoa(os.Getpid()) + `}`
	door := func(door, vendor, home, mode string) string {
		return "door: " + door + " (" + vendor + " green; home " + home + ") " + mode
	}
	gitFile := "gitdir: /elsewhere\n" // a worktree's .git: the exclude step dies of ENOTDIR (Q-S7-2)
	opencodeGreen, claudeGreen := door("opencode", "openai", "opencode", "interactive"), door("claude", "anthropic", "opencode", "interactive")
	cases := []kickoffCase{
		{name: "usage/no-args", stdout: usageLine},
		{name: "usage/help-without-brief", args: []string{"--help"}, stdout: usageLine},
		{name: "usage/help-with-brief", args: []string{"--help", "--brief", "a.md"}, seed: brief, stdout: usageLine},
		{name: "usage/empty-brief", args: []string{"--brief", "", "--dry-run"}, stdout: usageLine},
		{name: "brief/missing", args: []string{"--brief", "a.md", "--brief", "nope.md"}, seed: brief},
		{name: "brief/brief-named-brief", args: []string{"--brief", "--brief", "a.md"}, seed: brief},
		{name: "brief/directory", args: []string{"--brief", ".git", "--dry-run"}, seed: c.green, stdout: "prompt: " + cmdref.KickoffPrompt(".git", nil)},
		{name: "cwd-bare", args: append(dryRun, "--cwd"), seed: brief, crash: crashC1, errorLine: cwdBoolean},
		{name: "dry-run/home-usable", args: dryRun, seed: seeds(brief, c.green), stdout: opencodeGreen},
		{name: "dry-run/home-red", args: dryRun, seed: seeds(brief, c.openaiRed), stdout: claudeGreen},
		{name: "dry-run/door-claude", args: append(dryRun, "--door", "claude"), seed: seeds(brief, c.green), stdout: claudeGreen},
		{name: "dry-run/door-auto", args: append(dryRun, "--door", "auto"), seed: seeds(brief, c.anthropicRed), stdout: opencodeGreen},
		{name: "dry-run/headless", args: append(dryRun, "--headless"), seed: seeds(brief, c.green), stdout: door("opencode", "openai", "opencode", "headless")},
		{name: "dry-run/side-briefs", args: append(dryRun, "--brief", "b.md", "--brief", "c.md"), seed: seeds(briefs, c.green), stdout: "prompt: " + cmdref.KickoffPrompt("a.md", []string{"b.md", "c.md"})},
		{name: "dry-run/unattended", args: append(dryRun, "--unattended"), seed: seeds(brief, c.green), stdout: supervisorLine},
		{name: "dry-run/awaiting-human-cleared", args: dryRun, seed: seeds(brief, c.green, seedFlip(`{"from":"opencode","to":"claude","at":"2026-01-01T00:00:00.000Z","awaiting_human":{"reason":"push?"}}`)), stdout: supervisorLine},
		{name: "dry-run/exclude-appended", args: dryRun, seed: seeds(brief, c.green, seedExclude("*.log")), stdout: opencodeGreen},
		{name: "dry-run/exclude-present", args: dryRun, seed: seeds(brief, c.green, seedExclude("*.log\n.orchestrator\n")), stdout: opencodeGreen},
		{name: "dry-run/no-git", args: dryRun, seed: seeds(brief, c.green, withGit(nil)), stdout: opencodeGreen},
		{name: "dry-run/git-file", args: dryRun, seed: seeds(brief, c.green, withGit(&gitFile)), crash: crashC1, errorLine: "Error: ENOTDIR: not a directory, mkdir '$PROJECT/.git/info'", stdout: supervisorLine},
		{name: "door-gemini", args: append(dryRun, "--door", "gemini"), seed: seeds(brief, c.green), crash: crashC2, stdout: supervisorLine},
		{name: "door-bare", args: append(dryRun, "--door"), seed: seeds(brief, c.green), crash: crashC2, stdout: supervisorLine},
		{name: "both-red/dry-run", args: dryRun, seed: seeds(brief, c.bothRed), stdout: "dry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset."},
		{name: "both-red/pause", args: []string{"--brief", "a.md"}, seed: seeds(brief, c.bothRed), stdout: "Both vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do."},
	}
	for _, kc := range cases {
		t.Run(kc.name, func(t *testing.T) {
			t.Parallel()
			h.compareKickoff(t, h.kickoffInvocation(kc), kc.stdout)
		})
	}
	t.Run("registry-home-claude", func(t *testing.T) {
		t.Parallel()
		root := h.jsTreeWithRegistry(t, h.registryWithHome(t, "claude"))
		inv := h.kickoffInvocation(kickoffCase{args: dryRun, seed: seeds(brief, c.green)})
		inv.js[1] = filepath.Join(root, "bin", "kickoff.mjs")
		inv.env = append(inv.env, "ORCH_HOME="+root, "ORCH_REGISTRY="+filepath.Join(root, "models.json"))
		h.compareKickoff(t, inv, door("claude", "anthropic", "claude", "interactive"))
	})
	t.Run("PE-2/registry-missing", func(t *testing.T) {
		t.Parallel()
		h.testKickoffRegistryMissing(t, seeds(brief, c.green))
	})
}

// compareKickoff checks that the JS run printed the expected line (or nothing, for ""), then compares the pair.
func (h *harness) compareKickoff(t *testing.T, inv invocation, line string) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	if line == "" && js.stdout != "" || line != "" && !strings.Contains("\n"+js.stdout, "\n"+line+"\n") {
		t.Errorf("js stdout %q, want the line %q (none for \"\")", js.stdout, line)
	}
	if inv.crash != noCrash {
		if err := crashError(inv.crash, inv.errorLine, js, orch); err != nil {
			t.Error(err)
		}
		js.stderr, orch.stderr = "", ""
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
