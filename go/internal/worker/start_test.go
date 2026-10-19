package worker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// opts builds the parsed options of a command line: "--key value" pairs, a "--key" followed
// by another key or by nothing is the JS true.
func opts(args ...string) cliargs.Opts {
	o := cliargs.Opts{}
	for i := 0; i < len(args); i++ {
		key := strings.TrimPrefix(args[i], "--")
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			o[key] = cliargs.Opt{Value: args[i+1]}
			i++
		} else {
			o[key] = cliargs.Opt{Flag: true}
		}
	}
	return o
}

// testProject is a project in a temp directory (its physical path, as Node's process.cwd()
// gives it) holding the brief b.md.
func testProject(t *testing.T, brief string) Project {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "b.md"), brief)
	return NewProject(root)
}

// fakeRun configures testdata/bridge.sh through its FAKE_* variables and logs into a temp
// directory; HOME is a temp directory too, so incidents stay in the test.
type fakeRun struct {
	log    string
	stdout bytes.Buffer
	stderr bytes.Buffer
	// notified receives the channel the worker's SIGINT and SIGTERM are delivered on.
	notified chan chan<- os.Signal
}

func newFakeRun(t *testing.T, env map[string]string) *fakeRun {
	t.Helper()
	f := &fakeRun{log: t.TempDir(), notified: make(chan chan<- os.Signal, 1)}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKE_LOG", f.log)
	for _, key := range []string{"FAKE_NO_STDIN", "FAKE_WAIT", "FAKE_STDERR", "FAKE_STDOUT", "FAKE_EXIT"} {
		t.Setenv(key, env[key])
	}
	return f
}

func (f *fakeRun) runtime(t *testing.T) Runtime {
	t.Helper()
	self, err := filepath.Abs("testdata/bridge.sh")
	if err != nil {
		t.Fatal(err)
	}
	return Runtime{
		Self:   self,
		Pid:    4242, // only written into the lock and the state, never signalled
		Stdout: &f.stdout,
		Stderr: &f.stderr,
		Now:    fixedClock(),
		NewID:  func() string { return "run-uuid" },
		Notify: func(c chan<- os.Signal) { f.notified <- c },
	}
}

func (f *fakeRun) logged(t *testing.T, name string) string {
	t.Helper()
	return readText(t, filepath.Join(f.log, name))
}

func startForeground(t *testing.T, p Project, f *fakeRun, args ...string) (int, error) {
	t.Helper()
	launch, err := p.PrepareStart(opts(args...))
	if err != nil {
		t.Fatal(err)
	}
	return launch.Foreground(f.runtime(t))
}

// childPid is the bridge's pid in a state file, which differs per run.
var childPid = regexp.MustCompile(`"worker_pid": \d+`)

func stateFile(t *testing.T, p Project, lane string) string {
	t.Helper()
	return childPid.ReplaceAllString(readText(t, p.StatePath(lane)), `"worker_pid": <pid>`)
}

func requireNoLock(t *testing.T, p Project, lane string) {
	t.Helper()
	if _, err := os.Lstat(p.LockPath(lane)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock %s: %v, want it removed", p.LockPath(lane), err)
	}
}

const okResult = `{"ok":true,"session_id":"s-1","final_message":"done","record":"/r.json","usage":{"t":1},"cost_usd":0.5,"permission_denials":[1,2]}` + "\n"

func TestBridgeArgsPassTheOptionsAsTheJSDoes(t *testing.T) {
	p := NewProject("/p")
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"claude defaults", []string{"--harness", "claude"}, []string{"claude-exec", "--cwd", "/p", "--timeout-min", "120", "--permission-mode", "auto"}},
		{"claude every option", []string{"--harness", "claude", "--model", "m", "--effort", "e", "--resume", "r", "--force", "--agent", "a",
			"--permission-mode", "plan", "--allowed-tools", "Read", "--max-turns", "3", "--timeout-min", "5", "--sandbox", "s"},
			[]string{"claude-exec", "--cwd", "/p", "--timeout-min", "5", "--model", "m", "--effort", "e", "--resume", "r", "--force", "--agent", "a",
				"--permission-mode", "plan", "--allowed-tools", "Read", "--max-turns", "3"}},
		{"bare values are the text true", []string{"--harness", "claude", "--model", "--resume"},
			[]string{"claude-exec", "--cwd", "/p", "--timeout-min", "120", "--model", "true", "--resume", "true", "--permission-mode", "auto"}},
		{"empty values are left out", []string{"--harness", "claude", "--model", "", "--timeout-min", ""},
			[]string{"claude-exec", "--cwd", "/p", "--timeout-min", "120", "--permission-mode", "auto"}},
		{"codex sandbox, no agent", []string{"--harness", "codex", "--agent", "a", "--allowed-tools", "x"},
			[]string{"codex-exec", "--cwd", "/p", "--timeout-min", "120", "--sandbox", "workspace-write"}},
		{"codex read-only", []string{"--harness", "codex", "--sandbox", "read-only"},
			[]string{"codex-exec", "--cwd", "/p", "--timeout-min", "120", "--sandbox", "read-only"}},
		{"opencode takes --effort as well", []string{"--harness", "opencode", "--effort", "X", "--agent", "coder", "--sandbox", "s"},
			[]string{"opencode-exec", "--cwd", "/p", "--timeout-min", "120", "--effort", "X", "--agent", "coder"}},
	} {
		o := opts(tc.args...)
		harness, _ := o.Value("harness")
		l := &Launch{project: p, opts: o, harness: harness}
		if got := l.bridgeArgs(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: bridgeArgs\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestPrepareStartValidatesInTheJSOrder(t *testing.T) {
	p := testProject(t, "brief")
	writeFile(t, filepath.Join(p.Root, "b.txt"), "x")
	if err := os.Mkdir(filepath.Join(p.Root, "d.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string // the refusal, or the lane
	}{
		{[]string{"--brief", "absent.md", "--harness", "gemini"}, "brief must be an existing .md file"},
		{[]string{"--brief", "b.txt", "--harness", "claude"}, "brief must be an existing .md file"},
		{[]string{"--brief", "b.md", "--harness", "gemini"}, "harness must be codex, claude or opencode"},
		{[]string{"--brief", "b.md", "--harness"}, "harness must be codex, claude or opencode"},
		{[]string{"--brief", "b.md", "--harness", "claude"}, "lane b"},
		{[]string{"--brief", p.Root + "/b.md", "--harness", "claude", "--lane", ""}, "lane b"},
		{[]string{"--brief", "d.md/", "--harness", "codex"}, "lane d"},
		{[]string{"--brief", "b.md", "--harness", "opencode", "--lane", "x"}, "lane x"},
		{[]string{"--brief", "b.md", "--harness", "claude", "--lane"}, "lane true"},
	} {
		launch, err := p.PrepareStart(opts(tc.args...))
		var got string
		var refused *UsageError
		switch {
		case errors.As(err, &refused):
			got = refused.Message
		case err != nil:
			t.Fatalf("%q: %v", tc.args, err)
		default:
			got = "lane " + jsjson.TemplateString(launch.lane)
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.args, got, tc.want)
		}
	}
	_, err := p.PrepareStart(opts("--harness", "claude", "--brief"))
	if err == nil || err.Error() != `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[1]" argument must be of type string. Received type boolean (true)` {
		t.Errorf("bare --brief: %v", err)
	}
}

func TestForegroundFinishedRun(t *testing.T) {
	p := testProject(t, "do the thing\n")
	f := newFakeRun(t, map[string]string{
		"FAKE_STDERR": "[claude] session 0123abcd-0000-4000-8000-0123456789ab model=m\n[claude] tool: Read x\n",
		"FAKE_STDOUT": "noise\n" + okResult,
	})
	code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "claude", "--breadth", "broad")
	if err != nil || code != 0 {
		t.Fatalf("Foreground = %d, %v; stderr %q", code, err, f.stderr.String())
	}
	if want := "[claude] session 0123abcd-0000-4000-8000-0123456789ab model=m\n[claude] tool: Read x\n" +
		`{"lane":"b","status":"finished","session_id":"s-1","exit_code":0}` + "\n"; f.stdout.String() != want {
		t.Errorf("stdout %q, want %q", f.stdout.String(), want)
	}
	const want = `{
  "lane": "b",
  "status": "finished",
  "pid": 4242,
  "harness": "claude",
  "brief": "b.md",
  "breadth": "broad",
  "run_id": "run-uuid",
  "session_id": "s-1",
  "started_at": "2026-10-19T12:00:01.000Z",
  "log_path": null,
  "model": null,
  "updated_at": "2026-10-19T12:00:07.000Z",
  "state_storage": "available",
  "worker_pid": <pid>,
  "last_progress_at": "2026-10-19T12:00:05.000Z",
  "last_tool": "Read",
  "exit_code": 0,
  "result_summary": "done",
  "record": "/r.json",
  "usage": {
    "t": 1
  },
  "cost_usd": 0.5,
  "permission_denials": 2
}
`
	if got := stateFile(t, p, "b"); got != want {
		t.Errorf("state file\n%s\nwant\n%s", got, want)
	}
	if got, want := f.logged(t, "argv"), "claude-exec\n--cwd\n"+p.Root+"\n--timeout-min\n120\n--permission-mode\nauto\n"; got != want {
		t.Errorf("bridge argv %q, want %q", got, want)
	}
	if got := f.logged(t, "stdin"); !strings.HasPrefix(got, "You are a worker in an orchestrated multi-agent project at "+p.Root+".\nFirst read b.md ") ||
		!strings.HasSuffix(got, "never touch another lane's files.\n\n--- BRIEF ---\ndo the thing\n") {
		t.Errorf("bridge stdin %q", got)
	}
	requireNoLock(t, p, "b")
}

func TestForegroundConductorPromptIsTheBriefAlone(t *testing.T) {
	p := testProject(t, "brief only")
	f := newFakeRun(t, map[string]string{"FAKE_STDOUT": okResult})
	if code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "opencode", "--lane", "conductor"); err != nil || code != 0 {
		t.Fatalf("Foreground = %d, %v", code, err)
	}
	if got := f.logged(t, "stdin"); got != "brief only" {
		t.Errorf("bridge stdin %q, want the brief alone", got)
	}
}

func TestForegroundStateKeyOrderPerBranch(t *testing.T) {
	head := func(status, sessionID string) string {
		return `{
  "lane": "b",
  "status": "` + status + `",
  "pid": 4242,
  "harness": "codex",
  "brief": "b.md",
  "breadth": "standard",
  "run_id": "run-uuid",
  "session_id": ` + sessionID + `,
  "started_at": "2026-10-19T12:00:01.000Z",
  "log_path": null,
  "model": null,
  "updated_at": "2026-10-19T12:00:04.000Z",
  "state_storage": "available",
  "worker_pid": <pid>,
`
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		wantCode int
		want     string
	}{
		{"failed without stderr", map[string]string{"FAKE_STDOUT": `{"ok":false,"errors":["e1",null,["a","b"]],"final_message":"m"}`, "FAKE_EXIT": "1"}, 1, head("failed", "null") + `  "exit_code": 1,
  "errors": [
    "e1",
    null,
    [
      "a",
      "b"
    ]
  ],
  "record": null,
  "result_summary": "m"
}
`},
		{"deferred without stderr", map[string]string{"FAKE_STDOUT": `{"ok":false,"reason":"budget_red","resets_at":"soon","note":"red"}`, "FAKE_EXIT": "75"}, 0, head("deferred", "null") + `  "deferred_reason": "budget_red",
  "resume_after": "soon",
  "result_summary": "red"
}
`},
		{"finished without a record", map[string]string{"FAKE_STDOUT": `{"ok":true,"thread_id":"t","permission_denials":"abc"}`}, 0, head("finished", `"t"`) + `  "exit_code": 0,
  "result_summary": "",
  "usage": null,
  "cost_usd": null,
  "permission_denials": 3
}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProject(t, "brief")
			f := newFakeRun(t, tc.env)
			code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "codex")
			if err != nil || code != tc.wantCode {
				t.Fatalf("Foreground = %d, %v; want %d", code, err, tc.wantCode)
			}
			if got := stateFile(t, p, "b"); got != tc.want {
				t.Errorf("state file\n%s\nwant\n%s", got, tc.want)
			}
			requireNoLock(t, p, "b")
		})
	}
}

// relayRun is a run publishing to a temp project whose relayed chunks are fed directly.
func relayRun(t *testing.T) (*run, *bytes.Buffer, Project) {
	t.Helper()
	p := NewProject(t.TempDir())
	out := &bytes.Buffer{}
	fields := jsjson.NewObject()
	fields.Set("lane", "r")
	fields.Set("session_id", nil)
	r := &run{rt: Runtime{Stdout: out, Now: fixedClock()}, state: p.NewState(fields, fixedClock())}
	return r, out, p
}

func relayChunks(r *run, chunks ...string) {
	for _, chunk := range chunks {
		_, _ = bridgeStderr{r}.Write([]byte(chunk))
	}
}

func TestRelayAppliesTheJSRulesPerChunk(t *testing.T) {
	const s1, s2 = "11111111-0000-4000-8000-000000000001", "22222222-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		name        string
		chunks      []string
		wantSession any
		wantTool    any // nil: no last_tool key
	}{
		{"first session in a chunk wins", []string{"thread " + s1 + " session " + s2}, s1, nil},
		{"a later chunk's session replaces it", []string{"session " + s1, "x", "thread " + s2}, s2, nil},
		{"leftmost tool", []string{"tool: Read then tool: Edit"}, nil, "Read"},
		{"ran first leaves no tool", []string{"ran: ls -> 0\ntool: Read"}, nil, nil},
		{"a later tool chunk replaces it", []string{"tool: Read", "tool: Bash_2"}, nil, "Bash_2"},
		{"a tool name is ASCII word characters", []string{"tool: é"}, nil, nil},
		{"a short id is no session", []string{"session 1234"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out, _ := relayRun(t)
			relayChunks(r, tc.chunks...)
			if got := out.String(); got != strings.Join(tc.chunks, "") {
				t.Errorf("relayed %q", got)
			}
			if got, _ := r.state.Fields.Get("session_id"); got != tc.wantSession {
				t.Errorf("session_id = %v, want %v", got, tc.wantSession)
			}
			if got, _ := r.state.Fields.Get("last_tool"); got != tc.wantTool {
				t.Errorf("last_tool = %v, want %v", got, tc.wantTool)
			}
		})
	}
}

func TestRelayPublishesProgressOnEveryChunk(t *testing.T) {
	r, _, _ := relayRun(t)
	relayChunks(r, "a", "b", "c")
	if got, _ := r.state.Fields.Get("last_progress_at"); got != "2026-10-19T12:00:03.000Z" {
		t.Errorf("last_progress_at = %v, want the third chunk's time", got)
	}
}

func TestRelayCarriesASplitCharacterAndReplacesInvalidBytes(t *testing.T) {
	r, out, _ := relayRun(t)
	smile := "\U0001F600"
	relayChunks(r, "a"+smile[:1], smile[1:3], smile[3:]+"b", "\xff\xfe!", "end\xe2\x82")
	r.mu.Lock()
	out.WriteString(r.relay.flush())
	r.mu.Unlock()
	if want := "a" + smile + "b��!end�"; out.String() != want {
		t.Errorf("relayed %q, want %q", out.String(), want)
	}
}

func TestParseResultIsTheLastLineAsJSON(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   string // JSON of the result; "null" for none
	}{
		{"", "null"},
		{"progress\n{\"ok\":true}\n \t\n", `{"ok":true}`},
		{"{\"a\":1}\n{\"b\":2}", `{"b":2}`},
		{"{\"a\":1}\nnot json", "null"},
		{"null\n", "null"},
		{"7", "7"},
		{`"s"`, `"s"`},
		{"[1,null]\r\n", "[1,null]"},
		{"  {\"ok\":false}  ", `{"ok":false}`},
	} {
		if got := marshal(t, parseResult(tc.output)); got != tc.want {
			t.Errorf("parseResult(%q) = %s, want %s", tc.output, got, tc.want)
		}
	}
}

// closingRun is a run of a started bridge whose close branch the test drives directly.
func closingRun(t *testing.T) (*run, Project) {
	t.Helper()
	p := testProject(t, "brief")
	t.Setenv("HOME", t.TempDir())
	launch, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	rt := Runtime{Pid: 4242, Stdout: out, Now: fixedClock(), NewID: func() string { return "id" }}
	r := &run{launch: launch, rt: rt, state: p.NewState(launch.initialState(rt), rt.Now)}
	r.finish("running", jsjson.NewObject())
	return r, p
}

func statusOf(r *run) any {
	status, _ := r.state.Fields.Get("status")
	return status
}

func readMailbox(t *testing.T, p Project) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.Root, ".orchestrator", "mailbox", "messages.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func incidents(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local", "state", "opencode", "orchestrator", "incidents.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The JS coercions of a result it does not expect, and where each exception leaves the run.
func TestCloseBranchesCoerceResultsAsTheJSDoes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     string
		wantErr    string // "" for none
		wantStatus string
		wantMail   bool
		wantKind   string // the worker incident's kind, "" for none
	}{
		{"finished with a numeric message throws before publishing", `{"ok":true,"final_message":7}`, `TypeError: (result.final_message || "").slice is not a function`, "running", false, ""},
		{"failed with an object message throws before publishing", `{"ok":false,"final_message":{}}`, `TypeError: ((intermediate value) || "").slice is not a function`, "running", false, ""},
		{"errors that are not an array skip the incident and throw at the mail", `{"ok":false,"errors":"rate limit"}`, `TypeError: ((intermediate value) || ["no result"]).join is not a function`, "failed", false, ""},
		{"a record that is not a string throws in path.relative", `{"ok":false,"errors":["x"],"record":5}`, `TypeError [ERR_INVALID_ARG_TYPE]: The "to" argument must be of type string. Received type number (5)`, "failed", false, "crash"},
		{"an array record is an instance of Array", `{"ok":false,"record":[1]}`, `TypeError [ERR_INVALID_ARG_TYPE]: The "to" argument must be of type string. Received an instance of Array`, "failed", false, "crash"},
		{"limit in the errors is a limit incident", `{"ok":false,"errors":["Usage LIMIT"],"record":"/elsewhere/r.json"}`, "", "failed", true, "limit"},
		{"case folding is ASCII only: a dotless i is no i", `{"ok":false,"errors":["l\u0131m\u0131t"]}`, "", "failed", true, "crash"},
		{"no result", `null`, "", "failed", true, "crash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := closingRun(t)
			err := r.closeWith(1.0, parseResult(tc.result))
			if got := ""; err != nil {
				got = err.Error()
				if got != tc.wantErr {
					t.Errorf("error %q, want %q", got, tc.wantErr)
				}
			} else if tc.wantErr != "" {
				t.Errorf("no error, want %q", tc.wantErr)
			}
			if got := statusOf(r); got != tc.wantStatus {
				t.Errorf("status %v, want %s", got, tc.wantStatus)
			}
			if got := readMailbox(t, p); (got != "") != tc.wantMail {
				t.Errorf("mailbox %q, want a message: %v", got, tc.wantMail)
			}
			got := incidents(t)
			if (got != "") != (tc.wantKind != "") || (tc.wantKind != "" && !strings.Contains(got, `"kind":"`+tc.wantKind+`"`)) {
				t.Errorf("incidents %q, want kind %q", got, tc.wantKind)
			}
		})
	}
}

func TestBlockedMessageNamesTheRecordRelativeToTheProject(t *testing.T) {
	r, p := closingRun(t)
	r.output = []byte("x" + strings.Repeat("é", 1600))
	if err := r.closeWith(nil, parseResult(`{"ok":false,"errors":["a",null,["b","c"]],"record":"`+p.Root+`/.orchestrator/runs/r.json"}`)); err != nil {
		t.Fatal(err)
	}
	if got := readMailbox(t, p); !strings.Contains(got, `"from":"b","to":"coordinator","kind":"blocked","subject":"b failed on claude","body":"a; ; b,c","refs":[".orchestrator/runs/r.json"]`) {
		t.Errorf("mailbox %q", got)
	}
	if got := incidents(t); !strings.Contains(got, `"summary":"worker lane b failed on claude: a; ; b,c","detail":"`+strings.Repeat("é", 1500)+`"`) ||
		!strings.Contains(got, `"context":{"harness":"claude","model":null,"exit_code":null}`) {
		t.Errorf("incident %q", got)
	}
}

func TestLengthOrZeroIsOptionalLength(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  any
	}{
		{`[1,2,3]`, 3.0}, {`"a😀"`, 3.0}, {`{"length":"x"}`, "x"}, {`{"length":null}`, 0.0},
		{`{}`, 0.0}, {`5`, 0.0}, {`true`, 0.0}, {`null`, 0.0},
	} {
		if got := lengthOrZero(parse(t, tc.value)); got != tc.want {
			t.Errorf("lengthOrZero(%s) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestCutSummary(t *testing.T) {
	long := strings.Repeat("a", 1999) + "\U0001F600"
	if got, _ := cutSummary(long, "m"); got != strings.Repeat("a", 1999)+"�" {
		t.Errorf("a cut inside a surrogate pair keeps U+FFFD (PE-1), got %q", got)
	}
	items := make([]any, 2100)
	if got, _ := cutSummary(items, "m"); len(got.([]any)) != 2000 {
		t.Errorf("an array keeps its first 2000 elements")
	}
	for _, falsy := range []any{nil, false, 0.0, ""} {
		if got, err := cutSummary(falsy, "m"); got != "" || err != nil {
			t.Errorf("cutSummary(%v) = %v, %v", falsy, got, err)
		}
	}
}

// From the close event on, the JS runs synchronously to process.exit, so a signal then never
// reaches its handler: the branch's state and final line stand.
func TestSignalAfterTheCloseChangesNothing(t *testing.T) {
	r, p := closingRun(t)
	r.output = []byte(`{"ok":true}` + "\n")
	zero := 0
	code, err := r.settle(&zero, nil)
	if err != nil {
		t.Fatal(err)
	}
	settled := readText(t, p.StatePath("b"))
	r.interrupt(syscall.SIGTERM)
	exit, err := r.report(code)
	if err != nil || exit != 0 {
		t.Errorf("report = %d, %v; want 0", exit, err)
	}
	if got := r.rt.Stdout.(*bytes.Buffer).String(); got != `{"lane":"b","status":"finished","session_id":null,"exit_code":0}`+"\n" {
		t.Errorf("final line %q", got)
	}
	r.interrupt(syscall.SIGINT)
	if after := readText(t, p.StatePath("b")); after != settled || !strings.Contains(after, `"status": "finished"`) {
		t.Errorf("a signal after the close changed the state:\n%s", after)
	}
}

// A signal during the run publishes "interrupted", reaches the bridge and its child through
// the group (never SIGKILL: the bridge ends on its own, with its own exit code), and the close
// branch then overwrites the status.
func TestSignalDuringTheRunReachesTheBridgeGroup(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			p := testProject(t, "brief")
			f := newFakeRun(t, map[string]string{"FAKE_WAIT": "1", "FAKE_STDOUT": `{"ok":false,"errors":["stopped"]}`, "FAKE_EXIT": "1"})
			var code int
			var err error
			done := make(chan struct{})
			go func() {
				defer close(done)
				code, err = startForeground(t, p, f, "--brief", "b.md", "--harness", "claude")
			}()
			signals := <-f.notified
			waitForFile(t, filepath.Join(f.log, "ready"))
			signals <- sig
			name := map[syscall.Signal]string{syscall.SIGTERM: "TERM", syscall.SIGINT: "INT"}[sig]
			waitFor(t, func() bool {
				logged, _ := os.ReadFile(filepath.Join(f.log, "signals"))
				return strings.Contains(string(logged), "child "+name) && strings.Contains(string(logged), "bridge "+name)
			}, "the bridge and its child to log "+name)
			if got := stateFile(t, p, "b"); !strings.Contains(got, `"status": "interrupted"`) {
				t.Errorf("state after the signal:\n%s", got)
			}
			writeFile(t, filepath.Join(f.log, "release"), "")
			<-done
			if err != nil || code != 1 {
				t.Fatalf("Foreground = %d, %v", code, err)
			}
			if got := stateFile(t, p, "b"); !strings.Contains(got, `"status": "failed"`) || !strings.Contains(got, `"exit_code": 1,`) {
				t.Errorf("the close branch did not overwrite the status, or the bridge was killed:\n%s", got)
			}
			close(signals)
		})
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	waitFor(t, func() bool { _, err := os.Stat(path); return err == nil }, path)
}

func waitFor(t *testing.T, ready func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// From the bridge's start on the lock is removed however the run ends; a crash before that
// (the brief is a directory) leaves the lock and the "starting" state, as in the JS.
func TestLockIsRemovedOnEveryPathAfterTheBridgeStarts(t *testing.T) {
	t.Run("C2 after the relay", func(t *testing.T) {
		p := testProject(t, "brief")
		f := newFakeRun(t, map[string]string{"FAKE_STDERR": "progress\n", "FAKE_STDOUT": `{"ok":true,"final_message":7}`})
		code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "claude")
		if code != 1 || err == nil || !strings.HasPrefix(err.Error(), "TypeError: ") {
			t.Errorf("Foreground = %d, %v; want the TypeError", code, err)
		}
		if f.stdout.String() != "progress\n" {
			t.Errorf("stdout %q, want the relay without a final line", f.stdout.String())
		}
		if got := stateFile(t, p, "b"); !strings.Contains(got, `"status": "running"`) {
			t.Errorf("state:\n%s", got)
		}
		requireNoLock(t, p, "b")
	})
	t.Run("a bridge that cannot start", func(t *testing.T) {
		p := testProject(t, "brief")
		f := newFakeRun(t, nil)
		launch, err := p.PrepareStart(opts("--brief", "b.md", "--harness", "claude"))
		if err != nil {
			t.Fatal(err)
		}
		rt := f.runtime(t)
		rt.Self = filepath.Join(t.TempDir(), "absent")
		if code, err := launch.Foreground(rt); code != 1 || err == nil {
			t.Errorf("Foreground = %d, %v; want a crash", code, err)
		}
		requireNoLock(t, p, "b")
	})
	t.Run("a brief that is a directory", func(t *testing.T) {
		p := testProject(t, "brief")
		if err := os.Mkdir(filepath.Join(p.Root, "d.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		f := newFakeRun(t, nil)
		code, err := startForeground(t, p, f, "--brief", "d.md", "--harness", "claude")
		if code != 1 || err == nil || err.Error() != "EISDIR: illegal operation on a directory, read" {
			t.Errorf("Foreground = %d, %v; want EISDIR", code, err)
		}
		if got := readText(t, p.LockPath("d")); got != "4242" {
			t.Errorf("lock %q, want it kept with the worker's pid", got)
		}
		if got := stateFile(t, p, "d"); !strings.Contains(got, `"status": "starting"`) {
			t.Errorf("state:\n%s", got)
		}
	})
}

// PE-13: a bridge that exits without reading its 1 MiB prompt does not crash the worker.
func TestUnreadPromptIsNotAnError(t *testing.T) {
	p := testProject(t, strings.Repeat("x", 1<<20))
	f := newFakeRun(t, map[string]string{"FAKE_NO_STDIN": "1", "FAKE_STDOUT": okResult})
	code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "opencode", "--effort", "X")
	if err != nil || code != 0 {
		t.Fatalf("Foreground = %d, %v", code, err)
	}
	if got := stateFile(t, p, "b"); !strings.Contains(got, `"status": "finished"`) {
		t.Errorf("state:\n%s", got)
	}
	requireNoLock(t, p, "b")
}

func TestLockedLaneIsRefused(t *testing.T) {
	p := testProject(t, "brief")
	writeFile(t, p.LockPath("b"), "1")
	writeFile(t, p.StatePath("b"), `{"pid":"x","status":"running","extra":1}`)
	f := newFakeRun(t, nil)
	code, err := startForeground(t, p, f, "--brief", "b.md", "--harness", "claude")
	if err != nil || code != ExitLaneLocked {
		t.Fatalf("Foreground = %d, %v", code, err)
	}
	want := `{"error":"lane locked","lane":"b","previous":{"status":"running","pid":"x","alive":false},"hint":"inspect worker state; remove the lock only after confirming the process is gone"}` + "\n"
	if f.stderr.String() != want || f.stdout.String() != "" {
		t.Errorf("stderr %q, stdout %q", f.stderr.String(), f.stdout.String())
	}
	if got := readText(t, p.LockPath("b")); got != "1" {
		t.Errorf("the lock was changed to %q", got)
	}
}

func TestPreviousWorkerKeepsAFalsyStateAsItIs(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{`null`, `null`}, {`0`, `0`}, {`""`, `""`}, {`false`, `false`},
		{`"abc"`, `{"alive":false}`}, {`[1]`, `{"alive":false}`},
		{`{"status":null}`, `{"status":null,"alive":false}`},
	} {
		if got := marshal(t, previousWorker(parse(t, tc.state), Alive)); got != tc.want {
			t.Errorf("previousWorker(%s) = %s, want %s", tc.state, got, tc.want)
		}
	}
}

// The relay, the bridge's stdout, the signal handler and the final line share the run's lock;
// -race checks it under concurrent chunks and signals.
func TestRunIsSafeUnderConcurrentEvents(t *testing.T) {
	r, p := closingRun(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			relayChunks(r, "tool: Read\n")
			_, _ = bridgeStdout{r}.Write([]byte("x"))
			r.interrupt(syscall.SIGTERM)
		}()
	}
	wg.Wait()
	if _, err := r.report(nil); err != nil {
		t.Fatal(err)
	}
	if got := stateFile(t, p, "b"); !strings.Contains(got, `"status": "interrupted"`) {
		t.Errorf("state:\n%s", got)
	}
}

// Node gives every errno an err.code; Code knows only its table's, so for another errno the
// state records an approximation of String(err) (S6a review F1).
func TestErrorCodeFallsBackToTheErrorText(t *testing.T) {
	if got := errorCode(syscall.EISDIR); got != "EISDIR" {
		t.Errorf("errorCode(EISDIR) = %q", got)
	}
	if got := errorCode(syscall.EDOM); got != "Error: "+syscall.EDOM.Error() {
		t.Errorf("errorCode(EDOM) = %q", got)
	}
}
