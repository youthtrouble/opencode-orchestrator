//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// workerStartCase is one foreground `worker start`. The worker runs its runtime's bridge (the
// JS worker node <harness>-exec.mjs, orch its own <harness>-exec), which runs the bridge's
// fake as scripted by the embedded bridgeCase (fixture, script, usage, codex); its args are
// not used. The brief is b.md in the project unless brief is set ("" content for no brief).
// bridgeCrash marks a case whose bridge dies of an uncaught exception: only then is the crash
// block Node prints, which the worker relays to its stdout, reduced to its error line.
type workerStartCase struct {
	name        string
	args        []string // after "start"
	bridge      bridge   // the fake to script; claudeBridge when zero
	run         bridgeCase
	brief       *string
	crash       crashClass
	errorLine   string
	bridgeCrash bool
}

// Fresh worker values, rewritten only in their exact place and format.
var (
	workerPids    = regexp.MustCompile(`("(?:pid|worker_pid)": ?)\d+`)
	workerStamps  = regexp.MustCompile(`("(?:updated_at|last_progress_at)": ?")` + isoMillis.String() + `(")`)
	workerRunID   = regexp.MustCompile(`("run_id": ?")` + freshUUID.String() + `(")`)
	workerMailbox = regexp.MustCompile(`(?m)^(\{"id":")` + freshUUID.String() + `(","at":")` + isoMillis.String() + `(")`)
	// A run record's prompt_sha256 hashes a prompt that names the sandbox project; the prompt
	// itself is compared byte for byte in the fake's argv or stdin log.
	workerPromptHash = regexp.MustCompile(`("prompt_sha256": ?")[0-9a-f]{64}(")`)
	// The blocked message's ref is the run record relative to the project.
	workerRecordRef = regexp.MustCompile(`(\["\.orchestrator/runs/)\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z(-(?:claude|codex|opencode)-)`)
	// The failure incident's detail quotes the bridge's result line.
	workerEscapedStart    = regexp.MustCompile(`(\\"started_at\\":\\")` + isoMillis.String() + `(\\")`)
	workerEscapedDuration = regexp.MustCompile(`(\\"duration_ms\\":)\d+(,)`)
	// Node's crash block: the throwing location, its source line and caret line, a blank line,
	// the error line, the stack, a blank line and the Node version.
	nodeCrashBlock = regexp.MustCompile(`(?m)^file://[^\n]*\n[^\n]*\n *\^+\n\n([A-Za-z]*Error(?: \[[A-Z_]+\])?: [^\n]*)\n(?:    at [^\n]*\n)*\nNode\.js v\d+\.\d+\.\d+\n`)
)

// normalizeWorker rewrites the worker's fresh values and the bridge's.
func normalizeWorker(b bridge) func(string) string {
	return func(s string) string {
		s = workerPids.ReplaceAllString(s, "${1}<pid>")
		s = workerStamps.ReplaceAllString(s, "${1}<iso>${2}")
		s = workerRunID.ReplaceAllString(s, "${1}<uuid>${2}")
		s = workerMailbox.ReplaceAllString(s, "${1}<uuid>${2}<iso>${3}")
		s = workerPromptHash.ReplaceAllString(s, "${1}<sha256>${2}")
		s = workerRecordRef.ReplaceAllString(s, "${1}<stamp>${2}")
		s = workerEscapedStart.ReplaceAllString(s, "${1}<iso>${2}")
		s = workerEscapedDuration.ReplaceAllString(s, "${1}<ms>${2}")
		return b.normalize(s)
	}
}

// withoutNodeCrashBlock reduces a crash Node printed (source excerpt, error line, stack, Node
// version), which a JS bridge's stderr carries into the worker's stdout, to its error line,
// the line orch's bridge prints alone (plan go-s6.md section 7.2).
func withoutNodeCrashBlock(s string) string {
	return nodeCrashBlock.ReplaceAllString(s, "${1}\n")
}

func (h *harness) workerStartInvocation(t *testing.T, c workerStartCase) invocation {
	t.Helper()
	b := c.bridge
	if b.name == "" {
		b = claudeBridge
	}
	inv := h.bridgeInvocation(t, b, c.run)
	args := append([]string{"start"}, c.args...)
	inv.js = append([]string{h.node, filepath.Join(h.orchJS, "bin", "worker.mjs")}, args...)
	inv.orch = append([]string{h.orchBin, "worker"}, args...)
	inv.stdin = nil
	brief := "brief body\n"
	if c.brief != nil {
		brief = *c.brief
	}
	if brief != "" {
		inv.seed = seeds(inv.seed, seedProjectFile("b.md", brief))
	}
	normalize := normalizeWorker(b)
	if c.bridgeCrash {
		normalize = func(s string) string { return normalizeWorker(b)(withoutNodeCrashBlock(s)) }
	}
	inv.normalize = normalize
	if c.run.normalize != nil {
		inv.normalize = func(s string) string { return c.run.normalize(normalize(s)) }
	}
	inv.crash, inv.errorLine, inv.progress = c.crash, c.errorLine, ""
	runFileNames := inv.normalizeWrites
	inv.normalizeWrites = func(o outcome, before map[string]string) outcome {
		return withWorkerLockPid(runFileNames(o, before), before)
	}
	return inv
}

// withWorkerLockPid rewrites the pid in a lock the worker wrote (a seeded lock is compared as
// it is).
func withWorkerLockPid(o outcome, before map[string]string) outcome {
	for name, content := range o.files {
		if seeded, ok := before[name]; strings.HasSuffix(name, ".lock") && (!ok || seeded != content) {
			if _, err := strconv.Atoi(content); err == nil {
				o.files[name] = "<pid>"
			}
		}
	}
	return o
}

func text(s string) *string { return &s }

// testWorkerStart compares foreground `worker start` runs over the bridges' fakes.
func (h *harness) testWorkerStart(t *testing.T) {
	brief := []string{"--brief", "b.md"}
	claude := append(brief, "--harness", "claude")
	init := `{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-0000000000aa","model":"m"}` + "\n"
	result := func(fields string) map[string]string {
		return map[string]string{"jsonl": init + `{"type":"result","subtype":"success","is_error":false,"num_turns":1,` + fields + `}` + "\n", "exit": "0\n"}
	}
	codexThread := `{"type":"thread.started","thread_id":"0199a1b2-c3d4-7e5f-8a9b-0123456789ab"}` + "\n"
	codexOK := map[string]string{"jsonl": codexThread + `{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"ls","exit_code":0}}` + "\n" +
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"pong"}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}` + "\n", "exit": "0\n"}
	opencodeOK := map[string]string{"jsonl": `{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"pong"}}` + "\n" + `{"part":{"type":"tool","tool":"bash"}}` + "\n", "exit": "0\n"}
	const (
		briefBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[1]" argument must be of type string. Received type boolean (true)`
		cwdBoolean   = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	)
	live := strconv.Itoa(os.Getpid())
	dead := strconv.Itoa(reapedPid(t))
	locked := func(state string) bridgeCase {
		s := seedWorkerLock("b")
		if state != "" {
			s = seeds(s, seedWorkerState("b", state))
		}
		return bridgeCase{seed: s}
	}

	cases := []workerStartCase{
		// claude
		{name: "claude/finished", args: claude, run: bridgeCase{fixture: "ok"}},
		{name: "claude/conductor-lane", args: append(claude, "--lane", "conductor"), run: bridgeCase{fixture: "ok"}},
		{name: "claude/permission-denials", args: claude, run: bridgeCase{fixture: "permission-denied"}},
		{name: "claude/failed-bad-model", args: claude, run: bridgeCase{fixture: "bad-model"}},
		{name: "claude/failed-exit-with-stderr", args: claude, run: bridgeCase{script: map[string]string{"stderr": "boom\nfatal: no auth\n", "exit": "1\n"}}},
		{name: "claude/failed-rate-limit", args: claude, run: bridgeCase{script: map[string]string{"jsonl": `{"type":"result","is_error":true,"subtype":"rate_limited"}` + "\n", "exit": "1\n"}}},
		{name: "claude/deferred-red-cache", args: claude, run: bridgeCase{usage: "red"}},
		{name: "claude/deferred-usage-limit", args: claude, run: bridgeCase{fixture: "usage-limit", codex: true}},
		{name: "claude/every-flag", run: bridgeCase{fixture: "ok"}, args: append(claude,
			"--model", "claude-haiku-5-5", "--effort", "high", "--resume", "abc", "--force", "--allowed-tools", "Read,Edit", "--max-turns", "3",
			"--timeout-min", "5", "--permission-mode", "plan", "--agent", "reviewer", "--breadth", "broad", "--run-id", "run-1",
			"--log-path", "/tmp/w.log", "--sandbox", "read-only", "--lane", "w1")},
		{name: "claude/bare-model", args: append(claude, "--model"), run: bridgeCase{fixture: "ok"}},
		{name: "claude/bare-resume", args: append(claude, "--resume"), run: bridgeCase{fixture: "usage-limit", codex: true}},
		{name: "claude/bare-lane-failed", args: append(claude, "--lane"), run: bridgeCase{fixture: "bad-model"}},
		{name: "claude/long-final-message", args: claude, run: bridgeCase{script: result(`"result":"` + strings.Repeat("é", 2100) + `"`)}},
		{name: "claude/empty-lane-cannot-post", args: []string{"--brief", ".md", "--harness", "claude"}, run: bridgeCase{fixture: "bad-model", seed: seedProjectFile(".md", "brief\n")},
			crash: crashC1, errorLine: "Error: post needs from and subject"},
		{name: "claude/array-final-message", args: claude, run: bridgeCase{script: result(`"result":["a",1,null]`)}},
		{name: "claude/numeric-final-message", args: claude, run: bridgeCase{script: result(`"result":7`)}, crash: crashC2},
		{name: "claude/bridge-crash-null-event", args: claude, run: bridgeCase{script: map[string]string{"jsonl": init + "null\n", "exit": "0\n"}}, bridgeCrash: true},

		// codex
		{name: "codex/finished", args: append(brief, "--harness", "codex", "--agent", "x"), bridge: codexBridge, run: bridgeCase{script: codexOK}},
		{name: "codex/sandbox-read-only", args: append(brief, "--harness", "codex", "--sandbox", "read-only"), bridge: codexBridge, run: bridgeCase{script: codexOK}},

		// opencode
		{name: "opencode/finished-with-agent", args: append(brief, "--harness", "opencode", "--agent", "coder"), bridge: opencodeBridge, run: bridgeCase{script: opencodeOK}},
		{name: "opencode/effort-becomes-the-prompt", args: append(brief, "--harness", "opencode", "--effort", "X"), bridge: opencodeBridge, run: bridgeCase{script: opencodeOK}},

		// validation
		{name: "usage/no-brief", args: []string{"--harness", "claude"}},
		{name: "usage/no-harness", args: brief},
		{name: "usage/missing-brief", args: []string{"--brief", "absent.md", "--harness", "claude"}},
		{name: "usage/brief-not-md", args: []string{"--brief", "b.txt", "--harness", "claude"}, run: bridgeCase{seed: seedProjectFile("b.txt", "x")}},
		{name: "usage/harness-gemini", args: append(brief, "--harness", "gemini")},
		{name: "usage/harness-bare", args: append(brief, "--harness")},
		{name: "usage/brief-bare", args: []string{"--harness", "claude", "--brief"}, crash: crashC1, errorLine: briefBoolean},
		{name: "usage/cwd-bare", args: append(claude, "--cwd"), crash: crashC1, errorLine: cwdBoolean},
		{name: "crash/brief-is-a-directory", args: []string{"--brief", "d.md/", "--harness", "claude"}, brief: text(""), run: bridgeCase{seed: seedProjectDir("d.md")},
			crash: crashC1, errorLine: "Error: EISDIR: illegal operation on a directory, read"},

		// lane locked
		{name: "locked/live-holder", args: claude, run: locked(`{"lane":"b","status":"running","pid":` + live + `}`)},
		{name: "locked/dead-holder", args: claude, run: locked(`{"lane":"b","status":"running","pid":` + dead + `}`)},
		{name: "locked/no-state", args: claude, run: locked("")},
		{name: "locked/state-null", args: claude, run: locked(`null`)},
		{name: "locked/state-zero", args: claude, run: locked(`0`)},
		{name: "locked/state-string", args: claude, run: locked(`"abc"`)},
		{name: "locked/state-without-status", args: claude, run: locked(`{"pid":"x"}`)},

		// state unwritable
		{name: "state/json-is-a-directory", args: claude, run: bridgeCase{fixture: "ok", seed: seedProjectDir(".orchestrator/workers/b.json")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.workerStartInvocation(t, c)) })
	}
	t.Run("cross-runtime", h.testWorkerCrossRuntime)
	t.Run("PE-13/unread-prompt", h.testWorkerUnreadPrompt)
	t.Run("detach/pending-S6c", h.testWorkerDetachPending)
	t.Run("node-crash-block-reduction", testNodeCrashBlockReduction)
}

// testWorkerDetachPending asserts the S6c placeholder for a valid start with --detach; the JS
// would launch a detached runner, so it is not run.
func (h *harness) testWorkerDetachPending(t *testing.T) {
	sb := h.newSandbox(t)
	writeSeedFile(t, filepath.Join(sb.project, "b.md"), "brief\n")
	o := h.run(t, sb, []string{h.orchBin, "worker", "start", "--brief", "b.md", "--harness", "claude", "--detach"}, nil, nil)
	if o.exitCode != 2 || o.stdout != "" || o.stderr != "orch worker start --detach: not implemented yet (S6c)\n" || len(o.files) != 0 {
		t.Errorf("orch: exit %d, stdout %q, stderr %q, files %v; want the S6c placeholder", o.exitCode, o.stdout, o.stderr, o.files)
	}
}

// testWorkerCrossRuntime has one runtime's start write a lane and both runtimes' status read
// it: the state files are interchangeable.
func (h *harness) testWorkerCrossRuntime(t *testing.T) {
	start := h.workerStartInvocation(t, workerStartCase{args: []string{"--brief", "b.md", "--harness", "claude"}, run: bridgeCase{fixture: "ok"}})
	for _, writer := range []struct {
		name string
		argv []string
	}{{"written-by-js", start.js}, {"written-by-orch", start.orch}} {
		t.Run(writer.name, func(t *testing.T) {
			sb := h.newSandbox(t)
			start.seed(t, sb)
			if o := h.run(t, sb, writer.argv, nil, start.env); o.exitCode != 0 {
				t.Fatalf("start: exit %d, stdout %q, stderr %q", o.exitCode, o.stdout, o.stderr)
			}
			data, err := os.ReadFile(filepath.Join(sb.project, ".orchestrator", "workers", "b.json"))
			if err != nil {
				t.Fatal(err)
			}
			state := string(data)
			if !strings.Contains(state, `"status": "finished"`) {
				t.Fatalf("start wrote %s", state)
			}
			h.comparePair(t, h.workerInvocation(workerCase{args: []string{"status"}, seed: seedWorkerState("b", state)}))
		})
	}
}

// testWorkerUnreadPrompt asserts the PE-13 extension: opencode-exec given --effort X takes X as
// its prompt and never reads stdin, so a 1 MiB prompt meets a closed pipe. orch ignores the
// EPIPE and finishes the run; the JS dies of the uncaught `Error: write EPIPE`, leaving its
// lock and its "running" state.
func (h *harness) testWorkerUnreadPrompt(t *testing.T) {
	brief := strings.Repeat("x", 1<<20)
	opencodeOK := map[string]string{"jsonl": `{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"pong"}}` + "\n", "exit": "0\n"}
	js, orch := h.runPair(t, h.workerStartInvocation(t, workerStartCase{
		args:   []string{"--brief", "b.md", "--harness", "opencode", "--effort", "X"},
		bridge: opencodeBridge,
		run:    bridgeCase{script: opencodeOK},
		brief:  &brief,
	}))
	const lock, state = "project/workers/b.lock", "project/workers/b.json"
	if _, locked := orch.files[lock]; orch.exitCode != 0 || orch.stderr != "" || locked ||
		!strings.HasSuffix(orch.stdout, `{"lane":"b","status":"finished","session_id":"ses_1","exit_code":0}`+"\n") ||
		!strings.Contains(orch.files[state], `"status": "finished"`) {
		t.Errorf("orch: exit %d, stdout %q, stderr %q, lock left %v, state:\n%s", orch.exitCode, orch.stdout, orch.stderr, locked, orch.files[state])
	}
	if _, locked := js.files[lock]; js.exitCode != 1 || !strings.Contains(js.stderr, "Error: write EPIPE") || !locked ||
		!strings.Contains(js.files[state], `"status": "running"`) {
		t.Errorf("js did not die of the EPIPE as expected: exit %d, stderr %q, lock left %v, state:\n%s", js.exitCode, js.stderr, locked, js.files[state])
	}
}

// testNodeCrashBlockReduction pins the shape of a crash Node 26 prints (captured from
// v26.5.1) and its reduction to the error line, between relayed progress lines.
func testNodeCrashBlockReduction(t *testing.T) {
	const block = "file:///x/claude-exec.mjs:61\n" +
		"  if (ev.session_id && !result.session_id) result.session_id = ev.session_id;\n" +
		"         ^\n\n" +
		"TypeError: Cannot read properties of null (reading 'session_id')\n" +
		"    at Interface.<anonymous> (file:///x/claude-exec.mjs:61:10)\n" +
		"    at Interface.emit (node:events:508:28)\n\n" +
		"Node.js v26.5.1\n"
	in := "[claude] start\n" + block + `{"lane":"b"}` + "\n"
	want := "[claude] start\nTypeError: Cannot read properties of null (reading 'session_id')\n" + `{"lane":"b"}` + "\n"
	if got := withoutNodeCrashBlock(in); got != want {
		t.Errorf("withoutNodeCrashBlock\n got %q\nwant %q", got, want)
	}
}
