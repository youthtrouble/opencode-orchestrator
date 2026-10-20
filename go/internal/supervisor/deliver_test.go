package supervisor

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

func status(n int) *int { return &n }

// deliverJSON delivers text through the channel given as JSON and returns the result as JSON.
func (f *fixture) deliverJSON(channel string) string {
	f.t.Helper()
	value, err := jsjson.Unmarshal([]byte(channel))
	if err != nil {
		f.t.Fatal(err)
	}
	return compact(f.t, f.sup.deliverChannel(value, "wake up"))
}

func wantJSON(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestDeliverCodexQueue(t *testing.T) {
	cases := []struct {
		name   string
		result SyncResult
		want   string
	}{
		{"exit 0", SyncResult{Status: status(0)}, `{"status":"queued"}`},
		{"exit 1", SyncResult{Status: status(1)}, `{"status":"delivery_failed","error":"exit 1"}`},
		{"timed out", SyncResult{TimedOut: true}, `{"status":"delivery_uncertain","error":"timeout"}`},
		// Node checks status === 0 before the timeout: a child that exits 0 on SIGTERM queued it.
		{"timed out, exit 0", SyncResult{Status: status(0), TimedOut: true}, `{"status":"queued"}`},
		{"timed out, exit 3", SyncResult{Status: status(3), TimedOut: true}, `{"status":"delivery_uncertain","error":"timeout"}`},
		{"not started", SyncResult{Err: &procs.StartError{Name: "codex", Code: "ENOENT"}}, `{"status":"delivery_failed","error":"exit null"}`},
		{"signalled", SyncResult{}, `{"status":"delivery_failed","error":"exit null"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.runResult = c.result
			wantJSON(t, f.deliverJSON(`{"type":"codex-queue","thread":"th-1"}`), c.want)
			want := []SyncSpec{{Name: "codex", Args: []string{"queue", "--thread", "th-1", "--message", "wake up"}, Dir: f.root, Timeout: 30 * time.Second}}
			if !reflect.DeepEqual(f.runs, want) {
				t.Errorf("runs %+v, want %+v", f.runs, want)
			}
		})
	}
}

func TestDeliverStringifiesChannelValues(t *testing.T) {
	for channel, thread := range map[string]string{
		`{"type":"codex-queue"}`:                           "undefined",
		`{"type":"codex-queue","thread":null}`:             "null",
		`{"type":"codex-queue","thread":5}`:                "5",
		`{"type":"codex-queue","thread":{}}`:               "[object Object]",
		`{"type":"codex-queue","thread":["a\u0000b","c"]}`: "a", // String() then the C argv ends at NUL
	} {
		f := newFixture(t)
		f.runResult = SyncResult{Status: status(0)}
		f.deliverJSON(channel)
		if len(f.runs) != 1 || f.runs[0].Args[2] != thread {
			t.Errorf("%s: runs %+v, want thread %q", channel, f.runs, thread)
		}
	}
}

func TestDeliverOpencodeRunSpawnsAndQueues(t *testing.T) {
	for _, spawnErr := range []error{nil, &procs.StartError{Name: "opencode", Code: "ENOENT"}} {
		f := newFixture(t)
		f.spawnErr = spawnErr // PE-17: a missing opencode is still queued, and nothing else
		wantJSON(t, f.deliverJSON(`{"type":"opencode-run","session":"ses-1"}`), `{"status":"queued"}`)
		want := []spawnCall{{"opencode", []string{"run", "--session", "ses-1", "--format", "json", "wake up"}, f.root}}
		if !reflect.DeepEqual(f.spawns, want) || len(f.runs) != 0 {
			t.Errorf("spawns %+v, runs %+v", f.spawns, f.runs)
		}
	}
}

func TestDeliverClaudeResumeSelfExecsClaudeExec(t *testing.T) {
	for channel, mode := range map[string]string{
		`{"type":"claude-resume","session":"s"}`:                          "auto",
		`{"type":"claude-resume","session":"s","permission_mode":""}`:     "auto",
		`{"type":"claude-resume","session":"s","permission_mode":"plan"}`: "plan",
		`{"type":"claude-resume","session":"s","permission_mode":7}`:      "7",
	} {
		f := newFixture(t)
		wantJSON(t, f.deliverJSON(channel), `{"status":"queued"}`)
		want := []spawnCall{{"/bin/orch", []string{"claude-exec", "--cwd", f.root, "--resume", "s", "--permission-mode", mode, "--quiet", "--prompt", "wake up"}, f.root}}
		if !reflect.DeepEqual(f.spawns, want) {
			t.Errorf("%s: spawns %+v, want %+v", channel, f.spawns, want)
		}
	}
}

func TestDeliverCommand(t *testing.T) {
	f := newFixture(t)
	f.runResult = SyncResult{Status: status(0)}
	wantJSON(t, f.deliverJSON(`{"type":"command","argv":["notify","-t",5,null]}`), `{"status":"queued"}`)
	want := []SyncSpec{{Name: "notify", Args: []string{"-t", "5", "null", "wake up"}, Dir: f.root, Timeout: 30 * time.Second}}
	if !reflect.DeepEqual(f.runs, want) {
		t.Errorf("runs %+v, want %+v", f.runs, want)
	}
	for _, c := range []struct {
		result SyncResult
		want   string
	}{
		{SyncResult{Status: status(2)}, `{"status":"delivery_failed","error":"exit 2"}`},
		{SyncResult{TimedOut: true}, `{"status":"delivery_failed","error":"exit null"}`}, // not uncertain
		{SyncResult{Err: &procs.StartError{Name: "notify", Code: "ENOENT"}}, `{"status":"delivery_failed","error":"exit null"}`},
	} {
		f.runResult = c.result
		wantJSON(t, f.deliverJSON(`{"type":"command","argv":["notify"]}`), c.want)
	}
}

// TestDeliverCommandArgvShapes pins the results Node v26.5.1 gives for each argv shape (the
// probe ran the same expressions in a sandbox).
func TestDeliverCommandArgvShapes(t *testing.T) {
	failed := func(text string) string { return `{"status":"delivery_failed","error":"` + text + `"}` }
	notIterable := failed("TypeError: channel.argv.slice is not a function or its return value is not iterable")
	cases := []struct {
		argv     string
		want     string
		wantName string
		wantArgs []string
	}{
		{`{"type":"command"}`, failed("TypeError: Cannot read properties of undefined (reading '0')"), "", nil},
		{`{"type":"command","argv":null}`, failed("TypeError: Cannot read properties of null (reading '0')"), "", nil},
		{`{"type":"command","argv":{"0":"/bin/echo"}}`, notIterable, "", nil},
		{`{"type":"command","argv":5}`, notIterable, "", nil},
		{`{"type":"command","argv":true}`, notIterable, "", nil},
		{`{"type":"command","argv":"abc"}`, `{"status":"queued"}`, "a", []string{"b", "c", "wake up"}},
		{`{"type":"command","argv":"a😀"}`, `{"status":"queued"}`, "a", []string{"😀", "wake up"}},
		{`{"type":"command","argv":""}`, failed("ERR_INVALID_ARG_TYPE"), "", nil},
		{`{"type":"command","argv":[]}`, failed("ERR_INVALID_ARG_TYPE"), "", nil},
		{`{"type":"command","argv":[5]}`, failed("ERR_INVALID_ARG_TYPE"), "", nil},
		{`{"type":"command","argv":[null]}`, failed("ERR_INVALID_ARG_TYPE"), "", nil},
		{`{"type":"command","argv":[""]}`, failed("ERR_INVALID_ARG_VALUE"), "", nil},
		{`{"type":"command","argv":["/bin/ec\u0000ho"]}`, failed("ERR_INVALID_ARG_VALUE"), "", nil},
		{`{"type":"command","argv":["", "a\u0000"]}`, failed("ERR_INVALID_ARG_VALUE"), "", nil},
		{`{"type":"command","argv":[5, "a\u0000"]}`, failed("ERR_INVALID_ARG_TYPE"), "", nil},
		{`{"type":"command","argv":["/bin/echo","a\u0000b"]}`, failed("ERR_INVALID_ARG_VALUE"), "", nil},
		{`{"type":"command","argv":["/bin/echo",["a\u0000"],{},true,[1,2],""]}`, `{"status":"queued"}`, "/bin/echo", []string{"a", "[object Object]", "true", "1,2", "", "wake up"}},
	}
	for _, c := range cases {
		f := newFixture(t)
		f.runResult = SyncResult{Status: status(0)}
		wantJSON(t, f.deliverJSON(c.argv), c.want)
		switch {
		case c.wantName == "" && len(f.runs) != 0:
			t.Errorf("%s: ran %+v", c.argv, f.runs)
		case c.wantName != "" && (len(f.runs) != 1 || f.runs[0].Name != c.wantName || !reflect.DeepEqual(f.runs[0].Args, c.wantArgs)):
			t.Errorf("%s: runs %+v, want %q %q", c.argv, f.runs, c.wantName, c.wantArgs)
		}
	}
}

func TestDeliverRejectsANulArgumentOfEverySpawn(t *testing.T) {
	for _, channel := range []string{
		`{"type":"codex-queue","thread":"a\u0000"}`,
		`{"type":"opencode-run","session":"a\u0000"}`,
		`{"type":"claude-resume","session":"a\u0000"}`,
		`{"type":"claude-resume","session":"s","permission_mode":"a\u0000"}`,
	} {
		f := newFixture(t)
		wantJSON(t, f.deliverJSON(channel), `{"status":"delivery_failed","error":"ERR_INVALID_ARG_VALUE"}`)
		if len(f.runs)+len(f.spawns) != 0 {
			t.Errorf("%s: spawned %+v %+v", channel, f.runs, f.spawns)
		}
	}
}

func TestDeliverUnknownChannels(t *testing.T) {
	for channel, want := range map[string]string{
		`{"type":"nope"}`:  "unknown channel nope",
		`{}`:               "unknown channel undefined",
		`{"type":null}`:    "unknown channel null",
		`5`:                "unknown channel undefined",
		`{"type":["a",1]}`: "unknown channel a,1",
	} {
		f := newFixture(t)
		wantJSON(t, f.deliverJSON(channel), `{"status":"delivery_failed","error":"`+want+`"}`)
	}
}

// TestTickWakeStatusRule: the event is queued when any channel queued it or the mailbox took
// it, else uncertain when any channel may have it, else failed with a bug incident; the
// channel results keep the key order type, status, error, and omit an undefined type.
func TestTickWakeStatusRule(t *testing.T) {
	cases := []struct {
		name, wake  string
		noMailbox   bool
		result      SyncResult
		wantStatus  string
		wantResults string
	}{
		{"mailbox only", `[{"type":"command","argv":["x"]}]`, false, SyncResult{Status: status(1)}, "queued",
			`[{"type":"command","status":"delivery_failed","error":"exit 1"}]`},
		{"queued channel", `[{"argv":["x"]},{"type":"opencode-run","session":"s"}]`, true, SyncResult{}, "queued",
			`[{"status":"delivery_failed","error":"unknown channel undefined"},{"type":"opencode-run","status":"queued"}]`},
		{"uncertain", `[{"type":"codex-queue","thread":"t"},{"type":"x"}]`, true, SyncResult{TimedOut: true}, "delivery_uncertain",
			`[{"type":"codex-queue","status":"delivery_uncertain","error":"timeout"},{"type":"x","status":"delivery_failed","error":"unknown channel x"}]`},
		{"failed", `[{"type":"command","argv":["x"]}]`, true, SyncResult{TimedOut: true}, "delivery_failed",
			`[{"type":"command","status":"delivery_failed","error":"exit null"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			f.worker("b1", `"status": "finished", "started_at": "`+iso(-50)+`"`)
			f.write("supervisor.json", `{"wake": `+c.wake+`}`)
			if c.noMailbox {
				f.write("mailbox", "a file where the mailbox directory should be")
			}
			f.runResult = c.result
			state := f.mustTick(`{}`)
			event := jsjson.Path(state, "events", "run-b1:finished")
			if got := jsjson.Path(event, "status"); got != c.wantStatus {
				t.Errorf("status %v, want %s", got, c.wantStatus)
			}
			wantJSON(t, compact(t, jsjson.Path(event, "channels")), c.wantResults)
			bugs := 0
			for _, d := range f.incidents {
				if d.Kind == "bug" {
					bugs++
					if d.Summary != "wake delivery failed for finished" || d.Detail != c.wantResults {
						t.Errorf("incident %+v", d)
					}
				}
			}
			if wantBugs := map[bool]int{true: 1}[c.wantStatus == "delivery_failed"]; bugs != wantBugs {
				t.Errorf("%d bug incidents, want %d", bugs, wantBugs)
			}
			if log := f.read("logs/supervisor.log"); !strings.HasSuffix(log, " run-b1:finished "+c.wantStatus+" "+c.wantResults+"\n") {
				t.Errorf("log %q", log)
			}
		})
	}
}
