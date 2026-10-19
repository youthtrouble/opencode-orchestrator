//go:build parity

package parity

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
)

// kickoffLaunchArgs is the kickoff every launching case runs, before its door and mode flags.
var kickoffLaunchArgs = []string{"--brief", "a.md"}

// kickoffSupervisorStarted is the supervisor line of a kickoff whose `supervisor start`
// launched a serve: its pid, and its log path, which the 120-unit cut may end anywhere in.
var kickoffSupervisorStarted = regexp.MustCompile(`(?m)^(supervisor: \{"started":true,"supervisor_pid":)(\d+)(,"log":").*$`)

// testKickoffLaunches compares the kickoffs that launch something: a headless conductor lane
// on each door, each door's interactive CLI (its exit code, and a signal ending it), and a
// supervisor serve really started. Each asserts on both sides that the launch happened before
// the pair is compared, so no case passes by both runtimes launching nothing.
func (h *harness) testKickoffLaunches(t *testing.T) {
	t.Run("headless/claude", func(t *testing.T) {
		h.compareKickoffHeadless(t, claudeBridge, bridgeCase{fixture: "ok"}, "--door", "claude")
	})
	t.Run("headless/opencode", func(t *testing.T) {
		h.compareKickoffHeadless(t, opencodeBridge, bridgeCase{script: opencodeRunOK})
	})
	claudeTUI := func(script map[string]string) seed {
		return seeds(seedFile("fake-log/.keep", ""), h.seedFake(t, claudeBridge, bridgeCase{script: script}))
	}
	opencodeTUI := func(files map[string]string) seed {
		all := []seed{seedFile("fake-log/.keep", "")}
		for name, content := range files {
			all = append(all, seedFile("fake-opencode-tui/"+name, content))
		}
		return seeds(all...)
	}
	// A signal case's exit file holds 9, so that a signal not delivered fails it.
	interactive := []struct {
		name   string
		door   string
		tui    seed
		exit   int
		argv   string // the fake's argv log
		before []string
	}{
		{"interactive/claude-exit", "claude", claudeTUI(map[string]string{"exit": "3\n"}), 3, "claude-argv.txt", []string{"--agent", "conductor"}},
		{"interactive/claude-signal", "claude", claudeTUI(map[string]string{"signal": "TERM\n", "exit": "9\n"}), 0, "claude-argv.txt", []string{"--agent", "conductor"}},
		{"interactive/opencode-exit", "opencode", opencodeTUI(map[string]string{"exit": "4\n"}), 4, "opencode-tui-argv.txt", []string{"--prompt"}},
		{"interactive/opencode-sigint", "opencode", opencodeTUI(map[string]string{"signal": "INT\n", "exit": "9\n"}), 0, "opencode-tui-argv.txt", []string{"--prompt"}},
	}
	for _, c := range interactive {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h.compareKickoffInteractive(t, c.door, c.tui, c.exit, "fakelog/"+c.argv, append(c.before, cmdref.KickoffPrompt("a.md", nil)))
		})
	}
	t.Run("supervisor-started", h.testKickoffSupervisorStarted)
}

// kickoffBrief is the brief every launching case kicks off, with the fake logs' directory.
func kickoffBrief() seed {
	return seeds(seedProjectFile("a.md", "# a\n"), seedFile("fake-log/.keep", ""))
}

// kickoffRoutedGreen is kickoffBrief with green caches whose refresh fails, so both runtimes
// route on the seeded readings. It is built once per case: the caches' times are fixed then.
func kickoffRoutedGreen(t *testing.T) seed {
	return seeds(kickoffBrief(), newFlipCaches(t).green, kickoffFeedsFail())
}

// compareKickoffHeadless runs a headless kickoff on the bridge's door in each runtime's own
// sandbox, settles the conductor lane's runner by the pid each printed, and compares the
// kickoff's output with everything the runner wrote (lane state, log, mailbox, run record).
// The routing refresh's feeds fail over the bridge's green cache (see withoutPostRunSession).
func (h *harness) compareKickoffHeadless(t *testing.T, b bridge, run bridgeCase, door ...string) {
	t.Helper()
	inv := h.workerStartInvocation(t, workerStartCase{bridge: b, run: run, brief: text("")})
	args := append(append(append([]string{}, kickoffLaunchArgs...), "--headless"), door...)
	inv.js = append([]string{h.node, filepath.Join(h.orchJS, "bin", "kickoff.mjs")}, args...)
	inv.orch = append([]string{h.orchBin, "kickoff"}, args...)
	inv.seed = seeds(inv.seed, supervisorHeldByThisTest(), kickoffBrief(), kickoffFeedsFail())
	inv.extraFiles = []string{".git/info/exclude"}
	worker, fresh := inv.normalize, normalizeFlip(time.Now())
	inv.normalize = func(s string) string { return fresh(normalizeGeneratedAt(worker(s))) }
	launched := workerDetachCase{launchLine: kickoffLauncherLine}
	runners := strings.NewReplacer(h.runnerPrefixes(t)...)
	js := h.runDetached(t, inv, nil, inv.js, launched, runners)
	orch := h.runDetached(t, inv, nil, inv.orch, launched, runners)
	js, orch = withoutPostRunSession(t, js, orch)
	brief := cmdref.KickoffPrompt("a.md", nil) + "\n"
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 0 || o.files["project/briefs/conductor-kickoff.md"] != brief || o.files["fakelog/"+b.name+"-argv.txt"] == "" {
			t.Errorf("%s: exit %d, stdout %q; want a launched conductor lane that ran the fake %s", side, o.exitCode, o.stdout, b.name)
		}
	}
	compareOutcomes(t, js, orch)
}

// codexAppServerLog is the scripted codex app-server's log of the requests it was sent.
const codexAppServerLog = "fakelog/codex-app-server.jsonl"

// withoutPostRunSession drops the codex app-server log once the JS log is orch's (the
// kickoff's routing refresh) followed by nothing but the start of the session the JS claude
// bridge's unawaited post-run refresh opens before it exits (PE-12; orch's is off).
func withoutPostRunSession(t *testing.T, js, orch outcome) (outcome, outcome) {
	t.Helper()
	rest, found := strings.CutPrefix(js.files[codexAppServerLog], orch.files[codexAppServerLog])
	if !found || orch.files[codexAppServerLog] == "" || rest != "" && !strings.HasPrefix(rest, `{"jsonrpc":"2.0","id":1,"method":"initialize",`) {
		t.Errorf("codex app-server log: js %q, orch %q; want orch's refresh, then at most the JS post-run session", js.files[codexAppServerLog], orch.files[codexAppServerLog])
	}
	delete(js.files, codexAppServerLog)
	delete(orch.files, codexAppServerLog)
	return js, orch
}

// kickoffLauncherLine is the `worker start --detach` answer a headless kickoff prints.
func kickoffLauncherLine(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "{") && strings.Contains(line, `"runner_pid":`) {
			return line
		}
	}
	t.Fatalf("no launcher line in the kickoff's output %q", stdout)
	return ""
}

// compareKickoffInteractive runs an interactive kickoff on door with the fake's TUI scripted
// by tui, requires the exit code and that the fake logged argv, and compares the pair.
func (h *harness) compareKickoffInteractive(t *testing.T, door string, tui seed, exit int, argvLog string, argv []string) {
	t.Helper()
	inv := h.kickoffInvocation(kickoffCase{args: append(append([]string{}, kickoffLaunchArgs...), "--door", door), seed: seeds(kickoffRoutedGreen(t), tui)})
	js, orch := h.runPair(t, inv)
	launching := "launching: " + door + " ... (watch in another terminal: " + cmdref.WatchFollow() + ")\n"
	wantArgv := strings.Join(argv, "\n") + "\n"
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != exit || !strings.HasSuffix(o.stdout, launching) || o.files[argvLog] != wantArgv {
			t.Errorf("%s: exit %d, stdout %q, argv %q; want exit %d after launching %s with %q", side, o.exitCode, o.stdout, o.files[argvLog], exit, door, wantArgv)
		}
	}
	compareOutcomes(t, js, orch)
}

// testKickoffSupervisorStarted runs a dry-run kickoff with no supervisor lock, so each
// runtime's `supervisor start` launches a serve in its own sandbox; each serve is stopped
// by the pid its kickoff printed (see runKickoffSupervisorStarted).
func (h *harness) testKickoffSupervisorStarted(t *testing.T) {
	args := append(append([]string{}, kickoffLaunchArgs...), "--dry-run")
	s := kickoffRoutedGreen(t)
	js := h.runKickoffSupervisorStarted(t, s, append([]string{h.node, filepath.Join(h.orchJS, "bin", "kickoff.mjs")}, args...))
	orch := h.runKickoffSupervisorStarted(t, s, append([]string{h.orchBin, "kickoff"}, args...))
	compareOutcomes(t, js, orch)
}

// runKickoffSupervisorStarted runs the kickoff argv in a fresh sandbox seeded by s, requires its
// supervisor line to report a started serve, then stops that serve through its stop file
// and waits for the pid read from the line, killing it and failing on timeout (stopLaunched).
// The pid, the cut log path and the serve's fresh values are normalised.
func (h *harness) runKickoffSupervisorStarted(t *testing.T, s seed, argv []string) outcome {
	t.Helper()
	since := time.Now()
	sb := h.newSandbox(t)
	s(t, sb)
	o := h.run(t, sb, argv, nil, []string{noPostRunRefresh})
	started := kickoffSupervisorStarted.FindStringSubmatch(o.stdout)
	if started == nil {
		t.Fatalf("%s: no started supervisor in %q (stderr %q)", argv[0], o.stdout, o.stderr)
	}
	pid, err := strconv.Atoi(started[2])
	if err != nil || pid < 2 {
		t.Fatalf("%s: supervisor pid %q", argv[0], started[2])
	}
	h.stopLaunched(t, sb, pid)
	o.stdout = kickoffSupervisorStarted.ReplaceAllString(o.stdout, "${1}<pid>${3}<log>")
	o.files = sb.files(t)
	if state := o.files["project/supervisor-state.json"]; !strings.Contains(state, `"pid": `+started[2]+",") {
		t.Errorf("%s: no tick of the started serve %d in the state %q", argv[0], pid, state)
	}
	collectExtraFiles(t, sb, invocation{extraFiles: []string{".git/info/exclude"}}, o.files)
	return o.withProjectPlaceholder(t, sb).normalized(func(s string) string {
		return normalizeFlip(since)(normalizeGeneratedAt(normalizeServe(since)(s)))
	})
}
