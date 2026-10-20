//go:build parity

package parity

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// workerDetachCase is one `worker start --detach` (args follow "start"): the launcher's exit,
// stdout and stderr, then, once its runner has settled, every file the runner wrote. The
// runner runs the claude bridge over the fake scripted by run. keepAwake makes the fake which
// find the platform's keep-awake wrapper, whose fake logs its argv and execs the runner. steps,
// when set, run while the runner runs (see runDetached).
type workerDetachCase struct {
	name      string
	args      []string
	run       bridgeCase
	keepAwake bool
	steps     detachSteps
	// runnerRefuses marks a runner that exits before writing a worker state.
	runnerRefuses bool
	// project, when set, is the project directory the launch names with a relative --cwd, under
	// the sandbox's working directory; its .orchestrator/ is collected as the project's.
	project string
	// launchLine extracts the launch JSON from a successful command's stdout; nil means the
	// stdout is the launch line itself, as `worker start --detach` prints it.
	launchLine func(t *testing.T, stdout string) string
}

// detachSteps runs commands in the sandbox while the runner runs, with worker naming the
// runtime's worker command (node worker.mjs or orch worker), and returns their output.
type detachSteps func(t *testing.T, sb sandbox, worker []string, l launch) string

// launch is what the launcher prints.
type launch struct {
	RunnerPid int    `json:"runner_pid"`
	LogPath   string `json:"log_path"`
	RunID     string `json:"run_id"`
}

// settleTimeout bounds the wait for a detached runner to be gone.
const settleTimeout = 30 * time.Second

// runnerPid matches the pid as the launcher prints it and as `flip run` quotes the launcher's
// line inside a JSON string (\"runner_pid\":123).
var runnerPid = regexp.MustCompile(`("runner_pid\\?":)\d+`)

// keepAwakeWrapper is the wrapper keep-awake looks for on this platform.
func keepAwakeWrapper() string {
	if runtime.GOOS == "darwin" {
		return "caffeinate"
	}
	return "systemd-inhibit"
}

// testWorkerDetach compares `worker start --detach` launches and their runners.
func (h *harness) testWorkerDetach(t *testing.T) {
	claude := []string{"--brief", "b.md", "--harness", "claude", "--detach"}
	ok := bridgeCase{fixture: "ok"}
	live := strconv.Itoa(os.Getpid()) // only ever sent signal 0
	dead := strconv.Itoa(reapedPid(t))
	lockedBy := func(holder string) bridgeCase {
		return bridgeCase{fixture: "ok", seed: seeds(
			seedProjectFile(".orchestrator/workers/b.lock", holder),
			seedWorkerState("b", `{"lane":"b","status":"running","pid":`+dead+`,"extra":1}`))}
	}
	cases := []workerDetachCase{
		{name: "finished", args: claude, run: ok},
		{name: "keep-awake", args: claude, run: ok, keepAwake: true},
		// The launch flip.mjs makes.
		{name: "conductor", run: ok, args: []string{"--brief", "b.md", "--lane", "conductor", "--harness", "claude", "--agent", "conductor",
			"--breadth", "broad", "--cwd", ".", "--detach", "--permission-mode", "auto"}},
		{name: "detach-repeated", run: ok, args: []string{"--detach", "--brief", "b.md", "--detach", "--harness", "claude", "--detach"}},
		// Every "--detach" token is dropped, also --lane's value: the launcher's lane is
		// "--detach", the runner's --lane takes "--harness" as its value and the runner refuses
		// the start without a harness, in its log.
		{name: "detach-as-a-value", run: ok, args: []string{"--brief", "b.md", "--lane", "--detach", "--harness", "claude", "--detach"}, runnerRefuses: true},
		// Sol S2/R6: the runner gets the absolute root as its last --cwd, so it works in sub/,
		// not in sub/sub/, where it would refuse the missing brief after a successful launch.
		{name: "relative-cwd", args: []string{"--brief", "b.md", "--harness", "claude", "--cwd", "sub", "--detach"}, project: "sub",
			run: bridgeCase{fixture: "ok", seed: seedProjectFile("sub/b.md", "brief body\n")}},
		{name: "relative-cwd-keep-awake", args: []string{"--brief", "b.md", "--harness", "claude", "--cwd", "sub", "--detach"}, project: "sub",
			run: bridgeCase{fixture: "ok", seed: seedProjectFile("sub/b.md", "brief body\n")}, keepAwake: true},
		{name: "locked/live-holder", args: claude, run: lockedBy(live)},
		{name: "locked/live-holder-no-state", args: claude, run: bridgeCase{seed: seedProjectFile(".orchestrator/workers/b.lock", live+"\n")}},
		{name: "locked/stale-holder", args: claude, run: lockedBy(dead)},
		{name: "locked/not-a-pid", args: claude, run: lockedBy("abc")},
		{name: "stop-running", args: claude, steps: h.stopRunningWorker, run: bridgeCase{script: map[string]string{
			"jsonl": `{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-0000000000aa","model":"m"}` + "\n",
			"sleep": "30\n", "pid": ""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			js, orch := h.runDetachedPair(t, c)
			switch {
			case strings.HasPrefix(c.name, "locked/live-holder"):
				requireLaneLocked(t, "js", js)
				requireLaneLocked(t, "orch", orch)
			case c.name == "stop-running":
				js = withInterruptedRun(t, "js", js, jsInterruptedState, jsInterruptedLog, jsInterruptedMail, []string{jsInterruptedIncident}, false)
				orch = withInterruptedRun(t, "orch", orch, orchInterruptedState, orchInterruptedLog, orchInterruptedMail,
					[]string{orchBridgeIncident, orchInterruptedIncident}, true)
			}
			compareOutcomes(t, js, orch)
		})
	}
}

// requireLaneLocked asserts what flip.mjs relies on: exit 3, nothing on stdout, "lane locked"
// on stderr (as JSON without a hint), and no runner and no log.
func requireLaneLocked(t *testing.T, side string, o outcome) {
	t.Helper()
	if o.exitCode != 3 || o.stdout != "" || !strings.Contains(o.stderr, "lane locked") ||
		strings.Contains(o.stderr, `"hint"`) || !strings.HasPrefix(o.stderr, `{"error":"lane locked","lane":"b","previous":`) {
		t.Errorf("%s: exit %d, stdout %q, stderr %q; want the lane-locked refusal flip.mjs expects", side, o.exitCode, o.stdout, o.stderr)
	}
	for name := range o.files {
		if strings.HasPrefix(name, "project/logs/") {
			t.Errorf("%s: a refused launch wrote %s", side, name)
		}
	}
}

// runDetachedPair runs the case in each runtime's own sandbox.
func (h *harness) runDetachedPair(t *testing.T, c workerDetachCase) (js, orch outcome) {
	t.Helper()
	inv := h.workerStartInvocation(t, workerStartCase{args: c.args, run: c.run})
	if c.keepAwake {
		inv.seed = seeds(inv.seed, seedFile("fake-which/"+keepAwakeWrapper(), ""))
	}
	jsWorker := []string{h.node, filepath.Join(h.orchJS, "bin", "worker.mjs")}
	orchWorker := []string{h.orchBin, "worker"}
	runners := strings.NewReplacer(h.runnerPrefixes(t)...)
	js = h.runDetached(t, inv, jsWorker, inv.js, c, runners)
	orch = h.runDetached(t, inv, orchWorker, inv.orch, c, runners)
	return js, orch
}

// runnerPrefixes are old/new pairs that rewrite the runner's command, as a wrapper's argv
// log holds it one argument per line, to "<runner>": the JS runs process.execPath and the
// resolved worker.mjs, orch runs itself as "worker".
func (h *harness) runnerPrefixes(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command(h.node, "-p", "process.execPath").Output()
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.EvalSymlinks(filepath.Join(h.orchJS, "bin", "worker.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	orch, err := filepath.EvalSymlinks(h.orchBin)
	if err != nil {
		t.Fatal(err)
	}
	return []string{
		strings.TrimSpace(string(out)) + "\n" + script + "\n", "<runner>\n",
		h.orchBin + "\nworker\n", "<runner>\n",
		orch + "\nworker\n", "<runner>\n",
	}
}

// runDetached runs one runtime's launcher (argv) in a fresh sandbox. When it launched a
// runner, steps run (their output is appended to stdout), the runner is settled and the files
// are collected again, with the invocation's extra files. The launch is checked against what
// the runner wrote, then its run id, log name and pid and the runner's command are rewritten
// before the case's normalizers.
func (h *harness) runDetached(t *testing.T, inv invocation, worker, argv []string, c workerDetachCase, runners *strings.Replacer) outcome {
	t.Helper()
	sb := h.newSandbox(t)
	inv.seed(t, sb)
	before := map[string]string{}
	collectFiles(t, filepath.Join(sb.project, ".orchestrator"), "project", before)
	o := h.run(t, sb, argv, nil, inv.env)
	var l launch
	if o.exitCode == 0 {
		line := o.stdout
		if c.launchLine != nil {
			line = c.launchLine(t, o.stdout)
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil || l.RunnerPid < 2 {
			if l.RunnerPid >= 2 { // a launch line that names its runner but is otherwise wrong
				settle(t, l.RunnerPid)
			}
			t.Fatalf("%s: launch %q (%v)", argv[0], o.stdout, err)
		}
		func() {
			defer settle(t, l.RunnerPid)
			if c.steps != nil {
				o.stdout += c.steps(t, sb, worker, l)
			}
		}()
		o.files = sb.files(t)
		if c.project != "" {
			collectFiles(t, filepath.Join(sb.project, c.project, ".orchestrator"), "project", o.files)
		}
		requireLaunchRecorded(t, argv[0], l, o.files, !c.runnerRefuses)
	}
	collectExtraFiles(t, sb, inv, o.files)
	o = o.withLaunch(l, runners)
	o = o.withProjectPlaceholder(t, sb)
	o = inv.normalizeWrites(o, before)
	return o.normalized(inv.normalize)
}

// settle waits until the runner is gone: its launcher has exited, so it was reparented to init,
// which reaps it. Past settleTimeout it kills the runner's process group and fails the case. A
// pid below 2 fails the case unsignalled: -1 or -0 would reach every process the user owns or
// the test's own group (S3/R13).
func settle(t *testing.T, pid int) {
	t.Helper()
	if pid < 2 {
		t.Errorf("refusing to settle runner pid %d", pid)
		return
	}
	deadline := time.Now().Add(settleTimeout)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			t.Errorf("runner %d still running after %v; its group was killed", pid, settleTimeout)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// requireLaunchRecorded asserts that the log was created 0600 and, with stateful, that the
// runner's state names the launch's run id and log.
func requireLaunchRecorded(t *testing.T, side string, l launch, files map[string]string, stateful bool) {
	t.Helper()
	recorded := !stateful
	for name, content := range files {
		if strings.HasPrefix(name, "project/workers/") && strings.HasSuffix(name, ".json") {
			recorded = recorded || strings.Contains(content, `"run_id": "`+l.RunID+`"`) && strings.Contains(content, `"log_path": "`+l.LogPath+`"`)
		}
	}
	if !recorded {
		t.Errorf("%s: no worker state records run_id %s and log_path %s", side, l.RunID, l.LogPath)
	}
	if info, err := os.Stat(l.LogPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("%s: log %s: mode %v (%v), want 0600", side, l.LogPath, info.Mode(), err)
	}
}

// withLaunch rewrites the launch's fresh values: its run id (also in the log's name), its
// runner's pid, and the runner's command in a wrapper's argv log.
func (o outcome) withLaunch(l launch, runners *strings.Replacer) outcome {
	replace := func(s string) string {
		s = runnerPid.ReplaceAllString(s, "${1}<pid>")
		if l.RunID != "" {
			s = strings.ReplaceAll(s, l.RunID, "<run-id>")
			s = strings.ReplaceAll(s, "-"+l.RunID[:8]+".log", "-<run>.log")
		}
		return runners.Replace(s)
	}
	files := map[string]string{}
	for name, content := range o.files {
		files[replace(name)] = replace(content)
	}
	return outcome{exitCode: o.exitCode, stdout: replace(o.stdout), stderr: replace(o.stderr), files: files}
}

// stopRunningWorker waits until the runner's bridge runs the sleeping fake claude, compares
// status, then stops the worker. The state's pid must be the runner the case launched, so no
// other process can be signalled. However it returns, the fake claude is reaped before the
// caller settles the runner, which waits for it.
func (h *harness) stopRunningWorker(t *testing.T, sb sandbox, worker []string, l launch) string {
	t.Helper()
	state := filepath.Join(sb.project, ".orchestrator", "workers", "b.json")
	claudePid := filepath.Join(sb.home, "fake-claude", "pid")
	defer reapFakeClaude(t, claudePid)
	deadline := time.Now().Add(settleTimeout)
	for {
		data, _ := os.ReadFile(state)
		_, err := os.Stat(claudePid)
		s := string(data)
		if err == nil && strings.Contains(s, `"status": "running"`) && strings.Contains(s, `"worker_pid":`) &&
			strings.Contains(s, `"session_id": "00000000-0000-4000-8000-0000000000aa"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the worker never ran the fake claude; state:\n%s", worker[0], s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	env := []string{noPostRunRefresh}
	status := h.run(t, sb, append(worker, "status", "--lane", "b"), nil, env)
	if !strings.Contains(status.stdout, `"process_alive": true`) || !strings.Contains(status.stdout, `"lock_present": true`) {
		t.Errorf("%s: status of the running worker:\n%s", worker[0], status.stdout)
	}
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"pid": `+strconv.Itoa(l.RunnerPid)+",") {
		t.Fatalf("%s: the state's pid is not the runner %d; not stopping:\n%s", worker[0], l.RunnerPid, data)
	}
	stop := h.run(t, sb, append(worker, "stop", "--lane", "b"), nil, env)
	return "--- status\n" + status.stdout + status.stderr + "--- stop\n" + stop.stdout + stop.stderr
}

// reapFakeClaude ends the fake claude whose pid file is given (it would otherwise sleep for
// 30 s). On a case that has failed it is killed at once, and a missing pid file means it never
// started; otherwise it must have been signalled away by the stop, else it is killed and the
// case fails.
func reapFakeClaude(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if errors.Is(err, fs.ErrNotExist) && t.Failed() {
		return
	}
	if err != nil {
		t.Errorf("fake claude pid: %v", err)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 2 {
		t.Errorf("fake claude pid %q", data)
		return
	}
	if t.Failed() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("the fake claude %d was not signalled", pid)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PE-11 (extended) on a worker stopped while its bridge runs: the JS bridge dies at once and
// leaves no result; the Go bridge forwards the signal and reports the CLI's end.
const (
	interruptedSession = `"session_id":"00000000-0000-4000-8000-0000000000aa"`
	jsInterruptedState = `"exit_code": null,
  "errors": [
    "bridge produced no result"
  ],
  "record": null,`
	orchInterruptedState = `"exit_code": 1,
  "errors": [
    "claude exited null: "
  ],
  "record": "$PROJECT/.orchestrator/runs/<stamp>-claude-00000000.json",`
	jsInterruptedLog   = `{"lane":"b","status":"failed",` + interruptedSession + `,"exit_code":null}` + "\n"
	orchInterruptedLog = "[claude] done ok=false turns=null in <n>s; record $PROJECT/.orchestrator/runs/<stamp>-claude-00000000.json\n" +
		`{"lane":"b","status":"failed",` + interruptedSession + `,"exit_code":1}` + "\n"
	jsInterruptedMail       = `"body":"no result","refs":[]`
	orchInterruptedMail     = `"body":"claude exited null: ","refs":[".orchestrator/runs/<stamp>-claude-00000000.json"]`
	jsInterruptedIncident   = `"component":"worker","kind":"crash","summary":"worker lane b failed on claude: no result","detail":"","context":{"harness":"claude","model":null,"exit_code":null}`
	orchBridgeIncident      = `"component":"bridge","kind":"crash","summary":"claude bridge failed: claude exited null: "`
	orchInterruptedIncident = `"component":"worker","kind":"crash","summary":"worker lane b failed on claude: claude exited null: "`
	interruptedRecord       = "project/runs/<stamp>-claude-00000000.json"
)

// withInterruptedRun asserts the documented PE-11 differences of a stopped worker in one
// runtime's outcome and replaces each with "<PE-11>", so that everything else is compared.
// Both runtimes end the run "failed": the "interrupted" status the signal handler
// publishes is overwritten when the bridge closes.
func withInterruptedRun(t *testing.T, side string, o outcome, state, log, mail string, incidents []string, record bool) outcome {
	t.Helper()
	cut := func(name, old string) {
		content, ok := o.files[name]
		if !ok || !strings.Contains(content, old) {
			t.Errorf("%s: %s does not hold %q (PE-11):\n%s", side, name, old, content)
			return
		}
		o.files[name] = strings.Replace(content, old, "<PE-11>", 1)
	}
	cut("project/workers/b.json", state)
	cut("project/logs/b-<run>.log", log)
	cut("project/mailbox/messages.jsonl", mail)
	for _, incident := range incidents {
		if !strings.Contains(o.files["state/incidents.jsonl"], incident) {
			t.Errorf("%s: no incident %q (PE-11):\n%s", side, incident, o.files["state/incidents.jsonl"])
		}
	}
	if lines := strings.Count(o.files["state/incidents.jsonl"], "\n"); lines != len(incidents) {
		t.Errorf("%s: %d incidents, want %d", side, lines, len(incidents))
	}
	delete(o.files, "state/incidents.jsonl")
	if _, ok := o.files[interruptedRecord]; ok != record {
		t.Errorf("%s: run record written %v, want %v (PE-11)", side, ok, record)
	}
	delete(o.files, interruptedRecord)
	if o.exitCode != 0 || !strings.Contains(o.files["project/workers/b.json"], `"status": "failed"`) {
		t.Errorf("%s: launcher exit %d, state:\n%s", side, o.exitCode, o.files["project/workers/b.json"])
	}
	return o
}
