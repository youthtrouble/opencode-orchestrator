//go:build parity

package parity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
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
		h.compareKickoffHeadless(t, claudeBridge, bridgeCase{fixture: "ok"}, nil, `{"agent":"conductor","permission-mode":"auto"}`, "--door", "claude")
	})
	t.Run("headless/opencode", func(t *testing.T) {
		h.compareKickoffHeadless(t, opencodeBridge, bridgeCase{script: opencodeRunOK}, nil, `{"agent":"conductor"}`)
	})
	t.Run("clears-gate-without-relaunch", h.testKickoffClearsGateWithoutRelaunch)
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
	t.Run("both-red/supervisor-started", h.testKickoffBothRedStartsTheSupervisor)
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

// testKickoffClearsGateWithoutRelaunch asserts that kickoff clears a human gate without
// queueing the supervisor relaunch a plain `flip gate --clear` would: kickoff launches the
// conductor itself, so a pending launch would start a second one.
func (h *harness) testKickoffClearsGateWithoutRelaunch(t *testing.T) {
	gate := seedFlip(`{"awaiting_human":{"at":"2026-01-01T00:00:00.000Z","door":"opencode","reason":"push","question":null}}`)
	js, orch := h.compareKickoffHeadless(t, opencodeBridge, bridgeCase{script: opencodeRunOK}, gate, `{"agent":"conductor"}`)
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if flipJSON := o.files["project/flip.json"]; flipJSON != "{}\n" {
			t.Errorf("%s: flip.json = %q, want the gate cleared and no pending launch", side, flipJSON)
		}
	}
}

// compareKickoffHeadless runs a headless kickoff on the bridge's door in each runtime's own
// sandbox, with the project state seeded by state (nil for none), settles the conductor
// lane's runner by the pid each printed, and compares the kickoff's output with everything
// the runner wrote (lane state, log, mailbox, run record). It returns both outcomes. The
// routing refresh's feeds fail over the bridge's green cache (see withoutPostRunSession).
// launchOptions is the PE-62 launch_options the conductor lane's state records.
func (h *harness) compareKickoffHeadless(t *testing.T, b bridge, run bridgeCase, state seed, launchOptions string, door ...string) (js, orch outcome) {
	t.Helper()
	inv := h.workerStartInvocation(t, workerStartCase{bridge: b, run: run, brief: text("")})
	args := append(append(append([]string{}, kickoffLaunchArgs...), "--headless"), door...)
	inv.js = append([]string{h.node, filepath.Join(h.orchJS, "bin", "kickoff.mjs")}, args...)
	inv.orch = append([]string{h.orchBin, "kickoff"}, args...)
	inv.seed = seeds(inv.seed, supervisorHeldByThisTest(), kickoffBrief(), kickoffFeedsFail(), orNothing(state))
	inv.extraFiles = []string{".git/info/exclude"}
	normalizeWrites := inv.normalizeWrites
	inv.normalizeWrites = func(o outcome, before map[string]string) outcome {
		return normalizeWrites(flipTransactionSidecar(o, before), before)
	}
	worker, fresh := inv.normalize, normalizeFlip(time.Now())
	inv.normalize = func(s string) string { return withSupervisorHolder(fresh(normalizeGeneratedAt(worker(s)))) }
	launched := workerDetachCase{launchLine: kickoffLauncherLine}
	runners := strings.NewReplacer(h.runnerPrefixes(t)...)
	js = h.runDetached(t, inv, nil, inv.js, launched, runners)
	orch = h.runDetached(t, inv, nil, inv.orch, launched, runners)
	js, orch = withoutPostRunSession(t, js, orch)
	brief := cmdref.KickoffPrompt("a.md", nil) + "\n"
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 0 || o.files["project/briefs/conductor-kickoff.md"] != brief || o.files["fakelog/"+b.name+"-argv.txt"] == "" {
			t.Errorf("%s: exit %d, stdout %q; want a launched conductor lane that ran the fake %s", side, o.exitCode, o.stdout, b.name)
		}
	}
	withPE62(t, &js, &orch, launchOptions)
	compareOutcomes(t, js, orch)
	return js, orch
}

// codexAppServerLog is the scripted codex app-server's log of the requests it was sent.
const codexAppServerLog = "fakelog/codex-app-server.jsonl"

// codexSessionStart is the first request of every codex app-server session, as the fake logs it.
const codexSessionStart = `{"jsonrpc":"2.0","id":1,"method":"initialize",`

// withoutPostRunSession drops the codex app-server log once the JS log is orch's (the
// kickoff's routing refresh, one session) followed by nothing but the start of one more such
// session: the one the JS claude bridge's unawaited post-run refresh opens before it exits
// (PE-12; orch's is off).
func withoutPostRunSession(t *testing.T, js, orch outcome) (outcome, outcome) {
	t.Helper()
	session := orch.files[codexAppServerLog]
	rest, found := strings.CutPrefix(js.files[codexAppServerLog], session)
	if !found || !isOneSession(session) || !isSessionStart(rest, session) {
		t.Errorf("codex app-server log: js %q, orch %q; want orch's refresh, then at most the JS post-run session", js.files[codexAppServerLog], orch.files[codexAppServerLog])
	}
	delete(js.files, codexAppServerLog)
	delete(orch.files, codexAppServerLog)
	return js, orch
}

// isOneSession reports whether log holds exactly one app-server session.
func isOneSession(log string) bool {
	return strings.HasPrefix(log, codexSessionStart) && strings.Count(log, `"method":"initialize"`) == 1
}

// isSessionStart reports whether rest is empty or the first whole lines of session, a log of
// one session: a second session with the same requests, cut wherever its process exited.
func isSessionStart(rest, session string) bool {
	return rest == "" || strings.HasPrefix(rest, codexSessionStart) && strings.HasSuffix(rest, "\n") && strings.HasPrefix(session, rest)
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

// testKickoffSupervisorStarted runs a kickoff with no supervisor lock, so each runtime's
// `supervisor start` launches a serve in its own sandbox; each serve is stopped by the pid its
// kickoff printed (see runKickoffSupervisorStarted). A dry run starts nothing, so the kickoff
// names an unknown door: it runs the whole preflight, then dies of the door before any launch.
func (h *harness) testKickoffSupervisorStarted(t *testing.T) {
	args := append(append([]string{}, kickoffLaunchArgs...), "--door", "gemini")
	s := kickoffRoutedGreen(t)
	js := h.runKickoffSupervisorStarted(t, s, false, append([]string{h.node, filepath.Join(h.orchJS, "bin", "kickoff.mjs")}, args...))
	orch := h.runKickoffSupervisorStarted(t, s, false, append([]string{h.orchBin, "kickoff"}, args...))
	if err := crashError(crashC2, "", js, orch); err != nil {
		t.Error(err)
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

// testKickoffBothRedStartsTheSupervisor runs a real both-red kickoff in a fresh project with
// no supervisor lock (Sol R10): each runtime starts a serve to relaunch at the reset, saves
// the prompt as the conductor's brief, pauses naming it and exits 75. Each serve is stopped
// as in testKickoffSupervisorStarted, but only once it has evaluated the pause.
//
// Kickoff starts the serve before `flip pause` writes flip.json, and a serve ticks at once, so
// whether its first tick sees flip.json (and stamps last_flip_check) is a process-start race,
// the same in both runtimes. Waiting for the stamp makes its presence deterministic, and
// requireFlipEvaluated then asserts what exit 75 promises: the serve evaluates the pause.
// Only the stamp's position still shows the race; withFlipCheckAfterLanes settles it.
func (h *harness) testKickoffBothRedStartsTheSupervisor(t *testing.T) {
	s := seeds(kickoffBrief(), newFlipCaches(t).bothRed, kickoffFeedsFail())
	js := h.runKickoffSupervisorStarted(t, s, true, append([]string{h.node, filepath.Join(h.orchJS, "bin", "kickoff.mjs")}, kickoffLaunchArgs...))
	orch := h.runKickoffSupervisorStarted(t, s, true, append([]string{h.orchBin, "kickoff"}, kickoffLaunchArgs...))
	paused := `"brief": ".orchestrator/briefs/conductor-kickoff.md"`
	for side, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 75 || o.files["project/briefs/conductor-kickoff.md"] != cmdref.KickoffPrompt("a.md", nil)+"\n" || !strings.Contains(o.files["project/flip.json"], paused) {
			t.Errorf("%s: exit %d, stdout %q, flip.json %q; want 75, the kickoff brief and a pause naming it", side, o.exitCode, o.stdout, o.files["project/flip.json"])
		}
		requireFlipEvaluated(t, side, o)
	}
	js, orch = withFlipCheckAfterLanes(t, "js", js), withFlipCheckAfterLanes(t, "orch", orch)
	compareOutcomes(t, js, orch)
}

// The supervisor state lines that place last_flip_check. Both runtimes keep the state's keys
// in insertion order, so the stamp's position names the tick that wrote it:
//   - the first tick, seeing flip.json, stamps it before its own keys:
//     events, lanes, last_flip_check, checked_at, pid, status, stopped_at
//   - a later tick appends it after the first tick's keys (status is rewritten in place on stop):
//     events, lanes, checked_at, pid, status, last_flip_check, stopped_at
const (
	flipCheckLine    = `  "last_flip_check": <now+0>,` + "\n"
	firstTickBefore  = `  "lanes": {},` + "\n"
	secondTickBefore = `  "status": "stopped",` + "\n"
)

// withFlipCheckAfterLanes moves the state's last_flip_check line from where a second tick
// writes it to where a first tick does, so which tick won the race does not show in the
// compare. Every line is kept, and nothing else moves. A state in neither shape is reported
// and left as it is, so the compare shows it too.
func withFlipCheckAfterLanes(t *testing.T, side string, o outcome) outcome {
	t.Helper()
	const file = "project/supervisor-state.json"
	state := o.files[file]
	firstTick, secondTick := firstTickBefore+flipCheckLine, secondTickBefore+flipCheckLine
	switch {
	case strings.Count(state, flipCheckLine) != 1:
		t.Errorf("%s: want exactly one last_flip_check line in the state %q", side, state)
	case strings.Contains(state, firstTick):
	case strings.Contains(state, secondTick) && strings.Count(state, firstTickBefore) == 1:
		state = strings.Replace(state, secondTick, secondTickBefore, 1)
		o.files[file] = strings.Replace(state, firstTickBefore, firstTick, 1)
	default:
		t.Errorf("%s: last_flip_check is where neither a first nor a later tick writes it: %q", side, state)
	}
	return o
}

// flipCheckWait bounds the wait for a serve to stamp last_flip_check: a serve whose first
// tick missed flip.json stamps it on its second, one 5s serve interval later, so two
// intervals leave room for a slow runner.
const flipCheckWait = 12 * time.Second

// runKickoffSupervisorStarted runs the kickoff argv in a fresh sandbox seeded by s, requires its
// supervisor line to report a started serve, then stops that serve through its stop file
// and waits for the pid read from the line, killing it and failing on timeout (stopLaunched).
// With awaitFlipCheck, the stop waits first for the serve to evaluate the pause the kickoff
// recorded after starting it (see testKickoffBothRedStartsTheSupervisor); a kickoff that
// records no pause must not wait, as its serve never stamps last_flip_check.
// The pid, the cut log path and the serve's fresh values are normalised.
func (h *harness) runKickoffSupervisorStarted(t *testing.T, s seed, awaitFlipCheck bool, argv []string) outcome {
	t.Helper()
	since := time.Now()
	sb := h.newSandbox(t)
	s(t, sb)
	before := map[string]string{}
	collectFiles(t, filepath.Join(sb.project, ".orchestrator"), "project", before)
	o := h.run(t, sb, argv, nil, []string{noPostRunRefresh})
	pid := startedServePid(o.stdout)
	if pid < 2 {
		stopUnreadServe(t, sb, argv[0] == h.node)
		t.Fatalf("%s: no started supervisor with a pid in %q (stderr %q)", argv[0], o.stdout, o.stderr)
	}
	if awaitFlipCheck {
		waitForFlipCheck(t, sb, argv[0])
	}
	h.stopLaunched(t, sb, pid)
	o.stdout = kickoffSupervisorStarted.ReplaceAllString(o.stdout, "${1}<pid>${3}<log>")
	o.files = sb.files(t)
	if state := o.files["project/supervisor-state.json"]; !strings.Contains(state, `"pid": `+strconv.Itoa(pid)+",") {
		t.Errorf("%s: no tick of the started serve %d in the state %q", argv[0], pid, state)
	}
	collectExtraFiles(t, sb, invocation{extraFiles: []string{".git/info/exclude"}}, o.files)
	o = flipTransactionSidecar(o, before)
	return o.withProjectPlaceholder(t, sb).normalized(func(s string) string {
		return normalizeFlip(since)(normalizeGeneratedAt(normalizeServe(since)(s)))
	})
}

// waitForFlipCheck polls sb's supervisor state until a tick has stamped last_flip_check, and
// fails naming side and the state seen after flipCheckWait. It never stops the serve: the
// caller still does, whether or not the stamp came.
func waitForFlipCheck(t *testing.T, sb sandbox, side string) {
	t.Helper()
	path := filepath.Join(sb.project, ".orchestrator", "supervisor-state.json")
	deadline := time.Now().Add(flipCheckWait)
	for {
		state, _ := os.ReadFile(path) // absent until the first tick; a timeout reports it
		if strings.Contains(string(state), `"last_flip_check":`) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s: no serve tick stamped last_flip_check within %s; state %q", side, flipCheckWait, state)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stopUnreadServe stops a serve the kickoff may have started in sb when its pid could not be
// read from the output, and returns only once that serve has exited, so the sandbox (and its
// stop file) outlives it. A serve starting after the stop file exists stops at its first tick.
// A running one is found by unreadServePid, waited for and killed after serveExitWait.
// legacyPidText is set for the frozen JS oracle, which takes no kernel lock.
func stopUnreadServe(t *testing.T, sb sandbox, legacyPidText bool) {
	t.Helper()
	dir := filepath.Join(sb.project, ".orchestrator")
	if err := os.WriteFile(filepath.Join(dir, "supervisor.stop"), nil, 0o644); err != nil {
		t.Error(err)
	}
	pid, err := unreadServePid(filepath.Join(dir, "supervisor.lock"), legacyPidText)
	if err != nil {
		t.Error(err)
		return
	}
	if pid < 2 {
		return
	}
	deadline := time.Now().Add(serveExitWait)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("serve %d did not stop; killed", pid)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// unreadServePid polls, for at most serveLockWait, for the pid of the serve holding lock: the
// kernel's holder (ownlock.Probe) for orch, the lock's text for the frozen JS, whose serve
// writes its pid there and holds no record lock. The text is read only here, in the harness,
// which holds no lock on that inode (contract C2). 0 when none appears: no serve started, or
// one already stopped and released its lock.
func unreadServePid(lock string, legacyPidText bool) (int, error) {
	deadline := time.Now().Add(serveLockWait)
	for {
		pid, err := servePid(lock, legacyPidText)
		if err != nil || pid >= 2 || time.Now().After(deadline) {
			return pid, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func servePid(lock string, legacyPidText bool) (int, error) {
	if !legacyPidText {
		held, pid, err := ownlock.Probe(lock)
		if !held {
			pid = 0
		}
		return pid, err
	}
	text, err := os.ReadFile(lock)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(text))) // "" while the JS is writing it
	return pid, nil
}

// startedServePid is the pid of the serve a kickoff's supervisor line reports started, 0 when
// the line is missing or malformed.
func startedServePid(stdout string) int {
	started := kickoffSupervisorStarted.FindStringSubmatch(stdout)
	if started == nil {
		return 0
	}
	pid, _ := strconv.Atoi(started[2])
	return pid
}

// testKickoffUnreadServeCleanup is the failure path of runKickoffSupervisorStarted: with a
// supervisor line missing or malformed, each runtime's serve (launched by its own `supervisor
// start`, as kickoff's preflight does) must have exited before the case ends and its sandbox
// goes, whether it holds a kernel lock (orch) or only writes its pid (the frozen JS).
func (h *harness) testKickoffUnreadServeCleanup(t *testing.T) {
	for _, side := range []struct {
		name   string
		argv   []string
		legacy bool
	}{
		{"js", []string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, true},
		{"orch", []string{h.orchBin, "supervisor"}, false},
	} {
		for name, stdout := range map[string]string{"missing": "", "malformed": "supervisor: {\"started\":true,\"supervisor_pid\":\"x\"}\n"} {
			t.Run(side.name+"/"+name, func(t *testing.T) {
				t.Parallel()
				if pid := startedServePid(stdout); pid >= 2 {
					t.Fatalf("pid %d read from %q", pid, stdout)
				}
				sb := h.newSandbox(t)
				o := h.run(t, sb, append(side.argv, "start", "--cwd", "."), nil, []string{noPostRunRefresh})
				var launched struct {
					Pid int `json:"supervisor_pid"`
				}
				if err := json.Unmarshal([]byte(o.stdout), &launched); err != nil || launched.Pid < 2 {
					t.Fatalf("start output %q: %v", o.stdout, err)
				}
				// The cleanup must find the running serve itself: the JS one by its text, orch's
				// through the kernel (a JS serve found by Probe would read as none).
				lock := filepath.Join(sb.project, ".orchestrator", "supervisor.lock")
				if pid, err := unreadServePid(lock, side.legacy); err != nil || pid != launched.Pid {
					t.Errorf("serve pid %d, %v; want the launched %d", pid, err, launched.Pid)
				}
				stopUnreadServe(t, sb, side.legacy)
				if err := syscall.Kill(launched.Pid, 0); !errors.Is(err, syscall.ESRCH) {
					t.Errorf("serve %d still running after the cleanup (%v)", launched.Pid, err)
				}
				state, _ := os.ReadFile(filepath.Join(sb.project, ".orchestrator", "supervisor-state.json"))
				if !strings.Contains(string(state), `"status": "stopped"`) {
					t.Errorf("serve did not stop through its stop file: state %q", state)
				}
			})
		}
	}
}
