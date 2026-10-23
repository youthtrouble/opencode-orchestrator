package supervisor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"os"
)

// evaluate runs one flip evaluation over state (JSON) with config (JSON) at the fixture's
// clock, as the end of a tick does.
func (f *fixture) evaluate(state, config string) (*jsjson.Object, error) {
	f.t.Helper()
	parse := func(text string) any {
		value, err := jsjson.Unmarshal([]byte(text))
		if err != nil {
			f.t.Fatal(err)
		}
		return value
	}
	object := parse(state).(*jsjson.Object)
	now := unixSeconds(f.now)
	t := &tick{config: parse(config), now: now, nowISO: iso(now - t0)}
	return object, f.sup.evaluateFlip(object, t)
}

func (f *fixture) mustEvaluate(state string) *jsjson.Object {
	f.t.Helper()
	object, err := f.evaluate(state, `{}`)
	if err != nil {
		f.t.Fatalf("evaluateFlip: %v", err)
	}
	return object
}

// decides makes `flip decide` print stdout and every `flip run` print "  launched\n".
func (f *fixture) decides(stdout string) {
	f.respond = func(spec SyncSpec) SyncResult {
		if len(spec.Args) > 1 && spec.Args[1] == "decide" {
			return SyncResult{Status: status(0), Stdout: stdout}
		}
		return SyncResult{Status: status(0), Stdout: "  launched\n"}
	}
}

// launches lists the `flip run` calls.
func (f *fixture) launches() []SyncSpec {
	var runs []SyncSpec
	for _, spec := range f.runs {
		if spec.Args[1] == "run" {
			runs = append(runs, spec)
		}
	}
	return runs
}

// logText is the supervisor log, "" when there is none.
func (f *fixture) logText() string {
	data, _ := os.ReadFile(f.path("logs/supervisor.log"))
	return string(data)
}

const deadConductor = `{"harness": "opencode", "status": "finished", "pid": 9, "session_id": "ses-o", "started_at": "2027-01-15T08:00:00.000Z"}`

func TestFlipEvaluationNeedsAConductorLaneOrAFlipFile(t *testing.T) {
	f := newFixture(t)
	state := f.mustEvaluate(`{"a": 1}`)
	wantJSON(t, compact(t, state), `{"a":1}`)
	if len(f.runs) != 0 {
		t.Errorf("ran %+v", f.runs)
	}
}

func TestFlipEvaluationRunsOnceAMinute(t *testing.T) {
	cases := []struct {
		last string
		runs bool
	}{
		{`0`, true}, {`null`, true}, {`""`, true}, {`false`, true},
		{jsjson.TemplateString(float64(t0 - 60)), true},
		{jsjson.TemplateString(float64(t0 - 59)), false},
		{`"` + jsjson.TemplateString(float64(t0-60)) + `"`, true}, // a numeric string is a number
		{`"abc"`, false}, // NaN: never again
		{`{}`, false},
		{`[1]`, true},
	}
	for _, c := range cases {
		f := newFixture(t)
		f.write("flip.json", `{}`)
		f.decides("")
		state := f.mustEvaluate(`{"last_flip_check": ` + c.last + `}`)
		got := compact(t, jsjson.Path(state, "last_flip_check"))
		if ran := len(f.runs) == 1; ran != c.runs || (ran && got != jsjson.TemplateString(float64(t0))) || (!ran && got != compact(t, mustParse(t, c.last))) {
			t.Errorf("last_flip_check %s: ran %v, now %s", c.last, ran, got)
		}
	}
}

func mustParse(t *testing.T, text string) any {
	t.Helper()
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestFlipActiveDoor observes the active door through `flip decide --active`.
func TestFlipActiveDoor(t *testing.T) {
	cases := []struct {
		name, lane, flip, config string
		aliveCalls               []bool
		want                     string
	}{
		{"flip.to", ``, `{"to": "claude"}`, `{}`, nil, "claude"},
		{"flip.to any value", ``, `{"to": 5}`, `{}`, nil, "5"},
		{"config.home", ``, `{"to": ""}`, `{"home": "claude"}`, nil, "claude"},
		{"default", ``, `{}`, `{"home": 0}`, nil, "opencode"},
		{"default without flip.json", `{"status": "failed"}`, ``, `{}`, []bool{false}, "opencode"},
		// The lane is alive when the door is chosen and gone when idleness is checked.
		{"live claude lane", `{"harness": "claude", "status": "running", "pid": 9}`, `{"to": "opencode"}`, `{}`, []bool{true, false}, "claude"},
		{"live other lane", `{"harness": "codex", "status": "running", "pid": 9}`, `{"to": "claude"}`, `{}`, []bool{true, false}, "opencode"},
		{"ended lane", `{"harness": "claude", "status": "deferred", "pid": 9}`, `{"to": "x"}`, `{}`, []bool{false}, "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.lane != "" {
				f.write("workers/conductor.json", c.lane)
			}
			if c.flip != "" {
				f.write("flip.json", c.flip)
			}
			calls := c.aliveCalls
			f.sup.Alive = func(any) bool {
				if len(calls) == 0 {
					t.Fatal("unexpected alive() call")
				}
				alive := calls[0]
				calls = calls[1:]
				return alive
			}
			f.decides("")
			if _, err := f.evaluate(`{}`, c.config); err != nil {
				t.Fatal(err)
			}
			want := []SyncSpec{{Name: "/bin/orch", Args: []string{"flip", "decide", "--active", c.want, "--cwd", f.root}}}
			if !reflect.DeepEqual(f.runs, want) {
				t.Errorf("runs %+v, want %+v", f.runs, want)
			}
		})
	}
}

func TestFlipActiveDoorReadsANullConfigOnlyWhenNeeded(t *testing.T) {
	f := newFixture(t)
	f.write("flip.json", `{}`)
	state, err := f.evaluate(`{}`, `null`)
	wantError(t, err, "Cannot read properties of null (reading 'home')")
	wantJSON(t, compact(t, state), `{"last_flip_check":1800000000}`)

	f = newFixture(t)
	f.write("flip.json", `{"to": "claude"}`)
	f.decides("")
	if _, err := f.evaluate(`{}`, `null`); err != nil {
		t.Fatalf("flip.to set: config.home is not read, got %v", err)
	}
}

func TestFlipAwaitingHumanIsLoggedOnceAndRearmedWhenCleared(t *testing.T) {
	f := newFixture(t)
	f.mkdir("logs")
	f.write("flip.json", `{"to": "claude", "awaiting_human": {"reason": "push?"}}`)
	f.write("workers/conductor.json", deadConductor)
	state := f.mustEvaluate(`{}`)
	wantJSON(t, compact(t, state), `{"last_flip_check":1800000000,"awaiting_human_logged":true}`)
	wantLog := iso(0) + " conductor awaiting human: push?; no relaunch until cleared\n"
	if got := f.logText(); got != wantLog {
		t.Errorf("log %q, want %q", got, wantLog)
	}

	f.at(60)
	state = f.mustEvaluate(compact(t, state))
	if got := f.logText(); got != wantLog {
		t.Errorf("second evaluation logged again: %q", got)
	}
	f.write("flip.json", `{"awaiting_human": true}`)
	f.at(120)
	f.mustEvaluate(`{"awaiting_human_logged": false}`)
	if got := f.logText(); got != wantLog+iso(120)+" conductor awaiting human: undefined; no relaunch until cleared\n" {
		t.Errorf("log %q", got)
	}
	if len(f.runs) != 0 {
		t.Errorf("ran %+v while awaiting a human", f.runs)
	}

	f.write("flip.json", `{"awaiting_human": null}`)
	f.decides("")
	f.at(180)
	state = f.mustEvaluate(`{"awaiting_human_logged": true, "z": 1}`)
	wantJSON(t, compact(t, state), `{"awaiting_human_logged":false,"z":1,"last_flip_check":1800000180}`)
}

func TestFlipAwaitingHumanWithoutLogsAborts(t *testing.T) {
	f := newFixture(t)
	f.write("flip.json", `{"awaiting_human": {"reason": "r"}}`)
	state, err := f.evaluate(`{}`, `{}`)
	if err == nil || errorCode(err) != "ENOENT" {
		t.Fatalf("error %v, want ENOENT", err)
	}
	wantJSON(t, compact(t, state), `{"last_flip_check":1800000000}`)
}

// neverStarted is a finished opencode conductor lane and a later flip to claude.
func neverStarted(f *fixture) {
	f.mkdir("logs")
	f.write("workers/conductor.json", deadConductor)
	f.write("flip.json", `{"to": "claude", "at": "2027-01-15T09:00:00.000Z"}`)
	f.decides("")
}

func TestFlipTargetNeverStartedLaunchesIt(t *testing.T) {
	f := newFixture(t)
	neverStarted(f)
	state := f.mustEvaluate(`{"last_relaunch_at": ` + jsjson.TemplateString(float64(t0-120)) + `}`)
	want := []SyncSpec{{Name: "/bin/orch", Args: []string{"flip", "run", "--unless-gated", "--to", "claude", "--from", "opencode", "--reason", "flip target never started", "--cwd", f.root}, Dir: f.root}}
	if !reflect.DeepEqual(f.runs, want) {
		t.Errorf("runs %+v, want %+v", f.runs, want)
	}
	if got := f.logText(); got != iso(0)+" flip target never started: launching claude: launched\n" {
		t.Errorf("log %q", got)
	}
	wantJSON(t, compact(t, state), `{"last_relaunch_at":1800000000,"last_flip_check":1800000000}`)
}

func TestFlipTargetNeverStartedNeedsEveryCondition(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		state string
	}{
		{"pending launch set", func(f *fixture) {
			f.write("flip.json", `{"to": "claude", "at": "2027-01-15T09:00:00.000Z", "pending_launch": {"to": "claude"}}`)
		}, `{}`},
		{"lane alive", func(f *fixture) { f.live[9] = true }, `{}`},
		{"lock held", func(f *fixture) { f.holdLock("workers/conductor.lock") }, `{}`},
		{"no lane", func(f *fixture) { f.write("workers/conductor.json", `null`) }, `{}`},
		{"same door", func(f *fixture) {
			f.write("workers/conductor.json", strings.Replace(deadConductor, `"opencode"`, `"claude"`, 1))
		}, `{}`},
		{"lane deferred", func(f *fixture) {
			f.write("workers/conductor.json", strings.Replace(deadConductor, `"finished"`, `"deferred"`, 1))
		}, `{}`},
		{"flip before the lane started", func(f *fixture) {
			f.write("flip.json", `{"to": "claude", "at": "2027-01-15T07:59:59.999Z"}`)
		}, `{}`},
		{"flip date unparsable", func(f *fixture) { f.write("flip.json", `{"to": "claude", "at": "soon"}`) }, `{}`},
		{"flip date missing", func(f *fixture) { f.write("flip.json", `{"to": "claude"}`) }, `{}`},
		{"started_at unparsable", func(f *fixture) {
			f.write("workers/conductor.json", strings.Replace(deadConductor, `"2027-01-15T08:00:00.000Z"`, `5`, 1))
		}, `{}`},
		{"relaunched 119 s ago", func(f *fixture) {}, `{"last_relaunch_at": ` + jsjson.TemplateString(float64(t0-119)) + `}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			neverStarted(f)
			c.setup(f)
			f.mustEvaluate(c.state)
			for _, spec := range f.launches() {
				if spec.Args[8] == "flip target never started" {
					t.Errorf("launched %+v", spec)
				}
			}
		})
	}
}

func TestFlipPendingLaunch(t *testing.T) {
	cases := []struct {
		name, pending string
		wantArgs      []string
		wantLog       string
	}{
		{"full", `{"to": "claude", "from": "opencode", "reason": "r", "resume": "ses-1"}`,
			[]string{"--to", "claude", "--from", "opencode", "--reason", "r", "--resume", "ses-1"}, "opencode->claude"},
		{"defaults", `{"to": "claude", "resume": ""}`,
			[]string{"--to", "claude", "--from", "door", "--reason", "pending launch"}, "undefined->claude"},
		{"values", `{"to": null, "from": 5, "reason": true}`,
			[]string{"--to", "null", "--from", "5", "--reason", "true"}, "5->null"},
		{"primitive", `7`, []string{"--to", "undefined", "--from", "door", "--reason", "pending launch"}, "undefined->undefined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			f.write("flip.json", `{"to": "door", "pending_launch": `+c.pending+`}`)
			f.decides("")
			state := f.mustEvaluate(`{"last_relaunch_at": ` + jsjson.TemplateString(float64(t0-1)) + `}`)
			args := append([]string{"flip", "run", "--unless-gated"}, c.wantArgs[:6]...)
			args = append(args, "--cwd", f.root)
			args = append(args, c.wantArgs[6:]...)
			want := []SyncSpec{{Name: "/bin/orch", Args: args, Dir: f.root}}
			if !reflect.DeepEqual(f.runs, want) {
				t.Errorf("runs %+v, want %+v", f.runs, want)
			}
			if got := f.logText(); got != iso(0)+" pending launch "+c.wantLog+": launched\n" {
				t.Errorf("log %q", got)
			}
			wantJSON(t, compact(t, jsjson.Path(state, "redispatch", "pending:undefined")), `{"attempts":1,"last_attempt_at":"2027-01-15T08:00:00.000Z","confirmed_run_id":null}`)

			// The first failure waits 60 seconds before the next attempt.
			f.at(60)
			f.mustEvaluate(compact(t, state))
			if len(f.runs) != 2 {
				t.Errorf("second evaluation ran %d launches in total, want 2", len(f.runs))
			}
		})
	}
}

func TestFlipPendingLaunchWaitsForAnIdleLaneAndAFreeLock(t *testing.T) {
	f := newFixture(t)
	f.write("flip.json", `{"pending_launch": {"to": "claude"}}`)
	f.holdLock("workers/conductor.lock")
	f.decides("")
	f.mustEvaluate(`{}`)
	if len(f.launches()) != 0 || len(f.runs) != 1 {
		t.Errorf("runs %+v, want only decide", f.runs)
	}
}

// PE-30: a conductor lock file no process holds (a runner killed with SIGKILL) no longer
// blocks the relaunch of an idle lane; the JS waits for the file to go.
func TestFlipStaleConductorLockDoesNotBlockARelaunch(t *testing.T) {
	t.Run("pending launch", func(t *testing.T) {
		f := newFixture(t)
		f.mkdir("logs")
		f.write("flip.json", `{"pending_launch": {"to": "claude"}}`)
		f.write("workers/conductor.lock", "999999999")
		f.decides("")
		f.mustEvaluate(`{}`)
		if launches := f.launches(); len(launches) != 1 || launches[0].Args[1] != "run" {
			t.Errorf("runs %+v, want one flip run", f.runs)
		}
	})
	t.Run("flip target never started", func(t *testing.T) {
		f := newFixture(t)
		neverStarted(f)
		f.write("workers/conductor.lock", "999999999")
		f.mustEvaluate(`{"last_relaunch_at": ` + jsjson.TemplateString(float64(t0-120)) + `}`)
		if launches := f.launches(); len(launches) != 1 || launches[0].Args[8] != "flip target never started" {
			t.Errorf("runs %+v, want the never-started launch", f.runs)
		}
	})
}

func TestFlipDecideOutputs(t *testing.T) {
	for name, result := range map[string]SyncResult{
		"spawn failure":    {Err: &procs.StartError{Name: "/bin/orch", Code: "ENOENT"}},
		"empty stdout":     {Status: status(0), Stderr: `{"action": "flip", "to": "claude"}`},
		"not JSON":         {Status: status(0), Stdout: "flip to claude\n"},
		"JSON, then text":  {Status: status(0), Stdout: `{"action": "flip", "to": "claude"}` + "\ndone\n"},
		"no action":        {Status: status(0), Stdout: `5`},
		"falsy target":     {Status: status(0), Stdout: `{"action": "flip", "to": ""}`},
		"unknown action":   {Status: status(0), Stdout: `{"action": "pause", "to": "claude"}`},
		"stay, no restart": {Status: status(0), Stdout: `{"action": "stay"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write("flip.json", `{}`)
			f.respond = func(SyncSpec) SyncResult { return result }
			f.mustEvaluate(`{}`)
			if len(f.runs) != 1 {
				t.Errorf("runs %+v, want decide only", f.runs)
			}
		})
	}
}

// relaunchFixture is an idle project whose decide answers stdout (its last line is parsed).
func relaunchFixture(t *testing.T, lane, flip, stdout string) *fixture {
	f := newFixture(t)
	f.mkdir("logs")
	if lane != "" {
		f.write("workers/conductor.json", lane)
	}
	f.write("flip.json", flip)
	f.decides("noise\n" + stdout + "\n")
	return f
}

func TestFlipRelaunchOnFlip(t *testing.T) {
	f := relaunchFixture(t, deadConductor, `{"to": "opencode"}`, `{"action": "flip", "to": "claude", "reason": "anthropic green"}`)
	f.mailbox(`{"kind": "handoff", "at": "2027-01-15T08:30:00.000Z"}`)
	var conductorAtLaunch string
	decide := f.respond
	f.respond = func(spec SyncSpec) SyncResult {
		if spec.Args[1] == "run" {
			conductorAtLaunch = f.read("workers/conductor.json")
		}
		return decide(spec)
	}
	f.at(0.5)
	state := f.mustEvaluate(`{"last_relaunch_at": ` + jsjson.TemplateString(float64(t0-600)) + `}`)

	want := []SyncSpec{
		{Name: "/bin/orch", Args: []string{"flip", "decide", "--active", "opencode", "--cwd", f.root}},
		{Name: "/bin/orch", Args: []string{"flip", "run", "--unless-gated", "--to", "claude", "--from", "opencode", "--reason", "flip: anthropic green", "--cwd", f.root}},
	}
	if !reflect.DeepEqual(f.runs, want) {
		t.Errorf("runs %+v, want %+v", f.runs, want)
	}
	wantConductor := deadConductor
	if got := compact(t, mustParse(t, conductorAtLaunch)); got != compact(t, mustParse(t, wantConductor)) {
		t.Errorf("conductor.json at launch %s, want %s", got, wantConductor)
	}
	wantJSON(t, compact(t, jsjson.Path(state, "redispatch", "9:conductor")), `{"attempts":1,"last_attempt_at":"2027-01-15T08:00:00.000Z","confirmed_run_id":null}`)
	if got := f.logText(); got != iso(0)+" relaunch opencode->claude (flip): launched\n" {
		t.Errorf("log %q", got)
	}
	if len(f.incidents) != 1 || f.incidents[0].Component != "flip" || f.incidents[0].Kind != "flip" ||
		f.incidents[0].Summary != "conductor flip opencode->claude" || f.incidents[0].Detail != "anthropic green" || f.incidents[0].Project != f.root {
		t.Errorf("incidents %+v", f.incidents)
	}
}

func (f *fixture) mailbox(lines ...string) {
	f.write("mailbox/messages.jsonl", strings.Join(lines, "\n")+"\n")
}

func TestFlipRelaunchOnHandbackWithoutALane(t *testing.T) {
	f := relaunchFixture(t, ``, `{"to": "claude", "paused": 1}`, `{"action": "handback", "to": "opencode"}`)
	f.mustEvaluate(`{}`)
	launches := f.launches()
	if len(launches) != 1 || !reflect.DeepEqual(launches[0].Args[3:9], []string{"--to", "opencode", "--from", "claude", "--reason", "handback: undefined"}) || len(launches[0].Args) != 11 {
		t.Fatalf("launches %+v", launches)
	}
	if f.incidents[0].Detail != "" {
		t.Errorf("detail %q, want empty for an undefined reason", f.incidents[0].Detail)
	}
	if got := f.logText(); got != iso(0)+" relaunch claude->opencode (handback, was paused): launched\n" {
		t.Errorf("log %q", got)
	}
}

func TestFlipStayRelaunchesTheActiveDoor(t *testing.T) {
	lane := func(status string) string { return strings.Replace(deadConductor, `"finished"`, `"`+status+`"`, 1) }
	handoff := func(at string) string { return `{"kind": "handoff", "at": "` + at + `"}` }
	cases := []struct {
		name, lane, flip string
		mailbox          []string
		wantReason       string // "" when nothing is launched
		wantLogAction    string
	}{
		{"paused", ``, `{"paused": true}`, nil, "stay: r", "stay, was paused"},
		{"not paused, no lane", ``, `{"paused": 0}`, nil, "", ""},
		{"failed", lane("failed"), `{}`, nil, "stay: r", "stay"},
		{"deferred", lane("deferred"), `{}`, nil, "stay: r", "stay"},
		{"interrupted", lane("interrupted"), `{}`, nil, "stay: r", "stay"},
		{"running but dead", lane("running"), `{}`, nil, "", ""},
		{"finished early", deadConductor, `{}`, nil, "stay: r (previous conductor exited without handoff)", "stay"},
		{"finished after a handoff", deadConductor, `{}`, []string{handoff("2027-01-15T08:00:00.000Z")}, "", ""},
		{"handoff before the start", deadConductor, `{}`, []string{handoff("2027-01-15T07:00:00.000Z")}, "stay: r (previous conductor exited without handoff)", "stay"},
		{"odd mailbox lines", deadConductor, `{}`, []string{"", "null", "{bad", `{"kind": "handoff"}`, `{"kind": "note", "at": "2028"}`, `5`}, "stay: r (previous conductor exited without handoff)", "stay"},
		{"null at, no started_at", strings.Replace(deadConductor, `"started_at"`, `"begun"`, 1), `{}`, []string{`{"kind": "handoff", "at": null}`}, "stay: r (previous conductor exited without handoff)", "stay"},
		{"null at, null started_at", strings.Replace(deadConductor, `"2027-01-15T08:00:00.000Z"`, `null`, 1), `{}`, []string{`{"kind": "handoff", "at": null}`}, "", ""},
		{"handoff compared as text", deadConductor, `{}`, []string{handoff("2027-02")}, "", ""},
		{"already redispatched", strings.Replace(deadConductor, `}`, `, "redispatched_at": "x"}`, 1), `{}`, nil, "", ""},
		{"failed, redispatched", strings.Replace(lane("failed"), `}`, `, "redispatched_at": "x"}`, 1), `{}`, nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := relaunchFixture(t, c.lane, c.flip, `{"action": "stay", "to": "elsewhere", "reason": "r"}`)
			if c.mailbox != nil {
				f.mailbox(c.mailbox...)
			}
			f.mustEvaluate(`{}`)
			launches := f.launches()
			if c.wantReason == "" {
				if len(launches) != 0 {
					t.Errorf("launched %+v", launches)
				}
				return
			}
			if len(launches) != 1 || launches[0].Args[4] != "opencode" || launches[0].Args[8] != c.wantReason {
				t.Fatalf("launches %+v, want reason %q", launches, c.wantReason)
			}
			if got := f.logText(); got != iso(0)+" relaunch opencode->opencode ("+c.wantLogAction+"): launched\n" {
				t.Errorf("log %q", got)
			}
		})
	}
}

func TestFlipUnreadableMailboxAborts(t *testing.T) {
	f := relaunchFixture(t, deadConductor, `{}`, `{"action": "stay"}`)
	f.mkdir("mailbox/messages.jsonl")
	_, err := f.evaluate(`{}`, `{}`)
	if err == nil || errorCode(err) != "EISDIR" {
		t.Fatalf("error %v, want EISDIR", err)
	}
}

func TestFlipRelaunchCooldown(t *testing.T) {
	for last, launched := range map[float64]bool{t0 - 599: false, t0 - 600: true} {
		f := relaunchFixture(t, ``, `{}`, `{"action": "flip", "to": "claude"}`)
		state := f.mustEvaluate(`{"last_relaunch_at": ` + jsjson.TemplateString(last) + `}`)
		if got := len(f.launches()) == 1; got != launched {
			t.Errorf("last_relaunch_at %v: launched %v", last, got)
		}
		if !launched && jsjson.Path(state, "last_relaunch_at") != last {
			t.Errorf("last_relaunch_at changed without a launch: %v", jsjson.Path(state, "last_relaunch_at"))
		}
	}
}

func TestFlipRelaunchResumesTheSameDoorsSession(t *testing.T) {
	claudeLane := strings.Replace(deadConductor, `"opencode"`, `"claude"`, 1)
	cases := []struct {
		name, lane, flip, decision string
		wantResume                 []string
	}{
		{"same door opencode", deadConductor, `{"paused": true}`, `{"action": "stay"}`, []string{"--resume", "ses-o"}},
		{"same door claude", strings.Replace(claudeLane, `"finished"`, `"failed"`, 1), `{"to": "claude"}`, `{"action": "stay"}`, []string{"--resume", "ses-o"}},
		{"flip to the active door", deadConductor, `{}`, `{"action": "flip", "to": "opencode"}`, []string{"--resume", "ses-o"}},
		{"other door", deadConductor, `{}`, `{"action": "flip", "to": "claude"}`, nil},
		{"target is not the active door", claudeLane, `{"to": "opencode"}`, `{"action": "flip", "to": "claude"}`, nil},
		{"harness of another door", strings.Replace(claudeLane, `"finished"`, `"failed"`, 1), `{}`, `{"action": "stay"}`, nil},
		{"no session", strings.Replace(deadConductor, `"ses-o"`, `""`, 1), `{"paused": true}`, `{"action": "stay"}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := relaunchFixture(t, c.lane, c.flip, c.decision)
			f.mustEvaluate(`{}`)
			launches := f.launches()
			if len(launches) != 1 {
				t.Fatalf("launches %+v", launches)
			}
			if got := launches[0].Args[11:]; !reflect.DeepEqual(got, c.wantResume) && (len(got) != 0 || c.wantResume != nil) {
				t.Errorf("resume args %q, want %q", got, c.wantResume)
			}
		})
	}
}

// PE-36: even a hand-edited primitive lane is never written by the supervisor.
func TestFlipRelaunchLeavesAPrimitiveLaneUntouched(t *testing.T) {
	f := relaunchFixture(t, `7`, `{}`, `{"action":"flip","to":"claude"}`)
	f.mustEvaluate(`{}`)
	if f.read("workers/conductor.json") != "7" || len(f.launches()) != 1 {
		t.Fatalf("lane changed or launch missing: %v", f.launches())
	}
}

func TestFlipRejectsANulArgumentBeforeSpawning(t *testing.T) {
	f := relaunchFixture(t, ``, `{"to": "a\u0000"}`, ``)
	_, err := f.evaluate(`{}`, `{}`)
	if err == nil || errorCode(err) != "ERR_INVALID_ARG_VALUE" || len(f.runs) != 0 {
		t.Fatalf("error %v, runs %+v", err, f.runs)
	}
}

func TestTickRunsTheFlipEvaluationLast(t *testing.T) {
	f := newFixture(t)
	f.write("flip.json", `{"to": "claude"}`)
	f.decides("")
	state := f.mustTick(`{}`)
	wantJSON(t, strings.Join(state.Keys(), ","), "events,lanes,last_flip_check,checked_at,pid,status")
}

// PE-36 supersedes the old PE-16 rewrite on this path: no worker-owned file is written.
func TestFlipRelaunchLeavesAnArrayConductorUntouched(t *testing.T) {
	f := relaunchFixture(t, `[1]`, `{}`, `{"action":"flip","to":"claude"}`)
	f.mustEvaluate(`{}`)
	if f.read("workers/conductor.json") != "[1]" || len(f.launches()) != 1 {
		t.Fatalf("lane changed or launch missing: %v", f.launches())
	}
}

// The fake self-exec is the boundary after evaluateFlip's snapshot and before Run's
// transaction. Install a human gate there for each of the three automatic launch paths.
func TestSupervisorRelaunchCannotEraseAGateInstalledAfterItsRead(t *testing.T) {
	for _, branch := range []string{"never-started", "pending", "decided"} {
		t.Run(branch, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			switch branch {
			case "never-started":
				neverStarted(f)
			case "pending":
				f.write("flip.json", `{"pending_launch":{"to":"claude","from":"opencode"}}`)
			case "decided":
				f.write("flip.json", `{"paused":{"door":"opencode"}}`)
			}
			project := flipcmd.NewProject(f.root)
			launches := 0
			f.respond = func(spec SyncSpec) SyncResult {
				if spec.Args[1] == "decide" {
					return SyncResult{Status: status(0), Stdout: `{"action":"flip","to":"claude","reason":"headroom"}` + "\n"}
				}
				gateOpts := cliargs.Opts{"reason": {Value: "installed after supervisor read"}}
				if _, err := project.Gate(gateOpts); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(project.FlipPath)
				if err != nil {
					t.Fatal(err)
				}
				opts, _ := cliargs.ParseArgs(spec.Args[2:], []string{"to", "from", "reason", "cwd", "resume"}, []string{"unless-gated"})
				answer, code, err := project.Run(opts, func([]string) (*int, string, string) { launches++; return status(0), "", "" })
				if err != nil || code != 1 || compact(t, answer) != `{"flipped":false,"reason":"awaiting human"}` {
					t.Fatalf("automatic run = %v, %d, %v", answer, code, err)
				}
				after, err := os.ReadFile(project.FlipPath)
				if err != nil || string(after) != string(before) {
					t.Fatalf("gate changed: %s, %v", after, err)
				}
				return SyncResult{Status: status(code), Stdout: compact(t, answer) + "\n"}
			}
			f.mustEvaluate(`{}`)
			if len(f.launches()) != 1 || launches != 0 {
				t.Fatalf("flip runs %v, worker launches %d", f.launches(), launches)
			}
		})
	}
}

func TestUnreadableFlipStateBlocksRelaunchAndLogsOnce(t *testing.T) {
	for _, kind := range []string{"corrupt", "mode-0000"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir("logs")
			text := "{"
			if kind == "mode-0000" {
				text = `{}`
			}
			f.write("flip.json", text)
			if kind == "mode-0000" {
				if err := os.Chmod(f.sup.flipPath(), 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(f.sup.flipPath(), 0600) })
				if _, err := os.ReadFile(f.sup.flipPath()); err == nil {
					t.Skip("process can read mode-0000 files")
				}
			}
			state := f.mustEvaluate(`{}`)
			first := f.logText()
			state.Delete("last_flip_check")
			f.mustEvaluate(compact(t, state))
			if len(f.runs) != 0 || first == "" || f.logText() != first || !strings.Contains(first, "flip state unreadable:") {
				t.Fatalf("runs %v, first log %q, now %q", f.runs, first, f.logText())
			}
		})
	}
}
