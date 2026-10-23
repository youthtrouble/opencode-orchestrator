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
	// launchOptions is the PE-62 launch_options orch records for a case that writes a state.
	launchOptions string
}

// pe46WorkerEffort is PE-46 through `orch worker`: the JS's opencode-exec ran opencode on
// --effort's value X as the prompt; orch's ignores it and runs on the worker prompt.
func pe46WorkerEffort(t *testing.T, js, orch *outcome) {
	t.Helper()
	const argv = "fakelog/opencode-argv.txt"
	const workerPrompt = "\nconductor\nYou are a worker in an orchestrated multi-agent project at "
	if !strings.HasSuffix(js.files[argv], "\nconductor\nX\n") || !strings.Contains(orch.files[argv], workerPrompt) {
		t.Errorf("PE-46 not observed: argv log js %q, orch %q", js.files[argv], orch.files[argv])
	}
	orch.files[argv] = js.files[argv]
	record := runRecordFile(t, orch.files)
	orch.files[record] = recordPromptHead.ReplaceAllString(orch.files[record], `${1}"X"`)
}

// recordPromptHead is a run record's prompt_head with its value.
var recordPromptHead = regexp.MustCompile(`("prompt_head": )"(?:[^"\\]|\\.)*"`)

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
	inv.launchOptions = c.launchOptions
	inv.exception = c.run.exception
	runFileNames := inv.normalizeWrites
	inv.normalizeWrites = func(o outcome, before map[string]string) outcome {
		return withLockHolderPids(withWorkerLockPid(runFileNames(o, before), before))
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

// PE-28: the lane-locked hint. The JS tells people to remove the lock; orch never does (C8).
const (
	jsLaneLockedHint   = `"hint":"inspect worker state; remove the lock only after confirming the process is gone"`
	orchLaneLockedHint = `"hint":"inspect worker status; the lane frees itself when its runner exits"`
)

// withJSLaneLockedHint rewrites orch's lane-locked hint to the JS's (PE-28).
func withJSLaneLockedHint(s string) string {
	return strings.ReplaceAll(s, orchLaneLockedHint, jsLaneLockedHint)
}

// testWorkerStartReclaimsAStaleLock asserts PE-28 (L8): a lock file that no process holds, as a
// runner killed with SIGKILL leaves it, does not block a foreground start. The JS, pinned here,
// refuses the lane until a person removes the file; orch claims it, runs and removes it.
func (h *harness) testWorkerStartReclaimsAStaleLock(t *testing.T) {
	stale := seeds(seedWorkerLockHolding("b", strconv.Itoa(reapedPid(t))), seedWorkerState("b", `{"lane":"b","status":"running","pid":1}`))
	js, orch := h.runPair(t, h.workerStartInvocation(t, workerStartCase{args: []string{"--brief", "b.md", "--harness", "claude"}, run: bridgeCase{fixture: "ok", seed: stale}}))
	// The state's pid reads <pid> once normalized.
	jsRefusal := `{"error":"lane locked","lane":"b","previous":{"status":"running","pid":<pid>,"alive":true},` + jsLaneLockedHint + "}\n"
	if js.exitCode != 3 || js.stdout != "" || js.stderr != jsRefusal {
		t.Errorf("js: exit %d, stdout %q, stderr %q; want the pinned lane-locked refusal", js.exitCode, js.stdout, js.stderr)
	}
	if _, locked := orch.files["project/workers/b.lock"]; orch.exitCode != 0 || !strings.Contains(orch.stdout, `"status":"finished"`) || locked {
		t.Errorf("orch: exit %d, stdout %q, stderr %q, lock left %v; want a finished run that removed the lock", orch.exitCode, orch.stdout, orch.stderr, locked)
	}
}

// opencodeRunOK scripts the fake opencode run to finish a session that used a tool.
var opencodeRunOK = map[string]string{"jsonl": `{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"pong"}}` + "\n" + `{"part":{"type":"tool","tool":"bash"}}` + "\n", "exit": "0\n"}

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
	const (
		briefBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[1]" argument must be of type string. Received type boolean (true)`
		cwdBoolean   = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	)
	live := strconv.Itoa(os.Getpid())
	dead := strconv.Itoa(reapedPid(t))
	locked := func(state string) bridgeCase {
		s := seedHeldLock(workerLockPath("b"))
		if state != "" {
			s = seeds(s, seedWorkerState("b", state))
		}
		return bridgeCase{seed: s, normalize: withJSLaneLockedHint}
	}

	directoryBrief := []string{"--brief", "d.md/", "--harness", "claude"}
	const eisdir = "Error: EISDIR: illegal operation on a directory, read"

	cases := []workerStartCase{
		// claude
		{name: "claude/finished", launchOptions: `{}`, args: claude, run: bridgeCase{fixture: "ok"}},
		{name: "claude/conductor-lane", launchOptions: `{}`, args: append(claude, "--lane", "conductor"), run: bridgeCase{fixture: "ok"}},
		{name: "claude/permission-denials", launchOptions: `{}`, args: claude, run: bridgeCase{fixture: "permission-denied"}},
		{name: "claude/failed-bad-model", launchOptions: `{}`, args: claude, run: bridgeCase{fixture: "bad-model"}},
		{name: "claude/failed-exit-with-stderr", launchOptions: `{}`, args: claude, run: bridgeCase{script: map[string]string{"stderr": "boom\nfatal: no auth\n", "exit": "1\n"}}},
		{name: "claude/failed-rate-limit", launchOptions: `{}`, args: claude, run: bridgeCase{script: map[string]string{"jsonl": `{"type":"result","is_error":true,"subtype":"rate_limited"}` + "\n", "exit": "1\n"}}},
		{name: "claude/deferred-red-cache", launchOptions: `{}`, args: claude, run: bridgeCase{usage: "red"}},
		{name: "claude/deferred-usage-limit", launchOptions: `{}`, args: claude, run: bridgeCase{fixture: "usage-limit", codex: true}},
		{name: "claude/every-flag", launchOptions: `{"effort":"high","timeout-min":"5","agent":"reviewer","sandbox":"read-only","permission-mode":"plan","allowed-tools":"Read,Edit","max-turns":"3"}`, run: bridgeCase{fixture: "ok"}, args: append(claude,
			"--model", "claude-haiku-5-5", "--effort", "high", "--resume", "abc", "--force", "--allowed-tools", "Read,Edit", "--max-turns", "3",
			"--timeout-min", "5", "--permission-mode", "plan", "--agent", "reviewer", "--breadth", "broad", "--run-id", "run-1",
			"--log-path", "/tmp/w.log", "--sandbox", "read-only", "--lane", "w1")},
		{name: "claude/bare-model", launchOptions: `{}`, args: append(claude, "--model"), run: bridgeCase{fixture: "ok"}},
		{name: "claude/bare-resume", launchOptions: `{}`, args: append(claude, "--resume"), run: bridgeCase{fixture: "usage-limit", codex: true}},
		{name: "claude/bare-lane-failed", launchOptions: `{}`, args: append(claude, "--lane"), run: bridgeCase{fixture: "bad-model"}},
		{name: "claude/long-final-message", launchOptions: `{}`, args: claude, run: bridgeCase{script: result(`"result":"` + strings.Repeat("é", 2100) + `"`)}},
		{name: "claude/empty-lane-cannot-post", launchOptions: `{}`, args: []string{"--brief", ".md", "--harness", "claude"}, run: bridgeCase{fixture: "bad-model", seed: seedProjectFile(".md", "brief\n")},
			crash: crashC1, errorLine: "Error: post needs from and subject"},
		{name: "claude/array-final-message", launchOptions: `{}`, args: claude, run: bridgeCase{script: result(`"result":["a",1,null]`)}},
		{name: "claude/numeric-final-message", launchOptions: `{}`, args: claude, run: bridgeCase{script: result(`"result":7`)}, crash: crashC2},
		{name: "claude/bridge-crash-null-event", launchOptions: `{}`, args: claude, run: bridgeCase{script: map[string]string{"jsonl": init + "null\n", "exit": "0\n"}}, bridgeCrash: true},

		// codex
		{name: "codex/finished", launchOptions: `{"agent":"x"}`, args: append(brief, "--harness", "codex", "--agent", "x"), bridge: codexBridge, run: bridgeCase{script: codexOK}},
		{name: "codex/sandbox-read-only", launchOptions: `{"sandbox":"read-only"}`, args: append(brief, "--harness", "codex", "--sandbox", "read-only"), bridge: codexBridge, run: bridgeCase{script: codexOK}},

		// opencode
		{name: "opencode/finished-with-agent", launchOptions: `{"agent":"coder"}`, args: append(brief, "--harness", "opencode", "--agent", "coder"), bridge: opencodeBridge, run: bridgeCase{script: opencodeRunOK}},
		{name: "opencode/PE-46/effort-is-not-the-prompt", launchOptions: `{"effort":"X"}`, args: append(brief, "--harness", "opencode", "--effort", "X"), bridge: opencodeBridge, run: bridgeCase{script: opencodeRunOK, exception: pe46WorkerEffort}},

		// validation
		{name: "usage/no-brief", args: []string{"--harness", "claude"}, run: bridgeCase{exception: pe48UsageLine}},
		{name: "usage/no-harness", args: brief, run: bridgeCase{exception: pe48UsageLine}},
		{name: "usage/missing-brief", args: []string{"--brief", "absent.md", "--harness", "claude"}},
		{name: "usage/brief-not-md", args: []string{"--brief", "b.txt", "--harness", "claude"}, run: bridgeCase{seed: seedProjectFile("b.txt", "x")}},
		{name: "usage/harness-gemini", args: append(brief, "--harness", "gemini")},
		{name: "usage/harness-bare", args: append(brief, "--harness")},
		{name: "usage/brief-bare", args: []string{"--harness", "claude", "--brief"}, crash: crashC1, errorLine: briefBoolean},
		{name: "usage/cwd-bare", args: append(claude, "--cwd"), crash: crashC1, errorLine: cwdBoolean},
		// Sol S2/R8: a lane with a slash is refused after the harness check, before anything is
		// written, with or without --detach.
		{name: "usage/lane-traversal", args: append(claude, "--lane", "../flip"), run: bridgeCase{seed: seedProjectFile(".orchestrator/flip.json", "{}\n")}},
		{name: "usage/lane-traversal-detach", args: append(claude, "--lane", "../flip", "--detach"), run: bridgeCase{seed: seedProjectFile(".orchestrator/flip.json", "{}\n")}},
		{name: "usage/lane-traversal-harness-first", args: append(brief, "--harness", "gemini", "--lane", "../flip")},
		// S2/R11: the brief is read before the lane is claimed, so neither leaves a lock or a
		// state, and the read error wins over a held lane (exit 1, not 3).
		{name: "crash/brief-is-a-directory", args: directoryBrief, brief: text(""), run: bridgeCase{seed: seedProjectDir("d.md")},
			crash: crashC1, errorLine: eisdir},
		{name: "crash/brief-is-a-directory-lane-locked", args: directoryBrief, brief: text(""), run: bridgeCase{seed: seeds(seedProjectDir("d.md"), seedHeldLock(workerLockPath("d")))},
			crash: crashC1, errorLine: eisdir},

		// lane locked
		{name: "locked/live-holder", args: claude, run: locked(`{"lane":"b","status":"running","pid":` + live + `}`)},
		{name: "locked/dead-holder", args: claude, run: locked(`{"lane":"b","status":"running","pid":` + dead + `}`)},
		{name: "locked/no-state", args: claude, run: locked("")},
		{name: "locked/state-null", args: claude, run: locked(`null`)},
		{name: "locked/state-zero", args: claude, run: locked(`0`)},
		{name: "locked/state-string", args: claude, run: locked(`"abc"`)},
		{name: "locked/state-without-status", args: claude, run: locked(`{"pid":"x"}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.workerStartInvocation(t, c)) })
	}
	// The state path a directory: every publish fails its rename (EISDIR), and the run goes on
	// with the failure recorded. PE-21: the JS leaves `b.json.tmp`, orch no temp.
	t.Run("state/PE-21-json-is-a-directory", func(t *testing.T) {
		js, orch := h.runPair(t, h.workerStartInvocation(t, workerStartCase{args: claude, run: bridgeCase{fixture: "ok", seed: seedProjectDir(".orchestrator/workers/b.json")}}))
		assertPE21FailedRename(t, &js, &orch, "project/workers/b.json", `"last_state_write_error": "EISDIR"`)
		compareOutcomes(t, js, orch)
	})
	// comparePair only diffs one runtime's files against the other's, so it would pass if both
	// left a lock; this asserts the absence in each.
	t.Run("unreadable-brief-claims-nothing", func(t *testing.T) {
		js, orch := h.runPair(t, h.workerStartInvocation(t, workerStartCase{args: directoryBrief, brief: text(""), run: bridgeCase{seed: seedProjectDir("d.md")}}))
		for runtime, o := range map[string]outcome{"js": js, "orch": orch} {
			for _, path := range []string{"project/workers/d.lock", "project/workers/d.json"} {
				if _, present := o.files[path]; present {
					t.Errorf("%s left %s", runtime, path)
				}
			}
		}
	})
	t.Run("PE-28/stale-lock-is-reclaimed", h.testWorkerStartReclaimsAStaleLock)
	t.Run("PE-24/failed-run-scrubs-tokens", h.testWorkerFailedRunScrubsTokens)
	t.Run("cross-runtime", h.testWorkerCrossRuntime)
	t.Run("PE-13/unread-prompt", h.testWorkerUnreadPrompt)
	t.Run("node-crash-block-reduction", testNodeCrashBlockReduction)
}

// testWorkerFailedRunScrubsTokens asserts PE-24: the bridge fails with a credential on stderr.
// The JS copies it into the state's errors and the blocked message; orch writes <token> there.
// With the JS's marker replaced, everything else (run record and incident included) is equal.
func (h *harness) testWorkerFailedRunScrubsTokens(t *testing.T) {
	apiKey := "sk-" + "ant-" + strings.Repeat("x", 24)
	run := bridgeCase{script: map[string]string{"stderr": "401 invalid x-api-key " + apiKey + "\n", "exit": "1\n"}}
	js, orch := h.runPair(t, h.workerStartInvocation(t, workerStartCase{args: []string{"--brief", "b.md", "--harness", "claude"}, run: run}))
	withPE62(t, &js, &orch, `{}`)
	for _, file := range []string{"project/workers/b.json", "project/mailbox/messages.jsonl"} {
		if !strings.Contains(js.files[file], apiKey) {
			t.Errorf("js %s lacks the marker (PE-24 expects it verbatim):\n%s", file, js.files[file])
		}
		if strings.Contains(orch.files[file], apiKey) || !strings.Contains(orch.files[file], "x-api-key <token>") {
			t.Errorf("orch %s is not scrubbed (PE-24):\n%s", file, orch.files[file])
		}
		js.files[file] = strings.ReplaceAll(js.files[file], apiKey, "<token>")
	}
	compareOutcomes(t, js, orch)
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

// testWorkerUnreadPrompt asserts the PE-13 extension: the JS opencode-exec given --effort X
// takes X as its prompt and never reads stdin. Its 1 MiB brief exceeds Node's Unix socketpair
// buffer (assuming Linux's default of about 208 KiB), so the JS dies of the uncaught
// `Error: write EPIPE`, leaving its lock and "running" state. orch's 100 KiB brief exceeds a
// 64 KiB pipe buffer but stays under Linux's 128 KiB single-argument limit: opencode-exec
// reads the prompt (PE-46), passes it to opencode in argv, and finishes the run.
func (h *harness) testWorkerUnreadPrompt(t *testing.T) {
	jsBrief := strings.Repeat("x", 1<<20)
	orchBrief := strings.Repeat("x", 100<<10)
	opencodeOK := map[string]string{"jsonl": `{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"pong"}}` + "\n", "exit": "0\n"}
	c := workerStartCase{
		args:   []string{"--brief", "b.md", "--harness", "opencode", "--effort", "X"},
		bridge: opencodeBridge,
		run:    bridgeCase{script: opencodeOK},
		brief:  &jsBrief,
	}
	jsInv := h.workerStartInvocation(t, c)
	c.brief = &orchBrief
	orchInv := h.workerStartInvocation(t, c)
	js := h.runSide(t, jsInv, jsInv.js)
	orch := h.runSide(t, orchInv, orchInv.orch)
	const lock, state = "project/workers/b.lock", "project/workers/b.json"
	if _, locked := orch.files[lock]; orch.exitCode != 0 || orch.stderr != "" || locked ||
		!strings.HasSuffix(orch.stdout, `{"lane":"b","status":"finished","session_id":"ses_1","exit_code":0}`+"\n") ||
		!strings.Contains(orch.files[state], `"status": "finished"`) ||
		!strings.Contains(orch.files["fakelog/opencode-argv.txt"], orchBrief) {
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
