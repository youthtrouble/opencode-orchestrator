package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

func parseClaude(args ...string) options {
	opts, _ := cliargs.ParseArgs(args, claudeValueKeys, claudeFlagKeys)
	return options(opts)
}

var argvPrefix = []string{"-p", "--output-format", "stream-json", "--verbose"}

func TestClaudeArgv(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.WriteFile("schema.json", []byte("{\"type\":\"object\"}\n\xff"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"defaults", nil, []string{"--permission-mode", "auto", "--permission-prompts", "none", "--model", "claude-opus-5-5", "--", "p"}},
		{"every flag, in the JS order", []string{
			"--add-dir", "extra", "--json-schema", "schema.json", "--append-system-prompt", "be brief", "--agent", "reviewer",
			"--max-budget-usd", "1.5", "--max-turns", "3", "--disallowed-tools", "Bash", "--allowed-tools", "Read,Edit",
			"--effort", "high", "--model", "fable", "--permission-mode", "dontAsk",
		}, []string{
			"--permission-mode", "dontAsk", "--permission-prompts", "none", "--model", "fable", "--effort", "high",
			"--allowedTools", "Read,Edit", "--disallowedTools", "Bash", "--max-turns", "3", "--max-budget-usd", "1.5",
			"--agent", "reviewer", "--append-system-prompt", "be brief", "--json-schema", "{\"type\":\"object\"}\n\ufffd",
			"--add-dir", filepath.Join(dir, "extra"), "--", "p",
		}},
		{"resume replaces model", []string{"--resume", "abc", "--model", "haiku"}, []string{"--permission-mode", "auto", "--permission-prompts", "none", "--resume", "abc", "--", "p"}},
		{"empty values are falsy", []string{"--permission-mode", "", "--resume", "", "--effort", "", "--max-turns", ""}, []string{"--permission-mode", "auto", "--permission-prompts", "none", "--model", "claude-opus-5-5", "--", "p"}},
		{"bare resume is String(true)", []string{"--resume"}, []string{"--permission-mode", "auto", "--permission-prompts", "none", "--resume", "true", "--", "p"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := parseClaude(c.args...)
			got, err := claudeArgv(opts, opts.Or("model", defaultClaudeModel), "p")
			if err != nil {
				t.Fatal(err)
			}
			if want := append(append([]string{}, argvPrefix...), c.want...); !reflect.DeepEqual(got, want) {
				t.Errorf("argv\n got %q\nwant %q", got, want)
			}
		})
	}
	// A trailing bare key is true: Node's spawn passes it as "true".
	for _, key := range []string{"model", "effort", "permission-mode", "allowed-tools", "disallowed-tools", "max-turns", "max-budget-usd", "agent", "append-system-prompt"} {
		t.Run("bare "+key, func(t *testing.T) {
			opts := parseClaude("--" + key)
			got, err := claudeArgv(opts, opts.Or("model", defaultClaudeModel), "p")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(got, "\x00"), "\x00true\x00") {
				t.Errorf("argv %q has no \"true\" value for --%s", got, key)
			}
		})
	}
	crashes := []struct {
		name string
		args []string
		want string
	}{
		{"bare json-schema", []string{"--json-schema"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`},
		{"bare add-dir", []string{"--add-dir"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`},
		{"missing schema", []string{"--json-schema", "absent.json"}, "Error: ENOENT: no such file or directory, open '" + filepath.Join(dir, "absent.json") + "'"},
		{"schema is a directory", []string{"--json-schema", "."}, "Error: EISDIR: illegal operation on a directory, read"},
	}
	for _, c := range crashes {
		t.Run(c.name, func(t *testing.T) {
			_, err := claudeArgv(parseClaude(c.args...), defaultClaudeModel, "p")
			if err == nil || crashLine(err) != c.want {
				t.Errorf("crash line %q, want %q", crashLine(err), c.want)
			}
		})
	}
}

func TestStartLine(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "start model=claude-opus-5-5 mode=acceptEdits cwd=/w"},
		{[]string{"--effort", "high", "--permission-mode", "plan", "--model", "haiku"}, "start model=haiku effort=high mode=plan cwd=/w"},
		{[]string{"--resume", "abc"}, "start model=(resumed) mode=acceptEdits cwd=/w"},
		{[]string{"--model"}, "start model=true mode=acceptEdits cwd=/w"},
		{[]string{"--permission-mode", "x", "--effort"}, "start model=claude-opus-5-5 effort=true mode=x cwd=/w"},
	}
	for _, c := range cases {
		opts := parseClaude(c.args...)
		if got := startLine(opts, opts.Or("model", defaultClaudeModel), "/w"); got != c.want {
			t.Errorf("startLine(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestNewClaudeResultKeepsBareValues(t *testing.T) {
	// Only the last key can be bare on a real command line; each is tested here at once.
	opts := options{"effort": {Flag: true}, "resume": {Flag: true}}
	result := newClaudeResult(opts, true, "/w", "2026-10-19T00:00:00.000Z")
	got, err := jsjson.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"ok":false,"harness":"claude","session_id":true,"model":true,"effort":true,"cwd":"/w","started_at":"2026-10-19T00:00:00.000Z","final_message":"","usage":null,"model_usage":null,"cost_usd":null,"num_turns":null,"permission_denials":[],"tools_used":{},"errors":[],"structured_output":null}`
	if string(got) != want {
		t.Errorf("result\n got %s\nwant %s", got, want)
	}
}

// replay feeds input to a fresh event handler the way claude's stdout reaches it and returns
// the result and the log.
func replay(t *testing.T, input []byte) (*claudeEvents, string) {
	t.Helper()
	var log bytes.Buffer
	events := newClaudeEvents(&logger{w: &log, prefix: "[claude] "}, newClaudeResult(parseClaude(), defaultClaudeModel, "/w", "t"))
	lines := &procs.Lines{OnLine: events.handleLine}
	if _, err := lines.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := lines.Close(); err != nil {
		t.Fatal(err)
	}
	return events, log.String()
}

func field(t *testing.T, events *claudeEvents, key string) string {
	t.Helper()
	data, err := jsjson.Marshal(jsjson.Path(events.result, key))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The expectations were produced by running the JS line handler (l.59-73) over each fixture.
func TestClaudeEventsOverFixtures(t *testing.T) {
	const session2, session1 = "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000001"
	cases := []struct {
		name, session, finalPrefix, tools, errors string
		ok                                        bool
		turns, denials                            int
		structured                                string
		logs                                      []string
	}{
		{"ok", session2, "Fixture heading", `{"Read":1}`, `[]`, true, 2, 0, "null", []string{
			"session " + session2 + " model=claude-haiku-5-5", "tool: Read /tmp/fixture/README.md", "message: Fixture heading"}},
		{"permission-denied", session2, "The Bash command `touch probe.txt` was denied", `{"Bash":1}`, `[]`, true, 2, 1, "null", []string{
			"session " + session2 + " model=claude-haiku-5-5", "tool: Bash touch probe.txt",
			"message: The Bash command `touch probe.txt` was denied because Claude Code is running in \"don't ask\" permission mode, so no file was created. I didn't try another tool t"}},
		{"max-turns", session1, "", `{"Read":1}`, `["error_max_turns"]`, false, 2, 0, "null", []string{
			"session " + session1 + " model=claude-haiku-5-5", "tool: Read /tmp/fixture/README.md"}},
		{"structured-output", session2, `{"word":"pong"}`, `{"StructuredOutput":1}`, `[]`, true, 3, 0, `{"word":"pong"}`, []string{
			"session " + session2 + " model=claude-haiku-5-5", "message: pong", "tool: StructuredOutput"}},
		{"bad-model", session2, "There's an issue with the selected model", `{}`, `["There's an issue with the selected model (claude-not-a-model-0-0). It may not exist or you may not have access to it. Run --model to pick a different model."]`, false, 1, 0, "null", []string{
			"session " + session2 + " model=claude-not-a-model-0-0",
			"message: There's an issue with the selected model (claude-not-a-model-0-0). It may not exist or you may not have access to it. Run --model to pick a different model."}},
		{"usage-limit", session2, "You've hit your session limit · resets 3am", `{}`, `["You've hit your session limit · resets 3am"]`, false, 1, 0, "null", []string{
			"session " + session2 + " model=claude-haiku-5-5"}},
		{"long-line", session2, "Fixture heading", `{"Read":1}`, `[]`, true, 2, 0, "null", []string{
			"session " + session2 + " model=claude-haiku-5-5", "tool: Read /tmp/fixture/README.md", "message: Fixture heading"}},
		{"success-is-error", session2, "boom: synthetic failure text", `{"Read":1}`, `["boom: synthetic failure text"]`, false, 2, 0, "null", []string{
			"session " + session2 + " model=claude-haiku-5-5", "tool: Read /tmp/fixture/README.md", "message: Fixture heading"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claude", "stream-json", c.name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			events, log := replay(t, input)
			if events.crash != nil {
				t.Fatalf("crash: %v", events.crash)
			}
			var wantLog strings.Builder
			for _, line := range c.logs {
				wantLog.WriteString("[claude] " + line + "\n")
			}
			if log != wantLog.String() {
				t.Errorf("log\n got %q\nwant %q", log, wantLog.String())
			}
			r := events.result
			finalMessage, _ := jsjson.Path(r, "final_message").(string)
			denials, _ := jsjson.Path(r, "permission_denials").([]any)
			if jsjson.Path(r, "ok") != c.ok || jsjson.Path(r, "session_id") != c.session || !strings.HasPrefix(finalMessage, c.finalPrefix) ||
				jsjson.Path(r, "num_turns") != float64(c.turns) || len(denials) != c.denials {
				t.Errorf("ok %v session %v final %q turns %v denials %d", jsjson.Path(r, "ok"), jsjson.Path(r, "session_id"), finalMessage, jsjson.Path(r, "num_turns"), len(denials))
			}
			if got := field(t, events, "tools_used"); got != c.tools {
				t.Errorf("tools_used %s, want %s", got, c.tools)
			}
			if got := field(t, events, "errors"); got != c.errors {
				t.Errorf("errors %s, want %s", got, c.errors)
			}
			if got := field(t, events, "structured_output"); got != c.structured {
				t.Errorf("structured_output %s, want %s", got, c.structured)
			}
		})
	}
}

// Each expectation was checked against the JS line handler under Node 26.
func TestClaudeEdgeEvents(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantLog   string
		wantCrash string
		key, want string // a result field and its JSON
	}{
		{name: "unparsable lines are skipped", input: "not json\n{\"session_id\":\"s\"} trailing\n  {\"session_id\":\"s2\"}  \n", key: "session_id", want: `"s2"`},
		{name: "null event", input: "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\nnull\n{\"session_id\":\"later\"}\n", wantLog: "[claude] session s model=undefined\n", wantCrash: "TypeError: Cannot read properties of null (reading 'session_id')"},
		{name: "primitives and arrays are ignored", input: "5\n\"str\"\ntrue\n[1]\n{}\n", key: "session_id", want: "null"},
		{name: "first truthy session id of any type wins", input: "{\"session_id\":0}\n{\"session_id\":[1]}\n{\"session_id\":\"s\"}\n", key: "session_id", want: "[1]"},
		{name: "init prints values as templates", input: `{"type":"system","subtype":"init","session_id":{"a":1},"model":["x","y"]}` + "\n", wantLog: "[claude] session [object Object] model=x,y\n"},
		{name: "string content iterates characters", input: `{"type":"assistant","message":{"content":"text"}}` + "\n", key: "tools_used", want: "{}"},
		{name: "missing content", input: `{"type":"assistant","message":"m"}` + "\n" + `{"type":"assistant"}` + "\n", key: "tools_used", want: "{}"},
		{name: "number content is not iterable", input: `{"type":"assistant","message":{"content":5}}` + "\n", wantCrash: "TypeError: ev.message?.content is not iterable"},
		{name: "object content is not iterable", input: `{"type":"assistant","message":{"content":{"type":"text"}}}` + "\n", wantCrash: "TypeError: ev.message?.content is not iterable"},
		{name: "null block", input: `{"type":"assistant","message":{"content":[{"type":"text","text":"a"},null]}}` + "\n", wantLog: "[claude] message: a\n", wantCrash: "TypeError: Cannot read properties of null (reading 'type')"},
		{name: "non-string text", input: `{"type":"assistant","message":{"content":[{"type":"text","text":["a"]}]}}` + "\n", wantCrash: "TypeError: block.text.slice(...).replace is not a function"},
		{name: "falsy text and other blocks are skipped", input: `{"type":"assistant","message":{"content":[{"type":"text","text":""},5,"s",[1],{"type":"thinking"}]}}` + "\n", key: "tools_used", want: "{}"},
		{name: "text is cut at 160 UTF-16 units and only LF becomes a space", input: `{"type":"assistant","message":{"content":[{"type":"text","text":"a\nb\rc` + strings.Repeat("😀", 80) + `"}]}}` + "\n", wantLog: "[claude] message: a b\rc" + strings.Repeat("😀", 77) + "\ufffd\n"},
		{name: "tool names are property keys", input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":5},{"type":"tool_use","name":null},{"type":"tool_use","name":"__proto__"},{"type":"tool_use"},{"type":"tool_use","name":{}},{"type":"tool_use","name":"toString"},{"type":"tool_use","name":"toString"},{"type":"tool_use","name":"constructor"},{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Read"}]}}` + "\n",
			key: "tools_used", want: `{"5":1,"null":1,"undefined":1,"[object Object]":1,"toString":"function toString() { [native code] }11","constructor":"function Object() { [native code] }1","Read":2}`},
		{name: "tool detail: command cut at 100, else file path", input: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + strings.Repeat("x", 120) + `","file_path":"f"}},{"type":"tool_use","name":"Read","input":{"command":"","file_path":{"a":1}}},{"type":"tool_use","name":"Bash","input":{"command":7}},{"type":"tool_use","name":"T","input":"s"}]}}` + "\n",
			wantLog: "[claude] tool: Bash " + strings.Repeat("x", 100) + "\n[claude] tool: Read [object Object]\n[claude] tool: Bash 7\n[claude] tool: T\n"},
		{name: "result fields", input: `{"type":"result","is_error":0,"result":["r"],"usage":0,"modelUsage":{"m":1},"total_cost_usd":0,"num_turns":false,"permission_denials":null,"structured_output":false}` + "\n",
			key: "", want: `{"ok":true,"final_message":["r"],"usage":null,"model_usage":{"m":1},"cost_usd":0,"num_turns":false,"permission_denials":[],"errors":[],"structured_output":false}`},
		{name: "several results: last wins, errors accumulate", input: `{"type":"result","is_error":true,"subtype":"error_a","num_turns":1}` + "\n" + `{"type":"result","is_error":1,"subtype":"success","result":"` + strings.Repeat("é", 210) + `"}` + "\n" + `{"type":"result","is_error":true,"result":[1,null]}` + "\n" + `{"type":"result","is_error":true}` + "\n",
			key: "errors", want: `["error_a","` + strings.Repeat("é", 200) + `",[1,null],"error"]`},
		{name: "is_error with subtype success reports the result text", input: `{"type":"result","is_error":true,"subtype":"success","result":"boom"}` + "\n", key: "errors", want: `["boom"]`},
		{name: "non-string result when the error is chosen", input: `{"type":"result","is_error":true,"result":5}` + "\n", wantCrash: `TypeError: (ev.result || "error").slice is not a function`},
		{name: "non-string result is fine without is_error", input: `{"type":"result","result":5}` + "\n", key: "final_message", want: "5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events, log := replay(t, []byte(c.input))
			if c.wantCrash != "" {
				if events.crash == nil || crashLine(events.crash) != c.wantCrash {
					t.Errorf("crash %v, want %q", events.crash, c.wantCrash)
				}
				select {
				case <-events.crashed:
				default:
					t.Error("crashed channel not closed")
				}
			} else if events.crash != nil {
				t.Errorf("unexpected crash %v", events.crash)
			}
			if c.wantLog != "" && log != c.wantLog {
				t.Errorf("log\n got %q\nwant %q", log, c.wantLog)
			}
			if c.want == "" {
				return
			}
			got := ""
			if c.key != "" {
				got = field(t, events, c.key)
			} else {
				subset := jsjson.NewObject()
				for _, key := range []string{"ok", "final_message", "usage", "model_usage", "cost_usd", "num_turns", "permission_denials", "errors", "structured_output"} {
					value, _ := events.result.Get(key)
					subset.Set(key, value)
				}
				data, err := jsjson.Marshal(subset)
				if err != nil {
					t.Fatal(err)
				}
				got = string(data)
			}
			if got != c.want {
				t.Errorf("%s\n got %s\nwant %s", c.key, got, c.want)
			}
		})
	}
}

// Once the JS would have died, nothing more is logged: neither events nor the timeout.
func TestClaudeEventsAfterCrashAreDropped(t *testing.T) {
	events, log := replay(t, []byte("null\n{\"type\":\"system\",\"subtype\":\"init\"}\n"))
	if log != "" {
		t.Errorf("log after crash: %q", log)
	}
	if events.logIfRunning("timeout; killing") {
		t.Error("timeout logged after the crash")
	}
}

func TestTimeoutDelayFollowsNodeClamp(t *testing.T) {
	const oneMs = time.Millisecond
	cases := []struct {
		value any
		want  time.Duration
	}{
		{nil, 60 * time.Minute},
		{"", 60 * time.Minute},
		{"0", oneMs},
		{"abc", oneMs},
		{" 2 ", 2 * time.Minute},
		{"0x10", 16 * time.Minute},
		{"Infinity", oneMs},
		{"-1", oneMs},
		{"0.5", 30 * time.Second},
		{"35791", 35791 * time.Minute},
		{"35792", oneMs},
		{true, time.Minute},
	}
	for _, c := range cases {
		if got := timeoutDelay(c.value, 60); got != c.want {
			t.Errorf("timeoutDelay(%#v) = %v, want %v", c.value, got, c.want)
		}
	}
}

func TestStderrTail(t *testing.T) {
	cases := []struct{ stderr, want string }{
		{"", ""},
		{"one\n", "one"},
		{"\u00a0\ufeff one\ntwo \n\t", "one | two"},
		{"1\n2\n3\n4\n5\n", "3 | 4 | 5"},
		{"a\r\nb\r\nc\r\n", "a\r | b\r | c"},
		{"x\n\n\ny\n", " |  | y"},
		{"bad \xff\n", "bad \ufffd"},
	}
	for _, c := range cases {
		if got := stderrTail([]byte(c.stderr)); got != c.want {
			t.Errorf("stderrTail(%q) = %q, want %q", c.stderr, got, c.want)
		}
	}
}

func TestLimitPatternsIgnoreCaseInASCIIOnly(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"You've HIT YOUR Session LIMIT · resets 3am", true},
		{"usage limit reached", true},
		{"Rate Limit", true},
		{"hit your weekly limit", true},
		{"hit your wee\u212aly limit", false}, // Kelvin sign: Go's (?i) would match it, JS /i does not
		{"hit your monthly limit", false},
	}
	for _, c := range cases {
		if got := matchesIgnoringCase(limitText, c.text); got != c.want {
			t.Errorf("LIMIT_TEXT.test(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	if !matchesIgnoringCase(fable, "claude-FABLE-5-1") || matchesIgnoringCase(fable, "true") {
		t.Error("fable test")
	}
}
