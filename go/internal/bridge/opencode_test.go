package bridge

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

func parseOpencode(args ...string) (options, []string) {
	opts, positional := cliargs.ParseArgs(args, opencodeValueKeys, opencodeFlagKeys)
	return options(opts), positional
}

func TestOpencodeArgv(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"defaults", nil, []string{"run", "--format", "json", "--auto", "--agent", "conductor", "p"}},
		{"every flag, in the JS order", []string{"--model", "openai/gpt-6-luna", "--resume", "ses_1", "--agent", "coder"}, []string{"run", "--format", "json", "--auto", "--agent", "coder", "--session", "ses_1", "--model", "openai/gpt-6-luna", "p"}},
		{"empty values are falsy", []string{"--agent", "", "--resume", "", "--model", ""}, []string{"run", "--format", "json", "--auto", "--agent", "conductor", "p"}},
		{"bare agent is String(true)", []string{"--agent"}, []string{"run", "--format", "json", "--auto", "--agent", "true", "p"}},
		{"bare resume is String(true)", []string{"--resume"}, []string{"run", "--format", "json", "--auto", "--agent", "conductor", "--session", "true", "p"}},
		{"bare model is String(true)", []string{"--model"}, []string{"run", "--format", "json", "--auto", "--agent", "conductor", "--model", "true", "p"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, _ := parseOpencode(c.args...)
			if got := opencodeArgv(opts, "p"); !reflect.DeepEqual(got, c.want) {
				t.Errorf("argv\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// PE-46: --effort takes a value, which opencode-exec ignores; in the JS it was a flag and its
// value the prompt.
func TestOpencodeEffortTakesAValue(t *testing.T) {
	opts, positional := parseOpencode("--effort", "high", "--help")
	if len(positional) != 0 || opts.JS("effort") != "high" || opts.JS("help") != true {
		t.Errorf("opts %v, positional %q", opts, positional)
	}
	if got := opencodeArgv(opts, "p"); slices.Contains(got, "high") {
		t.Errorf("argv %q passes the effort on", got)
	}
}

func TestNewOpencodeResultKeepsBareValues(t *testing.T) {
	opts, _ := parseOpencode("--agent", "a", "--resume")
	got := marshal(t, newOpencodeResult(opts, "/w", "t"))
	const want = `{"ok":false,"harness":"opencode","session_id":true,"agent":"a","cwd":"/w","started_at":"t","final_message":"","errors":[]}`
	if got != want {
		t.Errorf("result\n got %s\nwant %s", got, want)
	}
}

// replayOpencode feeds input to a fresh opencode event handler the way opencode's stdout
// reaches it and returns the handler and the log.
func replayOpencode(t *testing.T, input string, args ...string) (*opencodeEvents, string) {
	t.Helper()
	var log bytes.Buffer
	opts, _ := parseOpencode(args...)
	events := newOpencodeEvents(&logger{w: &log, prefix: "[opencode] ", quiet: jsjson.Truthy(opts.JS("quiet"))}, newOpencodeResult(opts, "/w", "t"))
	lines := &procs.Lines{OnLine: events.handleLine}
	if _, err := lines.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if err := lines.Close(); err != nil {
		t.Fatal(err)
	}
	return events, log.String()
}

// Each expectation follows the JS line handler (l.29-35) under Node 26.
func TestOpencodeEvents(t *testing.T) {
	text := func(s string) string { return `{"part":{"type":"text","text":"` + s + `"}}` + "\n" }
	cases := []struct {
		name, input, wantLog, wantCrash, wantSession, wantText, wantErrors string
		args                                                               []string
	}{
		{name: "unparsable lines and primitives are skipped", input: "not json\n5\n\"s\"\n[1]\n{}\n" + `{"part":5}` + "\n" + `{"part":null}` + "\n" + `{"part":"text"}` + "\n", wantSession: "null"},
		{name: "null event", input: text("before") + "null\n" + text("after"), wantLog: "[opencode] message: before\n", wantCrash: "TypeError: Cannot read properties of null (reading 'sessionID')"},
		{name: "the first truthy sessionID wins, of any type", input: `{"sessionID":""}` + "\n" + `{"sessionID":0}` + "\n" + `{"sessionID":["s"]}` + "\n" + `{"sessionID":"later"}` + "\n", wantSession: `["s"]`},
		{name: "a resume id blocks the sessionID", args: []string{"--resume", "r"}, input: `{"sessionID":"s"}` + "\n", wantSession: `"r"`},
		{name: "the last truthy text part wins, cut at 160 units with only LF replaced",
			input:   text("first") + text("") + `{"part":{"type":"text"}}` + "\n" + text("a\\nb\\rc"+strings.Repeat("😀", 80)),
			wantLog: "[opencode] message: first\n[opencode] message: a b\rc" + strings.Repeat("😀", 77) + "�\n", wantSession: "null", wantText: "a\nb\rc" + strings.Repeat("😀", 80)},
		{name: "tool parts are logged, other parts are not",
			input:   `{"part":{"type":"tool","tool":"bash"}}` + "\n" + `{"part":{"type":"tool"}}` + "\n" + `{"part":{"type":"tool","tool":{"a":1},"text":"t"}}` + "\n" + `{"part":{"type":"reasoning","text":"r"}}` + "\n",
			wantLog: "[opencode] tool: bash\n[opencode] tool: undefined\n[opencode] tool: [object Object]\n", wantSession: "null"},
		{name: "text that is a number", input: `{"part":{"type":"text","text":5}}` + "\n", wantCrash: "TypeError: p.text.slice is not a function"},
		{name: "text that is an array", input: `{"part":{"type":"text","text":["a"]}}` + "\n", wantCrash: "TypeError: p.text.slice(...).replace is not a function"},
		{name: "text that is an object crashes under --quiet too", args: []string{"--quiet"}, input: text("quiet") + `{"part":{"type":"text","text":{}}}` + "\n", wantCrash: "TypeError: p.text.slice is not a function"},
		// PE-44: the JS ignored error events.
		{name: "error events push data.message, else message, else the error or the event",
			input: `{"type":"error","sessionID":"s","error":{"type":"unknown","message":"ChatGPT usage limit reached"}}` + "\n" +
				`{"type":"error","error":{"name":"APIError","data":{"message":"Rate limited"},"message":"outer"}}` + "\n" +
				`{"type":"error","error":{"type":"unknown"}}` + "\n" + `{"type":"error"}` + "\n",
			wantLog: "[opencode] error: ChatGPT usage limit reached\n[opencode] error: Rate limited\n" +
				"[opencode] error: {\"type\":\"unknown\"}\n[opencode] error: {\"type\":\"error\"}\n",
			wantSession: `"s"`,
			wantErrors:  `["ChatGPT usage limit reached","Rate limited","{\"type\":\"unknown\"}","{\"type\":\"error\"}"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events, log := replayOpencode(t, c.input, c.args...)
			if c.wantCrash != "" {
				if events.crash == nil || crashLine(events.crash) != c.wantCrash {
					t.Errorf("crash %v, want %q", events.crash, c.wantCrash)
				}
				return
			}
			if events.crash != nil {
				t.Fatalf("unexpected crash %v", events.crash)
			}
			if log != c.wantLog {
				t.Errorf("log\n got %q\nwant %q", log, c.wantLog)
			}
			if got := marshal(t, jsjson.Path(events.result, "session_id")); got != c.wantSession {
				t.Errorf("session_id %s, want %s", got, c.wantSession)
			}
			if events.text != c.wantText {
				t.Errorf("text %q, want %q", events.text, c.wantText)
			}
			if got := marshal(t, jsjson.Path(events.result, "errors")); c.wantErrors != "" && got != c.wantErrors {
				t.Errorf("errors %s, want %s", got, c.wantErrors)
			}
		})
	}
}

func TestOpencodeResultOfEveryExitPath(t *testing.T) {
	input := `{"sessionID":"s","part":{"type":"text","text":"answer"}}` + "\n"
	cases := []struct {
		name     string
		events   string // lines after input
		exitCode any
		stderr   string
		want     string
	}{
		{"ok", "", 0.0, "noise", `{"ok":true,"harness":"opencode","session_id":"s","agent":"conductor","cwd":"/w","started_at":"t","final_message":"answer","errors":[],"exit_code":0,"duration_ms":1500}`},
		{"a non-zero exit pushes the bare stderr tail", "", 3.0, "a\nb\nc\nd\n", `{"ok":false,"harness":"opencode","session_id":"s","agent":"conductor","cwd":"/w","started_at":"t","final_message":"answer","errors":["b | c | d"],"exit_code":3,"duration_ms":1500}`},
		{"an empty stderr pushes an empty string", "", 1.0, " \n", `{"ok":false,"harness":"opencode","session_id":"s","agent":"conductor","cwd":"/w","started_at":"t","final_message":"answer","errors":[""],"exit_code":1,"duration_ms":1500}`},
		{"killed by a signal", "", nil, "", `{"ok":false,"harness":"opencode","session_id":"s","agent":"conductor","cwd":"/w","started_at":"t","final_message":"answer","errors":[""],"exit_code":null,"duration_ms":1500}`},
		{"an error event replaces the stderr tail (PE-44)", `{"type":"error","error":{"message":"limit"}}` + "\n", 1.0, "tail", `{"ok":false,"harness":"opencode","session_id":"s","agent":"conductor","cwd":"/w","started_at":"t","final_message":"answer","errors":["limit"],"exit_code":1,"duration_ms":1500}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events, _ := replayOpencode(t, input+c.events)
			events.handleClose(c.exitCode, []byte(c.stderr), 1500*time.Millisecond)
			if got := marshal(t, events.result); got != c.want {
				t.Errorf("result\n got %s\nwant %s", got, c.want)
			}
		})
	}
}
