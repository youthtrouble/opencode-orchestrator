package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

func parseCodex(args ...string) options {
	opts, _ := cliargs.ParseArgs(args, codexValueKeys, codexFlagKeys)
	return options(opts)
}

func codexRequestFor(t *testing.T, args ...string) codexRequest {
	t.Helper()
	request, err := newCodexRequest(parseCodex(args...))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestCodexArgv(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	head := []string{"exec", "--json", "--skip-git-repo-check", "-C", dir}
	effortOf := func(effort string) []string { return []string{"-c", `model_reasoning_effort="` + effort + `"`} }
	// approved is the default sandbox's prefix: --approve-for-me alone, no -s.
	approved := func(effort string, rest ...string) []string {
		return slices.Concat(head, []string{"--approve-for-me"}, effortOf(effort), rest)
	}
	sandboxed := func(sandbox, effort string, rest ...string) []string {
		return slices.Concat(head, []string{"-s", sandbox}, effortOf(effort), rest)
	}
	addDir := filepath.Join(filepath.Dir(dir), "x", "y")
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"defaults approve without -s", nil, approved("high", "-m", "gpt-6.1-sol", "-")},
		{"every flag, exec-only options first", []string{
			"--ephemeral", "--add-dir", "../x/./y", "--schema", "s.json", "--resume", "", "--sandbox", "read-only", "--effort", "low", "--model", "gpt-6-luna",
		}, sandboxed("read-only", "low", "--add-dir", addDir, "-m", "gpt-6-luna", "--output-schema", filepath.Join(dir, "s.json"), "--ephemeral", "-")},
		{"read-only is not approved", []string{"--sandbox", "read-only"}, sandboxed("read-only", "high", "-m", "gpt-6.1-sol", "-")},
		{"danger-full-access is not approved", []string{"--sandbox", "danger-full-access"}, sandboxed("danger-full-access", "high", "-m", "gpt-6.1-sol", "-")},
		{"resume replaces model, approval before resume", []string{"--resume", "abc", "--model", "m"}, approved("high", "resume", "abc", "-")},
		{"resume with add-dir and schema", []string{"--resume", "abc", "--add-dir", "../x/./y", "--schema", "s.json", "--ephemeral"},
			approved("high", "--add-dir", addDir, "resume", "abc", "--output-schema", filepath.Join(dir, "s.json"), "--ephemeral", "-")},
		{"empty values are falsy", []string{"--model", "", "--effort", "", "--sandbox", "", "--schema", "", "--add-dir", "", "--resume", ""}, approved("high", "-m", "gpt-6.1-sol", "-")},
		{"bare effort is String(true)", []string{"--effort"}, approved("true", "-m", "gpt-6.1-sol", "-")},
		{"bare sandbox is -s true without approval", []string{"--sandbox"}, sandboxed("true", "high", "-m", "gpt-6.1-sol", "-")},
		{"bare resume is String(true)", []string{"--resume"}, approved("high", "resume", "true", "-")},
		{"the sandbox is compared case-sensitively", []string{"--sandbox", "Workspace-Write"}, sandboxed("Workspace-Write", "high", "-m", "gpt-6.1-sol", "-")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := codexRequestFor(t, c.args...).argv()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("argv\n got %q\nwant %q", got, c.want)
			}
		})
	}
	for _, key := range []string{"schema", "add-dir"} {
		t.Run("bare "+key, func(t *testing.T) {
			_, err := codexRequestFor(t, "--"+key).argv()
			if err == nil || crashLine(err) != `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)` {
				t.Errorf("crash line %q", crashLine(err))
			}
		})
	}
}

// Both TypeErrors come before the budget gate.
func TestNewCodexRequestCrashes(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--model"}, "TypeError: model.includes is not a function"},
		{[]string{"--model", "m", "--cwd"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`},
	}
	for _, c := range cases {
		if _, err := newCodexRequest(parseCodex(c.args...)); err == nil || crashLine(err) != c.want {
			t.Errorf("%q: crash line %q, want %q", c.args, crashLine(err), c.want)
		}
	}
}

func TestIsFrontierCodexModelIsACaseSensitiveSubstring(t *testing.T) {
	for model, want := range map[string]bool{"gpt-6-astra": true, "astral": true, "gpt-6-Astra": false, "gpt-6.1-sol": false} {
		if got := isFrontierCodexModel(model); got != want {
			t.Errorf("isFrontierCodexModel(%q) = %v", model, got)
		}
	}
}

func TestCodexStartLine(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "start model=gpt-6.1-sol effort=high sandbox=workspace-write cwd=/w"},
		{[]string{"--resume", "abc", "--model", "m", "--effort", "low", "--sandbox", "read-only"}, "start model=(resumed) effort=low sandbox=read-only cwd=/w"},
		{[]string{"--sandbox"}, "start model=gpt-6.1-sol effort=high sandbox=true cwd=/w"},
	}
	for _, c := range cases {
		request := codexRequestFor(t, c.args...)
		request.cwd = "/w"
		if got := request.startLine(); got != c.want {
			t.Errorf("startLine(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

// replayCodex feeds input to a fresh codex event handler the way codex's stdout reaches it and
// returns the handler and the log.
func replayCodex(t *testing.T, input string, args ...string) (*codexEvents, string) {
	t.Helper()
	var log bytes.Buffer
	request := codexRequestFor(t, args...)
	request.cwd = "/w"
	events := newCodexEvents(&logger{w: &log, prefix: "[codex] ", quiet: jsjson.Truthy(request.opts.JS("quiet"))}, newCodexResult(request, "t"))
	lines := &procs.Lines{OnLine: events.handleLine}
	if _, err := lines.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if err := lines.Close(); err != nil {
		t.Fatal(err)
	}
	return events, log.String()
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Each expectation follows the JS line handler (l.58-75) under Node 26.
func TestCodexEvents(t *testing.T) {
	const head = `{"ok":false,"harness":"codex","thread_id":null,"model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t",`
	const headWithoutThread = `{"ok":false,"harness":"codex","model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t",`
	const empty = `"final_message":"","usage":null,"files_changed":[],"commands":[],"errors":[]}`
	item := func(fields string) string { return `{"type":"item.completed","item":{` + fields + `}}` + "\n" }
	cases := []struct {
		name, input, wantLog, wantCrash, wantJSON string
		args                                      []string
	}{
		{name: "unparsable lines, primitives and other types are skipped",
			input:    "not json\n5\n\"s\"\n[1]\ntrue\n{}\n" + `{"type":"turn.started"}` + "\n" + `{"type":"error","message":"top-level errors are ignored"}` + "\n" + `{"type":"item.completed","item":5}` + "\n" + `{"type":"item.completed"}` + "\n" + item(`"type":"todo_list"`),
			wantJSON: head + empty},
		{name: "null event", input: `{"type":"thread.started","thread_id":"t1"}` + "\nnull\n" + item(`"type":"error","message":"late"`),
			wantLog: "[codex] thread t1\n", wantCrash: "TypeError: Cannot read properties of null (reading 'type')"},
		{name: "thread.started replaces a resume id", args: []string{"--resume", "r"}, input: `{"type":"thread.started","thread_id":"t1"}` + "\n",
			wantLog: "[codex] thread t1\n", wantJSON: `{"ok":false,"harness":"codex","thread_id":"t1","model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t",` + empty},
		{name: "thread.started without an id makes thread_id undefined", input: `{"type":"thread.started"}` + "\n",
			wantLog: "[codex] thread undefined\n", wantJSON: headWithoutThread + empty},
		{name: "a later id returns to the third place", input: `{"type":"thread.started"}` + "\n" + `{"type":"thread.started","thread_id":["a",null]}` + "\n",
			wantLog: "[codex] thread undefined\n[codex] thread a,\n", wantJSON: `{"ok":false,"harness":"codex","thread_id":["a",null],"model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t",` + empty},
		{name: "agent messages: the last truthy text wins, cut at 160 units with only LF replaced",
			input:    item(`"type":"agent_message","text":"first"`) + item(`"type":"agent_message","text":""`) + item(`"type":"agent_message"`) + item(`"type":"agent_message","text":"a\nb\rc`+strings.Repeat("😀", 80)+`"`),
			wantLog:  "[codex] message: first\n[codex] message: \n[codex] message: \n[codex] message: a b\rc" + strings.Repeat("😀", 77) + "�\n",
			wantJSON: head + `"final_message":"a\nb\rc` + strings.Repeat("😀", 80) + `","usage":null,"files_changed":[],"commands":[],"errors":[]}`},
		{name: "agent message text that is a number", input: item(`"type":"agent_message","text":7`), wantCrash: `TypeError: (it.text || "").slice is not a function`},
		{name: "agent message text that is an array", input: item(`"type":"agent_message","text":["a"]`), wantCrash: `TypeError: (it.text || "").slice(...).replace is not a function`},
		{name: "commands keep present fields only",
			input:    item(`"type":"command_execution","command":"go test","exit_code":0`) + item(`"type":"command_execution","command":"x"`) + item(`"type":"command_execution","exit_code":null`) + item(`"type":"command_execution","command":["ls","-l"],"exit_code":2`) + item(`"type":"command_execution","command":"`+strings.Repeat("c", 130)+`","exit_code":1`),
			wantLog:  "[codex] ran: go test -> 0\n[codex] ran: x -> undefined\n[codex] ran:  -> null\n[codex] ran: ls,-l -> 2\n[codex] ran: " + strings.Repeat("c", 120) + " -> 1\n",
			wantJSON: head + `"final_message":"","usage":null,"files_changed":[],"commands":[{"command":"go test","exit_code":0},{"command":"x"},{"exit_code":null},{"command":["ls","-l"],"exit_code":2},{"command":"` + strings.Repeat("c", 130) + `","exit_code":1}],"errors":[]}`},
		{name: "command that is a number", input: item(`"type":"command_execution","command":5`), wantCrash: `TypeError: (it.command || "").slice is not a function`},
		{name: "file changes keep present fields only",
			input:    item(`"type":"file_change","changes":[{"path":"a.go","kind":"update"},{"kind":"add"},5,"s",{"path":{"x":1}}]`) + item(`"type":"file_change"`) + item(`"type":"file_change","changes":[]`),
			wantLog:  "[codex] files: a.go, , , , [object Object]\n[codex] files: \n[codex] files: \n",
			wantJSON: head + `"final_message":"","usage":null,"files_changed":[{"path":"a.go","kind":"update"},{"kind":"add"},{},{},{"path":{"x":1}}],"commands":[],"errors":[]}`},
		{name: "null change", input: item(`"type":"file_change","changes":[{"path":"a"},null]`), wantCrash: "TypeError: Cannot read properties of null (reading 'path')"},
		{name: "object changes are not iterable", input: item(`"type":"file_change","changes":{"path":"a"}`), wantCrash: "TypeError: object is not iterable (cannot read property Symbol(Symbol.iterator))"},
		{name: "number changes are not iterable", input: item(`"type":"file_change","changes":1e21`), wantCrash: "TypeError: number 1e+21 is not iterable (cannot read property Symbol(Symbol.iterator))"},
		{name: "boolean changes are not iterable", input: item(`"type":"file_change","changes":true`), wantCrash: "TypeError: boolean true is not iterable (cannot read property Symbol(Symbol.iterator))"},
		{name: "changes that are a string", input: item(`"type":"file_change","changes":"ab"`), wantCrash: "TypeError: (it.changes || []).map is not a function"},
		{name: "error items push their message, undefined as null",
			input:    item(`"type":"error","message":"boom"`) + item(`"type":"error"`) + item(`"type":"error","message":{"a":1}`),
			wantLog:  "[codex] error: boom\n[codex] error: undefined\n[codex] error: [object Object]\n",
			wantJSON: head + `"final_message":"","usage":null,"files_changed":[],"commands":[],"errors":["boom",null,{"a":1}]}`},
		{name: "reasoning is logged at 100 units", input: item(`"type":"reasoning","text":"`+strings.Repeat("r", 110)+`\nx"`) + item(`"type":"reasoning"`),
			wantLog: "[codex] thinking: " + strings.Repeat("r", 100) + "\n[codex] thinking: \n", wantJSON: head + empty},
		{name: "reasoning text that is an object", input: item(`"type":"reasoning","text":{"a":1}`), wantCrash: `TypeError: (it.text || "").slice is not a function`},
		{name: "turns: completed sets usage and ok, failed pushes and clears ok",
			input:    `{"type":"turn.completed","usage":{"input_tokens":3}}` + "\n" + `{"type":"turn.failed","error":{"message":"rate limited"}}` + "\n" + `{"type":"turn.completed","usage":0}` + "\n",
			wantJSON: `{"ok":true,"harness":"codex","thread_id":null,"model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t","final_message":"","usage":null,"files_changed":[],"commands":[],"errors":["rate limited"]}`},
		{name: "turn.failed without a message serializes the error or the event",
			input:    `{"type":"turn.failed","error":"boom"}` + "\n" + `{"type":"turn.failed"}` + "\n" + `{"type":"turn.failed","error":{"message":"","code":1.50}}` + "\n" + `{"type":"turn.failed","error":{"message":5}}` + "\n",
			wantJSON: head + `"final_message":"","usage":null,"files_changed":[],"commands":[],"errors":["\"boom\"","{\"type\":\"turn.failed\"}","{\"message\":\"\",\"code\":1.5}",5]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events, log := replayCodex(t, c.input, c.args...)
			if c.wantCrash != "" {
				if events.crash == nil || crashLine(events.crash) != c.wantCrash {
					t.Errorf("crash %v, want %q", events.crash, c.wantCrash)
				}
			} else if events.crash != nil {
				t.Errorf("unexpected crash %v", events.crash)
			}
			if log != c.wantLog {
				t.Errorf("log\n got %q\nwant %q", log, c.wantLog)
			}
			if c.wantJSON != "" {
				if got := marshal(t, events.result.JSON()); got != c.wantJSON {
					t.Errorf("result\n got %s\nwant %s", got, c.wantJSON)
				}
			}
		})
	}
}

// Crash checks run whether or not the line is logged: the JS builds the template first.
func TestCodexCrashesUnderQuiet(t *testing.T) {
	events, log := replayCodex(t, `{"type":"item.completed","item":{"type":"reasoning","text":"a"}}`+"\n"+`{"type":"item.completed","item":{"type":"agent_message","text":7}}`+"\n", "--quiet")
	if events.crash == nil || log != "" {
		t.Errorf("crash %v, log %q", events.crash, log)
	}
}

// closeCodex replays input, closes the run with exitCode and stderr and returns the result
// after the tool-host check, as RunCodex reports it.
func closeCodex(t *testing.T, input string, exitCode any, stderr string) *codexResult {
	t.Helper()
	events, _ := replayCodex(t, input)
	events.handleClose(exitCode, []byte(stderr), 1500*time.Millisecond)
	events.result.flagToolHostOutage(events.result.cwd)
	return events.result
}

func TestCodexResultOfEveryExitPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const head = `{"ok":false,"harness":"codex","thread_id":"t1","model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t",`
	thread := `{"type":"thread.started","thread_id":"t1"}` + "\n"
	completed := `{"type":"turn.completed","usage":{"n":1}}` + "\n"
	message := func(text string) string {
		return `{"type":"item.completed","item":{"type":"agent_message","text":"` + text + `"}}` + "\n"
	}
	cases := []struct {
		name, input string
		exitCode    any
		stderr      string
		want        string
	}{
		{"ok", thread + message("done") + completed, 0.0, "",
			`{"ok":true,"harness":"codex","thread_id":"t1","model":"gpt-6.1-sol","effort":"high","sandbox":"workspace-write","cwd":"/w","started_at":"t","final_message":"done","usage":{"n":1},"files_changed":[],"commands":[],"errors":[],"exit_code":0,"duration_ms":1500}`},
		{"exit without errors pushes the stderr tail", thread + completed, 2.0, "a\nb\nc\nd\n",
			head + `"final_message":"","usage":{"n":1},"files_changed":[],"commands":[],"errors":["codex exited 2: b | c | d"],"exit_code":2,"duration_ms":1500}`},
		{"exit with errors keeps them, without the prefix", thread + `{"type":"turn.failed","error":{"message":"bad"}}` + "\n", 1.0, "ignored",
			head + `"final_message":"","usage":null,"files_changed":[],"commands":[],"errors":["bad"],"exit_code":1,"duration_ms":1500}`},
		{"killed by a signal", thread, nil, "",
			head + `"final_message":"","usage":null,"files_changed":[],"commands":[],"errors":["codex exited null: "],"exit_code":null,"duration_ms":1500}`},
		{"exit 0 without a completed turn is not ok", thread, 0.0, "",
			head + `"final_message":"","usage":null,"files_changed":[],"commands":[],"errors":[],"exit_code":0,"duration_ms":1500}`},
		{"tool-host outage appends reason after duration_ms", thread + message("I could NOT RUN the tests: tool host unavailable") + completed, 0.0, "",
			head + `"final_message":"I could NOT RUN the tests: tool host unavailable","usage":{"n":1},"files_changed":[],"commands":[],"errors":["codex completed the turn without executing any tool; its message reports tool-host failure (retry after ~60s)"],"exit_code":0,"duration_ms":1500,"reason":"tool_host_unavailable"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := marshal(t, closeCodex(t, c.input, c.exitCode, c.stderr).JSON()); got != c.want {
				t.Errorf("result\n got %s\nwant %s", got, c.want)
			}
		})
	}
	incidents, err := os.ReadFile(filepath.Join(home, ".local", "state", "opencode", "orchestrator", "incidents.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"component":"bridge","kind":"crash","summary":"codex turn completed with zero tool calls; code-mode host timeout reported in prose","detail":"I could NOT RUN the tests: tool host unavailable","context":{"model":"gpt-6.1-sol","thread_id":"t1"},"project":"w"`; !strings.Contains(string(incidents), want) || strings.Count(string(incidents), "\n") != 1 {
		t.Errorf("incidents:\n%s", incidents)
	}
}

// The no-op incident leaves an undefined thread_id out of its context.
func TestCodexToolHostOutageWithoutThread(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	result := closeCodex(t, `{"type":"thread.started"}`+"\n"+`{"type":"item.completed","item":{"type":"agent_message","text":"no tool"}}`+"\n"+`{"type":"turn.completed"}`+"\n", 0.0, "")
	if result.reason != "tool_host_unavailable" {
		t.Fatalf("reason %q", result.reason)
	}
	incidents, err := os.ReadFile(filepath.Join(home, ".local", "state", "opencode", "orchestrator", "incidents.jsonl"))
	if err != nil || !strings.Contains(string(incidents), `"context":{"model":"gpt-6.1-sol"},`) {
		t.Errorf("incidents (%v):\n%s", err, incidents)
	}
}

// A command or a file change makes the turn a real one, whatever its message says.
func TestCodexToolHostOutageNeedsAnIdleTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ran := `{"type":"item.completed","item":{"type":"command_execution","command":"ls","exit_code":0}}` + "\n"
	message := `{"type":"item.completed","item":{"type":"agent_message","text":"could not run"}}` + "\n"
	if result := closeCodex(t, ran+message+`{"type":"turn.completed"}`+"\n", 0.0, ""); !result.ok || result.reason != "" {
		t.Errorf("ok %v reason %q", result.ok, result.reason)
	}
}

func TestToolHostFailureIgnoresCaseInASCIIOnly(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Timed out negotiating with the CODE-MODE host", true},
		{"I could not run it", true},
		{"Unable to Execute", true},
		{"unable to run", true},
		{"Tool host failed", true},
		{"tool calls unavailable", true},
		{"tool call failed", true},
		{"No tool was available", true},
		{"no tooling", true},
		{"tool hosts failed", false},
		{"could not Kun", false}, // no: Kelvin sign is not k, and the word is "run" anyway
		{"unable to walK", false},
		{"no tool", true},
		{"no tooℬ", false},
		{"", false},
	}
	for _, c := range cases {
		if got := matchesIgnoringCase(toolHostFailure, c.text); got != c.want {
			t.Errorf("NOOP_RE.test(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestTimeoutDelayDefaults(t *testing.T) {
	cases := []struct {
		value          any
		defaultMinutes float64
		want           time.Duration
	}{
		{nil, defaultCodexTimeoutMins, 60 * time.Minute},
		{"", defaultCodexTimeoutMins, 60 * time.Minute},
		{"0", defaultCodexTimeoutMins, time.Millisecond},
		{true, defaultCodexTimeoutMins, time.Minute},
		{nil, defaultOpencodeTimeoutMins, 120 * time.Minute},
		{"", defaultOpencodeTimeoutMins, 120 * time.Minute},
		{"-1", defaultOpencodeTimeoutMins, time.Millisecond},
		{"35792", defaultOpencodeTimeoutMins, time.Millisecond},
		{true, defaultOpencodeTimeoutMins, time.Minute},
	}
	for _, c := range cases {
		if got := timeoutDelay(c.value, c.defaultMinutes); got != c.want {
			t.Errorf("timeoutDelay(%#v, %v) = %v, want %v", c.value, c.defaultMinutes, got, c.want)
		}
	}
}
