package worker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// deadPid is the pid of a child that has exited and been reaped.
func deadPid(t *testing.T) float64 {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return float64(cmd.Process.Pid)
}

func parse(t *testing.T, text string) any {
	t.Helper()
	v, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return v
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The expected values are Node 26's process.kill(pid, 0) outcomes (plan go-s6.md section 2):
// EPERM counts as alive, and a pid Node refuses to coerce is not alive.
func TestAliveCoercesAsProcessKill(t *testing.T) {
	live := strconv.Itoa(os.Getpid())
	dead := strconv.FormatFloat(deadPid(t), 'f', -1, 64)
	for _, tc := range []struct {
		pid  string // JSON
		want bool
	}{
		{live, true},
		{dead, false},
		{`"` + live + `"`, true},
		{`" ` + dead + ` "`, false},
		{`[` + live + `]`, true},
		{`["` + live + `"]`, true},
		{`1`, true},     // EPERM: init belongs to root
		{`true`, true},  // pid 1
		{`-1`, true},    // kill(-1, 0): any process the caller may signal
		{`[]`, true},    // "" is 0: the caller's own process group
		{`"0"`, true},   // likewise
		{`0`, false},    // falsy
		{`""`, false},   // falsy
		{`null`, false}, // falsy
		{`false`, false},
		{`1.5`, false},
		{`3000000000`, false},
		{`{}`, false},
		{`"abc"`, false},
		{`[1,2]`, false},
		{`[true]`, false},
	} {
		if got := Alive(parse(t, tc.pid)); got != tc.want {
			t.Errorf("Alive(%s) = %v, want %v", tc.pid, got, tc.want)
		}
	}
	if Alive(nil) {
		t.Error("Alive(undefined) is true")
	}
}

// watch.mjs's alive() lacks the falsy guard: the falsy pids Node's check lets through signal
// the process group; null and undefined do not pass it.
func TestSignalableHasNoFalsyGuard(t *testing.T) {
	for _, tc := range []struct {
		pid  string // JSON
		want bool
	}{{`0`, true}, {`""`, true}, {`false`, true}, {`null`, false}, {`1.5`, false}} {
		if got := Signalable(parse(t, tc.pid)); got != tc.want {
			t.Errorf("Signalable(%s) = %v, want %v", tc.pid, got, tc.want)
		}
	}
	if Signalable(nil) {
		t.Error("Signalable(undefined) is true")
	}
}

func TestLanePathsInterpolateTheJSValue(t *testing.T) {
	p := NewProject("/p")
	for _, tc := range []struct {
		lane any
		want string
	}{
		{"a", "/p/.orchestrator/workers/a.json"},
		{true, "/p/.orchestrator/workers/true.json"},
		{nil, "/p/.orchestrator/workers/undefined.json"},
		{"../x", "/p/.orchestrator/x.json"}, // not sanitised, as in the JS
	} {
		if got := p.StatePath(tc.lane); got != tc.want {
			t.Errorf("StatePath(%v) = %q, want %q", tc.lane, got, tc.want)
		}
	}
	if got := p.LockPath(true); got != "/p/.orchestrator/workers/true.lock" {
		t.Errorf("LockPath(true) = %q", got)
	}
}

func TestReadStateParsesAnyValueAndNilOnFailure(t *testing.T) {
	p := NewProject(t.TempDir())
	writeFile(t, p.StatePath("n"), "5")
	writeFile(t, p.StatePath("bad"), "{")
	writeFile(t, p.StatePath("bom"), "\ufeff{}")
	if err := os.MkdirAll(p.StatePath("dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := p.ReadState("n"); got != 5.0 {
		t.Errorf("ReadState(n) = %#v", got)
	}
	for _, lane := range []string{"bad", "bom", "dir", "missing"} {
		if got := p.ReadState(lane); got != nil {
			t.Errorf("ReadState(%s) = %#v, want nil", lane, got)
		}
	}
}

// fixedClock returns successive instants one second apart, starting at a fixed time.
func fixedClock() func() time.Time {
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		at = at.Add(time.Second)
		return at
	}
}

func newTestState(p Project) *State {
	fields := jsjson.NewObject()
	fields.Set("lane", "a")
	fields.Set("status", "starting")
	fields.Set("pid", 42.0)
	return p.NewState(fields, fixedClock())
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func changes(pairs ...any) *jsjson.Object {
	o := jsjson.NewObject()
	for i := 0; i < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// The file bytes are JSON.stringify(state, null, 2) + "\n" at each step, as the JS writes them.
func TestPublishKeyOrderAcrossWrites(t *testing.T) {
	p := NewProject(t.TempDir())
	s := newTestState(p)
	file := p.StatePath("a")

	s.Publish(changes())
	want := "{\n  \"lane\": \"a\",\n  \"status\": \"starting\",\n  \"pid\": 42,\n  \"updated_at\": \"2026-10-19T12:00:01.000Z\"\n}\n"
	if got := readText(t, file); got != want {
		t.Errorf("first write:\n%s\nwant:\n%s", got, want)
	}

	s.Publish(changes("status", "running", "worker_pid", 7.0))
	want = "{\n  \"lane\": \"a\",\n  \"status\": \"running\",\n  \"pid\": 42,\n  \"updated_at\": \"2026-10-19T12:00:02.000Z\",\n  \"state_storage\": \"available\",\n  \"worker_pid\": 7\n}\n"
	if got := readText(t, file); got != want {
		t.Errorf("second write:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(file + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp left after a successful write: %v", err)
	}
}

func TestPublishFailureIsRecordedAndNeverFatal(t *testing.T) {
	p := NewProject(t.TempDir())
	s := newTestState(p)
	file := p.StatePath("a")
	if err := os.MkdirAll(file, 0o755); err != nil { // rename onto a directory fails
		t.Fatal(err)
	}

	s.Publish(changes("status", "running"))
	if got := marshal(t, s.Fields); got != `{"lane":"a","status":"running","pid":42,"updated_at":"2026-10-19T12:00:01.000Z","state_storage":"unavailable","last_state_write_error":"EISDIR"}` {
		t.Errorf("state after a failed write: %s", got)
	}
	if got := readText(t, file+".tmp"); !strings.Contains(got, `"status": "running"`) {
		t.Errorf(".tmp not left by the failed rename: %q", got)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	s.Publish(changes("status", "finished"))
	want := "{\n  \"lane\": \"a\",\n  \"status\": \"finished\",\n  \"pid\": 42,\n  \"updated_at\": \"2026-10-19T12:00:02.000Z\",\n  \"state_storage\": \"unavailable\",\n  \"last_state_write_error\": \"EISDIR\"\n}\n"
	if got := readText(t, file); got != want {
		t.Errorf("recovery write:\n%s\nwant:\n%s", got, want)
	}
	if got := marshal(t, s.Fields); !strings.Contains(got, `"state_storage":"available","last_state_write_error":"EISDIR"`) {
		t.Errorf("state after recovery keeps its key order and the stale error: %s", got)
	}
}

func TestPublishMkdirFailureRecordsTheCode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".orchestrator"), "a file")
	s := newTestState(NewProject(root))
	s.Publish(changes())
	if got, _ := s.Fields.Get("last_state_write_error"); got != "ENOTDIR" {
		t.Errorf("last_state_write_error = %v, want ENOTDIR", got)
	}
}

func TestPublishFileModeIsUmaskedDefault(t *testing.T) {
	dir := t.TempDir()
	reference := filepath.Join(dir, "reference")
	f, err := os.OpenFile(reference, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	p := NewProject(dir)
	newTestState(p).Publish(changes())
	want, _ := os.Stat(reference)
	got, err := os.Stat(p.StatePath("a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode().Perm() != want.Mode().Perm() {
		t.Errorf("mode %v, want %v (0666 less the umask)", got.Mode().Perm(), want.Mode().Perm())
	}
}

func TestStatusListsLanesInByteOrderAndSpreadsEachState(t *testing.T) {
	p := NewProject(t.TempDir())
	live := strconv.Itoa(os.Getpid())
	writeFile(t, p.StatePath("b"), `{"lane":"b","pid":`+live+`,"process_alive":"x","lock_present":"y","z":1}`)
	writeFile(t, p.LockPath("b"), live)
	writeFile(t, p.StatePath("a"), `"ab"`)
	writeFile(t, p.StatePath("10"), `5`)
	writeFile(t, p.StatePath("1"), `null`)
	writeFile(t, p.StatePath("2"), `[1,2]`)
	writeFile(t, filepath.Join(p.WorkersDir, "notes.txt"), "")
	if err := os.MkdirAll(p.StatePath("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := p.Status(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"lane":"1","status":"unknown","process_alive":false,"lock_present":false},` +
		`{"process_alive":false,"lock_present":false},` +
		`{"0":1,"1":2,"process_alive":false,"lock_present":false},` +
		`{"0":"a","1":"b","process_alive":false,"lock_present":false},` +
		`{"lane":"b","pid":` + live + `,"process_alive":true,"lock_present":true,"z":1},` +
		`{"lane":"x","status":"unknown","process_alive":false,"lock_present":false}]`
	if text := marshal(t, got); text != want {
		t.Errorf("status:\n%s\nwant:\n%s", text, want)
	}
}

func TestStatusOfOneLaneAndOfNoProject(t *testing.T) {
	p := NewProject(t.TempDir())
	got, err := p.Status(true)
	if err != nil {
		t.Fatal(err)
	}
	if text := marshal(t, got); text != `[{"lane":true,"status":"unknown","process_alive":false,"lock_present":false}]` {
		t.Errorf("status --lane: %s", text)
	}
	got, err = p.Status("")
	if err != nil || marshal(t, got) != `[]` {
		t.Errorf("status without workers: %v %v", got, err)
	}
}

func TestStatusOfAFileWorkersDirIsNodesScandirError(t *testing.T) {
	p := NewProject(t.TempDir())
	writeFile(t, p.WorkersDir, "")
	_, err := p.Status(nil)
	want := "ENOTDIR: not a directory, scandir '" + p.WorkersDir + "'"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

type signal struct {
	pid int
	sig syscall.Signal
}

// recordingKiller records every signal and fails those whose pid is in fail with ESRCH.
func recordingKiller(sent *[]signal, fail ...int) func(int, syscall.Signal) error {
	return func(pid int, sig syscall.Signal) error {
		*sent = append(*sent, signal{pid, sig})
		for _, f := range fail {
			if f == pid {
				return syscall.ESRCH
			}
		}
		return nil
	}
}

func TestStopSignalsTheGroupThenThePid(t *testing.T) {
	live := os.Getpid()
	for _, tc := range []struct {
		name string
		fail []int
		want []signal
	}{
		{"group leader", nil, []signal{{-live, syscall.SIGINT}}},
		{"not a group leader", []int{-live}, []signal{{-live, syscall.SIGINT}, {live, syscall.SIGINT}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []signal
			p := NewProject(t.TempDir())
			p.kill = recordingKiller(&sent, tc.fail...)
			writeFile(t, p.StatePath("w"), `{"pid":"`+strconv.Itoa(live)+`"}`)
			out, err := p.Stop("w")
			if err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, out); got != `{"lane":"w","stopped":true,"pid":"`+strconv.Itoa(live)+`"}` {
				t.Errorf("stop: %s", got)
			}
			if !equalSignals(sent, tc.want) {
				t.Errorf("signals %v, want %v", sent, tc.want)
			}
		})
	}
}

func TestStopFailingSignalIsTheKillError(t *testing.T) {
	var sent []signal
	live := os.Getpid()
	p := NewProject(t.TempDir())
	p.kill = recordingKiller(&sent, -live, live)
	writeFile(t, p.StatePath("w"), `{"pid":`+strconv.Itoa(live)+`}`)
	_, err := p.Stop("w")
	if err == nil || err.Error() != "kill ESRCH" {
		t.Errorf("err = %v, want kill ESRCH", err)
	}
}

func TestStopWithoutALiveWorker(t *testing.T) {
	dead := strconv.FormatFloat(deadPid(t), 'f', -1, 64)
	for _, tc := range []struct {
		name, lane, state, want string
		undefinedLane           bool
	}{
		{name: "no state", lane: "w", want: `{"lane":"w","stopped":false,"reason":"no live worker"}`},
		{name: "dead pid", lane: "w", state: `{"pid":` + dead + `}`, want: `{"lane":"w","stopped":false,"reason":"no live worker"}`},
		{name: "falsy state", lane: "w", state: `0`, want: `{"lane":"w","stopped":false,"reason":"no live worker"}`},
		{name: "undefined lane", undefinedLane: true, state: `{"pid":` + dead + `}`, want: `{"stopped":false,"reason":"no live worker"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []signal
			p := NewProject(t.TempDir())
			p.kill = recordingKiller(&sent)
			var lane any = tc.lane
			if tc.undefinedLane {
				lane = nil
			}
			if tc.state != "" {
				writeFile(t, p.StatePath(lane), tc.state)
			}
			out, err := p.Stop(lane)
			if err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, out); got != tc.want {
				t.Errorf("stop: %s, want %s", got, tc.want)
			}
			if len(sent) != 0 {
				t.Errorf("signals sent: %v", sent)
			}
		})
	}
}

// PE-14: pids that are alive to the JS yet would make it signal every process of the user
// (kill(-1)), its own group (kill(0)) or an unrelated process (a negative pid) are refused.
func TestStopRefusesPidsBelowTwo(t *testing.T) {
	for _, pid := range []string{`1`, `true`, `"1"`, `[1]`, `-1`, `-12345`, `"0"`, `[]`} {
		t.Run(pid, func(t *testing.T) {
			var sent []signal
			p := NewProject(t.TempDir())
			p.kill = recordingKiller(&sent)
			writeFile(t, p.StatePath("w"), `{"pid":`+pid+`}`)
			out, err := p.Stop("w")
			if err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, out); got != `{"lane":"w","stopped":false,"reason":"no live worker"}` {
				t.Errorf("stop: %s", got)
			}
			if len(sent) != 0 {
				t.Errorf("signals sent: %v", sent)
			}
		})
	}
}

func equalSignals(a, b []signal) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
