//go:build parity

package parity

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testOpencodeExec compares `orch opencode-exec` with bin/opencode-exec.mjs over the scripted
// opencode run fake. There are no opencode captures yet (S5b-F), so every event is written
// inline.
func (h *harness) testOpencodeExec(t *testing.T) {
	nonExecutable := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonExecutable, "opencode"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := func(s string) string {
		return `{"type":"text","sessionID":"ses_0123456789","part":{"type":"text","text":"` + s + `"}}` + "\n"
	}
	ok := map[string]string{"jsonl": text("pong"), "exit": "0\n"}
	p := []string{"--prompt", "p"}
	cases := []bridgeCase{
		// Every flag, and how each argument is passed.
		{name: "flags/default-agent", args: p, script: ok},
		{name: "flags/all", args: []string{"--agent", "coder", "--resume", "ses_r", "--model", "openai/gpt-6-luna", "--cwd", "sub", "--timeout-min", "5", "--prompt", "multi\nline \u00e9", "extra"}, script: ok, seed: seedProjectFile("sub/.keep", "")},
		{name: "flags/empty-values", args: []string{"--agent", "", "--resume", "", "--model", "", "--cwd", "", "--prompt", "p"}, script: ok},
		{name: "flags/unknown-keys-are-flags", args: []string{"--frobnicate", "x", "--prompt", "p"}, script: ok},
		{name: "PE-46/help-prints-help", args: []string{"--help", "--prompt", "p"}, script: ok, exception: pe46Help},
		{name: "PE-46/effort-value-is-not-the-prompt", args: []string{"--effort", "high"}, script: ok, exception: pe46Effort},
		{name: "flags/effort-with-a-prompt", args: []string{"--effort", "high", "--prompt", "p"}, script: ok},
		{name: "quiet", args: []string{"--quiet", "--prompt", "p"}, script: map[string]string{"jsonl": `{"part":{"type":"tool","tool":"bash"}}` + "\n" + text("pong"), "exit": "0\n"}},
		{name: "force", args: []string{"--force", "--prompt", "p"}, script: ok},

		// Prompt sources; the prompt is opencode's last argument.
		{name: "prompt/positional", args: []string{"fix", "the", "bug"}, script: ok},
		{name: "prompt/file", args: []string{"--prompt-file", "brief.md", "--prompt", "loses"}, script: ok, seed: seedProjectFile("brief.md", "from file \xff\n"+strings.Repeat("ab", 300))},
		{name: "prompt/stdin", stdin: "from stdin\n", script: ok},
		{name: "prompt/empty-stdin", script: ok},
		{name: "prompt/file-missing", args: []string{"--prompt-file", "absent.md"}},
		{name: "prompt/file-is-directory", args: []string{"--prompt-file", "."}},
		{name: "prompt/file-unreadable", args: []string{"--prompt-file", "secret.md"}, seed: seedUnreadable("secret.md")},
		{name: "prompt/file-bare", args: []string{"--prompt-file"}},

		// The budget gate (openai, never frontier).
		{name: "gate/red-refuses", args: p, usage: "red"},
		{name: "gate/red-forced", args: []string{"--force", "--prompt", "p"}, usage: "red", script: ok},
		{name: "gate/unknown-usage", args: p, usage: "unknown", script: ok},
		{name: "gate/anthropic-red-allows-opencode", args: p, usage: "anthropic-red", script: ok},
		{name: "gate/codex-spawn-fails", args: p, usage: "none", env: []string{"PATH=" + h.pathWithout(t, "codex")}, crash: crashC1, errorLine: "Error: spawn codex ENOENT"},

		// How opencode ends.
		{name: "end/stderr-tail", args: p, script: map[string]string{"jsonl": text("partial"), "stderr": "\u00a0first\nsecond\r\nthird\n\nfifth \n\t\n", "exit": "2\n"}},
		{name: "end/empty-stderr", args: p, script: map[string]string{"exit": "1\n"}},
		{name: "end/killed-by-signal", args: p, script: map[string]string{"jsonl": text("hi"), "signal": "TERM\n"}},
		{name: "end/no-output", args: p, script: map[string]string{"exit": "0\n"}},
		// PE-65 (SIGINT, not SIGTERM) is not visible here: the fake dies of either; see
		// TestBridgeStopSignals.
		{name: "PE-9/PE-45/timeout-0-logs-nothing", args: []string{"--timeout-min", "0", "--prompt", "p"}, script: map[string]string{"sleep": "30\n"}, dropFakeLogs: true, normalize: withoutNodeWarnings,
			exception: pe45Timeout("opencode", "0", "")},
		{name: "end/limit-in-errors-is-a-limit-incident", args: p, script: map[string]string{"stderr": "Error: rate limit exceeded\n", "exit": "1\n"}},
		// PE-44: the JS ignored error events and filed a usage limit as a crash with the empty
		// stderr tail; PE-47: and without the model.
		{name: "PE-44/PE-47/error-event-is-a-limit-incident", args: []string{"--model", "openai/gpt-6.1-sol", "--prompt", "p"},
			script: map[string]string{"exit": "1\n", "jsonl": `{"type":"error","timestamp":1,"sessionID":"ses_1","error":{"type":"unknown","message":"ChatGPT usage limit reached"}}` + "\n"},
			exception: rewriteException("PE-44",
				`"kind":"limit","summary":"opencode bridge failed: ChatGPT usage limit reached"`, `"kind":"crash","summary":"opencode bridge failed: "`,
				`"ChatGPT usage limit reached"`, `""`,
				`"context":{"model":"openai/gpt-6.1-sol",`, `"context":{"model":null,`,
				"[opencode] error: ChatGPT usage limit reached\n", "")},

		// A trailing value key without a value is the JS value true.
		{name: "bare/agent", args: []string{"--prompt", "p", "--agent"}, script: ok},
		{name: "bare/model", args: []string{"--prompt", "p", "--model"}, script: ok},
		{name: "bare/timeout-min", args: []string{"--prompt", "p", "--timeout-min"}, script: ok},
		{name: "bare/resume-crashes-in-the-run-record", args: []string{"--prompt", "p", "--resume"}, script: ok, crash: crashC2},
		{name: "bare/cwd", args: []string{"--prompt", "p", "--cwd"}, crash: crashC1, errorLine: `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`},

		// Events.
		{name: "events/first-truthy-session-wins", args: p, script: map[string]string{"exit": "0\n", "jsonl": `{"sessionID":""}` + "\n" + `{"sessionID":0}` + "\n" + `{"sessionID":"ses_first"}` + "\n" + `{"sessionID":"ses_second"}` + "\n"}},
		{name: "events/resume-blocks-the-session", args: []string{"--resume", "ses_r", "--prompt", "p"}, script: map[string]string{"exit": "0\n", "jsonl": `{"sessionID":"ses_other"}` + "\n"}},
		{name: "events/session-of-another-type", args: p, script: map[string]string{"exit": "0\n", "jsonl": `{"sessionID":["s","t"]}` + "\n"}},
		{name: "events/odd-shapes", args: p, script: map[string]string{"exit": "0\n", "jsonl": strings.Join([]string{
			"not json", "5", `"s"`, "[1]", "{}", `{"part":5}`, `{"part":null}`, `{"part":"text"}`, `{"part":[1]}`,
			`  {"part":{"type":"text","text":"first\nline"}}  `, `{"part":{"type":"text","text":""}}`, `{"part":{"type":"text"}}`,
			`{"part":{"type":"tool","tool":"bash"}}`, `{"part":{"type":"tool"}}`, `{"part":{"type":"tool","tool":{"a":1},"text":"t"}}`,
			`{"part":{"type":"reasoning","text":"r"}}`,
			`{"part":{"type":"text","text":"a\nb\rc` + strings.Repeat("\U0001F600", 80) + `"}}`,
		}, "\n") + "\n"}},
		{name: "events/raw-line-separator", args: p, script: map[string]string{"exit": "0\n", "jsonl": text("a"+"\u2028"+"b") + text("whole")}},

		// Crashes.
		{name: "crash/null-event", args: p, script: map[string]string{"jsonl": text("before") + "null\n" + text("after"), "exit": "0\n"}, crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'sessionID')"},
		{name: "crash/text-not-a-string", args: p, script: map[string]string{"jsonl": `{"part":{"type":"text","text":7}}` + "\n", "exit": "0\n"}, crash: crashC2},
		{name: "crash/text-not-a-string-under-quiet", args: []string{"--quiet", "--prompt", "p"}, script: map[string]string{"jsonl": `{"part":{"type":"text","text":["a"]}}` + "\n", "exit": "0\n"}, crash: crashC2},
		{name: "crash/spawn-enoent", args: p, env: []string{"PATH=" + h.pathWithout(t, "opencode")}, crash: crashC1, errorLine: "Error: spawn opencode ENOENT"},
		{name: "crash/spawn-eacces", args: p, env: []string{"PATH=" + h.pathWithout(t, "opencode", nonExecutable)}, crash: crashC1, errorLine: "Error: spawn opencode EACCES"},
		{name: "crash/cwd-missing", args: []string{"--cwd", "absent", "--prompt", "p"}, crash: crashC1, errorLine: "Error: spawn opencode ENOENT"},
		{name: "crash/null-byte-in-the-prompt", stdin: "a\x00b", script: ok, crash: crashC1, errorLine: `TypeError [ERR_INVALID_ARG_VALUE]: The argument 'args[6]' must be a string without null bytes. Received 'a\x00b'`},

		// The environment is inherited whole.
		{name: "env/claudecode-reaches-opencode", args: p, script: ok, env: []string{"CLAUDECODE=1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.comparePair(t, h.bridgeInvocation(t, opencodeBridge, c))
		})
	}
	t.Run("PE-8/bare-prompt", func(t *testing.T) { h.testOpencodeBarePrompt(t) })
	t.Run("PE-1/prompt-head-split-surrogate", func(t *testing.T) { h.testOpencodeSplitSurrogate(t) })
}

// pe46Help is PE-46 for --help: the JS ignored it and ran opencode; orch prints its help and
// runs nothing. orch's whole outcome is asserted: exit 0, the exact help, no stderr, and only the
// files the case seeded, unchanged (the JS's files less those its run wrote).
func pe46Help(t *testing.T, js, orch *outcome) {
	t.Helper()
	jsRunWrites := []string{"fakelog/opencode-argv.txt", "fakelog/opencode-cwd.txt", "fakelog/opencode-env.txt", runRecordFile(t, js.files)}
	if js.exitCode != 0 || !strings.Contains(js.stdout, `"ok":true`) || !strings.HasSuffix(js.files[jsRunWrites[0]], "\np\n") {
		t.Errorf("js: exit %d, stdout %q, argv log %q", js.exitCode, js.stdout, js.files[jsRunWrites[0]])
	}
	if orch.exitCode != 0 || orch.stdout != opencodeExecHelp || orch.stderr != "" {
		t.Errorf("orch: exit %d, stdout %q, stderr %q", orch.exitCode, orch.stdout, orch.stderr)
	}
	seeded := maps.Clone(js.files)
	for _, name := range jsRunWrites {
		delete(seeded, name)
	}
	if !maps.Equal(orch.files, seeded) {
		t.Errorf("orch files %q, want the seeded files only %q", slices.Sorted(maps.Keys(orch.files)), slices.Sorted(maps.Keys(seeded)))
	}
	*orch = *js
}

// opencodeExecHelp is what `orch opencode-exec --help` prints (PE-46).
const opencodeExecHelp = `Run one headless opencode turn (opencode run --format json) on the ChatGPT subscription, structured result back.

  orch opencode-exec [--agent conductor] [--model PROVIDER/MODEL] [--effort LEVEL (ignored)] [--cwd DIR]
                     [--artifact-root DIR] [--resume SESSION_ID] [--timeout-min 120] [--force] [--quiet]
                     [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]

Prints one JSON object on stdout: { ok, harness, session_id, agent, cwd, started_at, final_message,
errors, exit_code, duration_ms, record }. opencode is stopped with SIGINT at the --timeout-min cap and
on a forwarded SIGTERM. Exit codes: 0 ok, 1 failed, 2 no prompt, 75 budget refused.
`

// pe46Effort is PE-46 for --effort: the JS read it as a flag and ran opencode on its value as
// the prompt; orch reads the value, ignores it and, with no other prompt, runs on stdin's (empty).
var pe46Effort = rewriteException("PE-46",
	"\nconductor\n\n", "\nconductor\nhigh\n",
	`"prompt_sha256": "`+sha256Hex("")+`"`, `"prompt_sha256": "`+sha256Hex("high")+`"`,
	`"prompt_head": ""`, `"prompt_head": "high"`)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// testOpencodeBarePrompt asserts PE-8 for opencode-exec: with a trailing bare --prompt the JS
// runs opencode on the prompt "true", then crashes hashing it; orch refuses before the gate and
// never starts opencode.
func (h *harness) testOpencodeBarePrompt(t *testing.T) {
	js, orch := h.runPair(t, h.bridgeInvocation(t, opencodeBridge, bridgeCase{args: []string{"--prompt"}, script: map[string]string{"exit": "0\n"}}))
	if js.exitCode != 1 || !strings.HasSuffix(js.files["fakelog/opencode-argv.txt"], "\nconductor\ntrue\n") || !strings.Contains(js.stderr, "TypeError [ERR_INVALID_ARG_TYPE]: The \"data\" argument") {
		t.Errorf("js: exit %d, argv log %q, stderr:\n%s", js.exitCode, js.files["fakelog/opencode-argv.txt"], js.stderr)
	}
	if _, started := orch.files["fakelog/opencode-argv.txt"]; orch.exitCode != 2 || orch.stdout != "" || orch.stderr != "no prompt: --prompt needs a value\n" || started {
		t.Errorf("orch: exit %d, stdout %q, stderr %q, opencode started %v", orch.exitCode, orch.stdout, orch.stderr, started)
	}
}

// testOpencodeSplitSurrogate asserts PE-1 for opencode-exec's 400-unit prompt_head cut inside
// an emoji: the JS run record holds "\ud83d", orch's U+FFFD; the rest is identical.
func (h *harness) testOpencodeSplitSurrogate(t *testing.T) {
	const emoji, lone = "\U0001F600", `\ud83d`
	prompt := strings.Repeat("p", 399) + emoji
	js, orch := h.runPair(t, h.bridgeInvocation(t, opencodeBridge, bridgeCase{args: []string{"--prompt", prompt}, script: map[string]string{"exit": "0\n"}}))
	record := runRecordFile(t, js.files)
	if want := `"prompt_head": "` + strings.Repeat("p", 399) + lone + `"`; !strings.Contains(js.files[record], want) {
		t.Errorf("PE-1 not observed: js %s lacks %q:\n%s", record, want, js.files[record])
	}
	js.files[record] = strings.ReplaceAll(js.files[record], lone, "\ufffd")
	compareOutcomes(t, js, orch)
}
