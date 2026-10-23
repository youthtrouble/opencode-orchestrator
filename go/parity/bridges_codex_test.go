//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCodexExec compares `orch codex-exec` with bin/codex-exec.mjs over the scripted codex
// exec fake. There are no codex captures yet (S5b-F), so every event is written inline.
func (h *harness) testCodexExec(t *testing.T) {
	nonExecutable := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonExecutable, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const (
		pathArgBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
		modelIncludes  = "TypeError: model.includes is not a function"
	)
	thread := `{"type":"thread.started","thread_id":"0199a1b2-c3d4"}` + "\n"
	item := func(fields string) string { return `{"type":"item.completed","item":{` + fields + `}}` + "\n" }
	completed := `{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":0,"output_tokens":3}}` + "\n"
	ok := map[string]string{"jsonl": thread + item(`"id":"item_0","type":"agent_message","text":"pong"`) + completed, "exit": "0\n"}
	p := []string{"--prompt", "p"}
	cases := []bridgeCase{
		// Every flag, and how each argument is passed.
		{name: "flags/all", script: ok, seed: seedProjectFile("sub/.keep", ""), args: []string{
			"--model", "gpt-6-luna", "--effort", "low", "--sandbox", "read-only", "--cwd", "sub", "--schema", "s.json",
			"--add-dir", "../x/./y", "--ephemeral", "--timeout-min", "5", "--force", "--prompt", "multi\nline \u00e9", "extra"}},
		{name: "flags/default-sandbox-approve-for-me", args: p, script: ok},
		{name: "flags/read-only", args: []string{"--sandbox", "read-only", "--prompt", "p"}, script: ok},
		{name: "flags/danger-full-access", args: []string{"--sandbox", "danger-full-access", "--prompt", "p"}, script: ok},
		{name: "flags/resume", args: []string{"--resume", "abc", "--model", "m", "--prompt", "p"}, script: ok},
		{name: "flags/resume-with-add-dir-and-schema", args: []string{"--resume", "abc", "--add-dir", "../x/./y", "--schema", "s.json", "--ephemeral", "--prompt", "p"}, script: ok},
		{name: "flags/empty-values", args: []string{"--model", "", "--effort", "", "--sandbox", "", "--resume", "", "--schema", "", "--add-dir", "", "--cwd", "", "--prompt", "p"}, script: ok},
		{name: "flags/unknown-keys-are-flags", args: []string{"--frobnicate", "x", "--prompt", "p"}, script: ok},
		{name: "quiet", args: []string{"--quiet", "--prompt", "p"}, script: ok},
		{name: "help", args: []string{"--prompt", "p", "--help"}, help: true},

		// Prompt sources; the prompt reaches codex on stdin.
		{name: "prompt/positional", args: []string{"fix", "the", "bug"}, script: ok},
		{name: "prompt/file", args: []string{"--prompt-file", "brief.md", "--prompt", "loses"}, script: ok, seed: seedProjectFile("brief.md", "from file \xff\n"+strings.Repeat("ab", 300))},
		{name: "prompt/stdin", stdin: "from stdin\n", script: ok},
		{name: "prompt/empty-stdin", script: ok},
		{name: "prompt/file-missing", args: []string{"--prompt-file", "absent.md"}},
		{name: "prompt/file-is-directory", args: []string{"--prompt-file", "."}},
		{name: "prompt/file-unreadable", args: []string{"--prompt-file", "secret.md"}, seed: seedUnreadable("secret.md")},
		{name: "prompt/file-bare", args: []string{"--prompt-file"}},

		// The budget gate (openai).
		{name: "gate/red-refuses", args: p, usage: "red"},
		{name: "gate/red-forced", args: []string{"--force", "--prompt", "p"}, usage: "red", script: ok},
		{name: "gate/unknown-usage", args: p, usage: "unknown", script: ok},
		{name: "gate/anthropic-red-allows-codex", args: p, usage: "anthropic-red", script: ok},
		{name: "gate/codex-spawn-fails", args: p, usage: "none", env: []string{"PATH=" + h.pathWithout(t, "codex")}, crash: crashC1, errorLine: "Error: spawn codex ENOENT"},

		// How codex ends.
		{name: "end/killed-by-signal", args: p, script: map[string]string{"jsonl": thread, "signal": "TERM\n"}},
		{name: "end/stderr-tail", args: p, script: map[string]string{"stderr": "\u00a0first\nsecond\r\nthird\n\nfifth \n\t\n", "exit": "2\n"}},
		{name: "end/no-output", args: p, script: map[string]string{"exit": "0\n"}},
		{name: "PE-9/PE-45/timeout-0", args: []string{"--timeout-min", "0", "--prompt", "p"}, script: map[string]string{"sleep": "30\n"}, dropFakeLogs: true, normalize: withoutNodeWarnings,
			exception: pe45Timeout("codex", "0", "codex exited null: ")},
		{name: "PE-9/PE-45/timeout-negative-warns-in-node", args: []string{"--timeout-min", "-1", "--prompt", "p"}, script: map[string]string{"sleep": "30\n"}, dropFakeLogs: true, normalize: withoutNodeWarnings,
			exception: pe45Timeout("codex", "-1", "codex exited null: ")},
		// PE-44: the JS ignored top-level error events, so a usage limit was a crash with the exit.
		{name: "PE-44/top-level-error-is-a-limit-incident", args: p, script: map[string]string{"jsonl": `{"type":"error","message":"You've hit your usage limit."}` + "\n", "exit": "1\n"},
			exception: rewriteException("PE-44",
				`"kind":"limit","summary":"codex bridge failed: You've hit your usage limit."`, `"kind":"crash","summary":"codex bridge failed: codex exited 1: "`,
				`"You've hit your usage limit."`, `"codex exited 1: "`,
				"[codex] error: You've hit your usage limit.\n", "")},
		{name: "end/limit-in-errors-is-a-limit-incident", args: p, script: map[string]string{"jsonl": `{"type":"turn.failed","error":{"message":"Rate limit reached"}}` + "\n", "exit": "1\n"}},

		// A trailing value key without a value is the JS value true.
		{name: "bare/model", args: []string{"--prompt", "p", "--model"}, crash: crashC1, errorLine: modelIncludes},
		{name: "bare/model-before-the-gate", args: []string{"--prompt", "p", "--model"}, usage: "red", crash: crashC1, errorLine: modelIncludes},
		{name: "bare/effort", args: []string{"--prompt", "p", "--effort"}, script: ok},
		{name: "bare/sandbox", args: []string{"--prompt", "p", "--sandbox"}, script: ok},
		{name: "bare/timeout-min", args: []string{"--prompt", "p", "--timeout-min"}, script: ok},
		{name: "bare/resume-with-a-thread", args: []string{"--prompt", "p", "--resume"}, script: ok},
		{name: "bare/resume-crashes-in-the-run-record", args: []string{"--prompt", "p", "--resume"}, script: map[string]string{"jsonl": completed, "exit": "0\n"}, crash: crashC2},
		{name: "bare/cwd", args: []string{"--prompt", "p", "--cwd"}, crash: crashC1, errorLine: pathArgBoolean},
		{name: "bare/cwd-before-the-gate", args: []string{"--prompt", "p", "--cwd"}, usage: "red", crash: crashC1, errorLine: pathArgBoolean},
		{name: "bare/schema", args: []string{"--prompt", "p", "--schema"}, crash: crashC1, errorLine: pathArgBoolean},
		{name: "bare/schema-after-the-gate", args: []string{"--prompt", "p", "--schema"}, usage: "red"},
		{name: "bare/add-dir", args: []string{"--prompt", "p", "--add-dir"}, crash: crashC1, errorLine: pathArgBoolean},

		// Other crashes.
		{name: "crash/spawn-enoent", args: p, env: []string{"PATH=" + h.pathWithout(t, "codex")}, crash: crashC1, errorLine: "Error: spawn codex ENOENT"},
		{name: "crash/spawn-eacces", args: p, env: []string{"PATH=" + h.pathWithout(t, "codex", nonExecutable)}, crash: crashC1, errorLine: "Error: spawn codex EACCES"},
		{name: "crash/cwd-missing", args: []string{"--cwd", "absent", "--prompt", "p"}, crash: crashC1, errorLine: "Error: spawn codex ENOENT"},
		{name: "crash/null-event", args: p, script: map[string]string{"jsonl": thread + "null\n" + thread, "exit": "0\n"}, crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'type')"},
		{name: "crash/null-change", args: p, script: map[string]string{"jsonl": thread + item(`"type":"file_change","changes":[{"path":"a","kind":"add"},null]`), "exit": "0\n"}, crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'path')"},
		{name: "crash/text-not-a-string", args: p, script: map[string]string{"jsonl": thread + item(`"type":"agent_message","text":7`), "exit": "0\n"}, crash: crashC2},
		{name: "crash/text-not-a-string-under-quiet", args: []string{"--quiet", "--prompt", "p"}, script: map[string]string{"jsonl": item(`"type":"reasoning","text":["a"]`), "exit": "0\n"}, crash: crashC2},
		{name: "crash/command-not-a-string", args: p, script: map[string]string{"jsonl": item(`"type":"command_execution","command":{"a":1},"exit_code":0`), "exit": "0\n"}, crash: crashC2},
		{name: "crash/changes-not-iterable", args: p, script: map[string]string{"jsonl": item(`"type":"file_change","changes":5`), "exit": "0\n"}, crash: crashC1, errorLine: "TypeError: number 5 is not iterable (cannot read property Symbol(Symbol.iterator))"},
		{name: "crash/changes-an-object", args: p, script: map[string]string{"jsonl": item(`"type":"file_change","changes":{"path":"a"}`), "exit": "0\n"}, crash: crashC1, errorLine: "TypeError: object is not iterable (cannot read property Symbol(Symbol.iterator))"},
		{name: "crash/changes-a-string", args: p, script: map[string]string{"jsonl": item(`"type":"file_change","changes":"ab"`), "exit": "0\n"}, crash: crashC2},

		// Event shapes that do not crash.
		{name: "events/thread-without-an-id-then-with-one", args: []string{"--resume", "r", "--prompt", "p"}, script: map[string]string{"exit": "0\n", "jsonl": `{"type":"thread.started"}` + "\n" + completed}},
		{name: "events/thread-id-returns-to-its-place", args: p, script: map[string]string{"exit": "0\n", "jsonl": `{"type":"thread.started"}` + "\n" + `{"type":"thread.started","thread_id":"t2"}` + "\n" + completed}},
		{name: "events/odd-shapes", args: p, script: map[string]string{"exit": "0\n", "jsonl": strings.Join([]string{
			"not json", "5", `"s"`, "[1]", "{}", `{"type":"turn.started"}`,
			`  {"type":"thread.started","thread_id":"t1"}  `, `{"type":"item.completed","item":5}`, `{"type":"item.completed"}`,
			`{"type":"item.completed","item":{"type":"agent_message","text":"first\nline"}}`,
			`{"type":"item.completed","item":{"type":"agent_message","text":""}}`,
			`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./..."}}`,
			`{"type":"item.completed","item":{"type":"command_execution","command":["ls","-l",null],"exit_code":null}}`,
			`{"type":"item.completed","item":{"type":"command_execution","exit_code":1}}`,
			`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go","kind":"update"},{"kind":"add"},5,"s",{"path":{"x":1}}]}}`,
			`{"type":"item.completed","item":{"type":"file_change"}}`,
			`{"type":"item.completed","item":{"type":"error"}}`,
			`{"type":"item.completed","item":{"type":"error","message":{"a":1}}}`,
			`{"type":"item.completed","item":{"type":"reasoning","text":"` + strings.Repeat("r", 110) + `"}}`,
			`{"type":"item.completed","item":{"type":"todo_list","items":[]}}`,
			`{"type":"turn.failed","error":"boom"}`, `{"type":"turn.failed"}`, `{"type":"turn.failed","error":{"message":"","code":1.50}}`,
			`{"type":"turn.failed","error":{"message":5}}`,
			`{"type":"turn.completed","usage":0}`, `{"type":"turn.completed","usage":{"n":1}}`,
		}, "\n") + "\n"}},
		// readline ends lines on raw U+2028 too (JSON.stringify leaves it raw): the event splits
		// into fragments that do not parse, so no message and no completed turn.
		{name: "events/raw-line-separator", args: p, script: map[string]string{"exit": "0\n", "jsonl": thread +
			item(`"type":"agent_message","text":"a`+"\u2028"+`b"`) + `{"type":"turn.completed","usage":{"x":"y` + "\u2028" + `z"}}` + "\n"}},

		// The tool-host no-op path and the environment.
		{name: "paths/tool-host-no-op", args: p, script: map[string]string{"exit": "0\n", "jsonl": thread + item(`"type":"agent_message","text":"I could not run the tests: Tool host unavailable."`) + completed}},
		{name: "paths/tool-host-no-op-without-thread", args: p, script: map[string]string{"exit": "0\n", "jsonl": `{"type":"thread.started"}` + "\n" + item(`"type":"agent_message","text":"no tool"`) + completed}},
		{name: "env/claudecode-reaches-codex", args: p, script: ok, env: []string{"CLAUDECODE=1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.comparePair(t, h.bridgeInvocation(t, codexBridge, c))
		})
	}
	t.Run("PE-8/bare-prompt", func(t *testing.T) { h.testCodexBarePrompt(t) })
	t.Run("PE-13/unread-prompt", func(t *testing.T) { h.testCodexUnreadPrompt(t) })
	t.Run("PE-1/cuts-split-surrogates", func(t *testing.T) { h.testCodexSplitSurrogates(t) })
}

// testCodexBarePrompt asserts PE-8 for codex-exec: with a trailing bare --prompt the JS logs its
// start line, spawns codex and dies of stdin.end(true); orch refuses before the gate. The fake
// is left unscripted, so the codex the JS orphans writes nothing.
func (h *harness) testCodexBarePrompt(t *testing.T) {
	js, orch := h.runPair(t, h.bridgeInvocation(t, codexBridge, bridgeCase{args: []string{"--prompt"}}))
	if js.exitCode != 1 || js.stdout != "" || !strings.HasPrefix(js.stderr, "[codex] start model=gpt-6.1-sol ") || !strings.Contains(js.stderr, "TypeError [ERR_INVALID_ARG_TYPE]: The \"chunk\" argument") {
		t.Errorf("js: exit %d, stdout %q, stderr:\n%s", js.exitCode, js.stdout, js.stderr)
	}
	if orch.exitCode != 2 || orch.stdout != "" || orch.stderr != "no prompt: --prompt needs a value\n" {
		t.Errorf("orch: exit %d, stdout %q, stderr %q", orch.exitCode, orch.stdout, orch.stderr)
	}
}

// testCodexUnreadPrompt asserts PE-13: codex exits 2 without reading a 1 MiB prompt. The JS
// dies of the uncaught EPIPE on codex's stdin, with no result and no record; orch reports the
// failed run. Both started the same codex.
func (h *harness) testCodexUnreadPrompt(t *testing.T) {
	js, orch := h.runPair(t, h.bridgeInvocation(t, codexBridge, bridgeCase{
		args:   []string{"--prompt-file", "big.md"},
		seed:   seedProjectFile("big.md", strings.Repeat("p", 1<<20)),
		script: map[string]string{"nostdin": "", "exit": "2\n"},
	}))
	if js.exitCode != 1 || js.stdout != "" || !strings.Contains(js.stderr, "\nError: write EPIPE\n") {
		t.Errorf("js: exit %d, stdout %q, stderr:\n%s", js.exitCode, js.stdout, js.stderr)
	}
	if orch.exitCode != 1 || !strings.Contains(orch.stdout, `"errors":["codex exited 2: "],"exit_code":2,`) ||
		!strings.Contains(orch.files["state/incidents.jsonl"], `"summary":"codex bridge failed: codex exited 2: "`) {
		t.Errorf("orch: exit %d, stdout %q, incidents %q", orch.exitCode, orch.stdout, orch.files["state/incidents.jsonl"])
	}
	runRecordFile(t, orch.files)
	for _, log := range []string{"fakelog/codex-argv.txt", "fakelog/codex-cwd.txt", "fakelog/codex-env.txt"} {
		if js.files[log] == "" || js.files[log] != orch.files[log] {
			t.Errorf("%s differs\n  js: %q\norch: %q", log, js.files[log], orch.files[log])
		}
	}
}

// testCodexSplitSurrogates asserts PE-1 for codex-exec's cuts that reach JSON: the 160-unit
// incident summary, the 400-unit no-op detail and the 400-unit prompt_head, each cut inside an
// emoji. The JS writes the lone high surrogate as "\ud83d", orch U+FFFD; with that escape
// replaced, stdout, the run record and the incidents are identical.
func (h *harness) testCodexSplitSurrogates(t *testing.T) {
	const emoji, lone = "\U0001F600", `\ud83d`
	noOp := strings.Repeat("no tool. ", 44) + "abc" // 399 units
	events := `{"type":"item.completed","item":{"type":"error","message":"` + strings.Repeat("c", 159) + emoji + `"}}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"` + noOp + emoji + `"}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	prompt := strings.Repeat("p", 399) + emoji
	js, orch := h.runPair(t, h.bridgeInvocation(t, codexBridge, bridgeCase{args: []string{"--prompt", prompt}, script: map[string]string{"jsonl": events, "exit": "0\n"}}))
	observe := func(where, got string, wants ...string) string {
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("PE-1 not observed: js %s lacks %q:\n%s", where, want, got)
			}
		}
		return strings.ReplaceAll(got, lone, "\ufffd")
	}
	const incidents = "state/incidents.jsonl"
	js.files[incidents] = observe(incidents, js.files[incidents], `"detail":"`+noOp+lone+`"`, `"summary":"codex bridge failed: `+strings.Repeat("c", 159)+lone+`"`)
	record := runRecordFile(t, js.files)
	js.files[record] = observe(record, js.files[record], `"prompt_head": "`+strings.Repeat("p", 399)+lone+`"`)
	compareOutcomes(t, js, orch)
}
