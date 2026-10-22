package flipcmd

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

const nowISO = "2026-10-19T12:00:00.000Z"

// newTestProject is a project in a fresh directory with a fixed clock and message id.
func newTestProject(t *testing.T) Project {
	t.Helper()
	p := NewProject(t.TempDir())
	p.Now = func() time.Time { return now }
	p.NewID = func() string { return "id-1" }
	return p
}

func opts(argv ...string) cliargs.Opts {
	o, _ := cliargs.ParseArgs(argv, []string{"active", "to", "from", "cwd", "reason", "resume", "question", "brief"}, []string{"force", "clear", "no-relaunch", "unless-gated"})
	return o
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// tempNamesSpelledOut replaces the random suffix of private temp names in message by "*".
func tempNamesSpelledOut(message string) string {
	return regexp.MustCompile(`\.tmp-[0-9]+`).ReplaceAllString(message, ".tmp-*")
}

func compact(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// indented is JSON.stringify(text parsed, null, 2) + "\n", as flip.json is written.
func indented(t *testing.T, text string) string {
	t.Helper()
	v, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	data, err := jsjson.MarshalIndent(v, "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func (p Project) messages(t *testing.T) string {
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

func TestReadDistinguishesAbsentStateAndJSONValues(t *testing.T) {
	for _, content := range []string{`{"to":"claude"}`, `null`, `0`, `""`, `"abc"`, `[1,2]`, `5`, `{"__proto__":1,"constructor":2}`} {
		p := newTestProject(t)
		writeFile(t, p.FlipPath, content)
		record, err := p.Read()
		if err != nil || !record.Present || compact(t, record.Value) != content {
			t.Errorf("Read over %s = %+v, %v", content, record, err)
		}
	}
	record, err := newTestProject(t).Read()
	if err != nil || record.Present || record.Value != nil {
		t.Errorf("missing = %+v, %v", record, err)
	}
	p := newTestProject(t)
	writeFile(t, p.FlipPath, "{\"to\":\"a\xffb\"}")
	record, err = p.Read()
	if err != nil || compact(t, record.Value) != `{"to":"a�b"}` {
		t.Errorf("UTF-8 = %+v, %v", record, err)
	}
}

func TestStatusPrintsTheRecordOrNeverFlipped(t *testing.T) {
	for content, want := range map[string]string{
		"":             `{"state":"never flipped"}`,
		`0`:            `{"state":"never flipped"}`,
		`false`:        `{"state":"never flipped"}`,
		`null`:         `{"state":"never flipped"}`,
		`""`:           `{"state":"never flipped"}`,
		`"abc"`:        `"abc"`,
		`[1,2]`:        `[1,2]`,
		`{"to":"x"}`:   `{"to":"x"}`,
		`{"to":"x"`:    `{"state":"never flipped"}`,
		`{"at":"now"}`: `{"at":"now"}`,
	} {
		p := newTestProject(t)
		if content != "" {
			writeFile(t, p.FlipPath, content)
		}
		if got := compact(t, p.Status()); got != want {
			t.Errorf("Status() over %q = %s, want %s", content, got, want)
		}
		if content == "" {
			if _, err := os.Stat(filepath.Dir(p.FlipPath)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Status() created .orchestrator (err %v)", err)
			}
		}
	}
}

func TestPauseSpreadsThePreviousValueAndReplacesPausedInPlace(t *testing.T) {
	paused := `{"at":"` + nowISO + `","door":"opencode","reason":"both vendors red"}`
	for _, c := range []struct {
		name, prev, want string
	}{
		{"fresh project", "", `{"paused":` + paused + `}`},
		{"keys kept, paused in place", `{"to":"claude","paused":{"door":"x"},"awaiting_human":{"door":"y"},"z":1}`,
			`{"to":"claude","paused":` + paused + `,"awaiting_human":{"door":"y"},"z":1}`},
		{"string spreads into index keys", `"abc"`, `{"0":"a","1":"b","2":"c","paused":` + paused + `}`},
		{"array spreads into index keys", `[1,2]`, `{"0":1,"1":2,"paused":` + paused + `}`},
		{"number spreads into nothing", `5`, `{"paused":` + paused + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProject(t)
			if c.prev != "" {
				writeFile(t, p.FlipPath, c.prev)
			}
			answer, err := p.Pause(opts("pause"))
			if err != nil {
				t.Fatal(err)
			}
			if got := compact(t, answer); got != `{"paused":true,"door":"opencode"}` {
				t.Errorf("answer = %s", got)
			}
			if got := readFile(t, p.FlipPath); got != indented(t, c.want) {
				t.Errorf("flip.json = %s, want %s", got, indented(t, c.want))
			}
			want := `{"id":"id-1","at":"` + nowISO + `","from":"conductor-opencode","to":"supervisor","kind":"handoff","subject":"conductor paused: both vendors red","body":"Supervisor will relaunch when a window resets.","refs":[".orchestrator/STATE.md"],"thread":null}` + "\n"
			if got := p.messages(t); got != want {
				t.Errorf("mailbox = %s, want %s", got, want)
			}
		})
	}
}

// A value key without a value (last on the command line) is the JS true.
func TestPauseInternalOptionValuesRetainJSTypes(t *testing.T) {
	p := newTestProject(t)
	answer, err := p.Pause(opts("pause", "--reason", "r", "--active"))
	if err != nil {
		t.Fatal(err)
	}
	if got := compact(t, answer); got != `{"paused":true,"door":true}` {
		t.Errorf("answer = %s", got)
	}
	if got := p.messages(t); !strings.Contains(got, `"from":"conductor-true"`) {
		t.Errorf("mailbox = %s, want a message from conductor-true", got)
	}

	p = newTestProject(t)
	if _, err := p.Pause(opts("pause", "--reason")); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, p.FlipPath), indented(t, `{"paused":{"at":"`+nowISO+`","door":"opencode","reason":true}}`); got != want {
		t.Errorf("flip.json = %s, want %s", got, want)
	}
}

// --brief (a both-red kickoff's) is stored last; a bare or empty one stores nothing.
func TestPauseStoresANonEmptyBrief(t *testing.T) {
	paused := `{"at":"` + nowISO + `","door":"claude","reason":"r"`
	for _, c := range []struct {
		name string
		argv []string
		want string
	}{
		{"brief", []string{"--brief", "x.md"}, paused + `,"brief":"x.md"}`},
		{"bare", []string{"--brief"}, paused + `}`},
		{"empty", []string{"--brief", ""}, paused + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProject(t)
			if _, err := p.Pause(opts(append([]string{"pause", "--active", "claude", "--reason", "r"}, c.argv...)...)); err != nil {
				t.Fatal(err)
			}
			if got, want := readFile(t, p.FlipPath), indented(t, `{"paused":`+c.want+`}`); got != want {
				t.Errorf("flip.json = %s, want %s", got, want)
			}
		})
	}
}

func TestPauseIgnoresMailboxFailures(t *testing.T) {
	p := newTestProject(t)
	writeFile(t, filepath.Join(p.Root, ".orchestrator", "mailbox"), "not a directory")
	if _, err := p.Pause(opts("pause")); err != nil {
		t.Fatalf("Pause() = %v, want the mailbox failure ignored", err)
	}
}

func TestPauseRefusesUnreadableStateAndDirectoryCreationFailures(t *testing.T) {
	p := newTestProject(t)
	writeFile(t, filepath.Join(p.Root, ".orchestrator"), "")
	_, err := p.Pause(opts("pause"))
	if want := "EEXIST: file already exists, mkdir '" + filepath.Join(p.Root, ".orchestrator") + "'"; err == nil || err.Error() != want {
		t.Errorf("Pause() = %v, want %s", err, want)
	}

	p = newTestProject(t)
	if err := os.MkdirAll(p.FlipPath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = p.Pause(opts("pause"))
	// PE-32: unreadable state fails before any replacement.
	if want := "flip state unreadable: " + p.FlipPath + ": EISDIR"; err == nil || tempNamesSpelledOut(err.Error()) != want {
		t.Errorf("Pause() = %v, want %s", err, want)
	}
	if got := p.messages(t); got != "" {
		t.Errorf("mailbox = %s, want nothing after a failed write", got)
	}
}

func TestGateAppendsKeepsPositionAndDeletesPaused(t *testing.T) {
	gate := `{"at":"` + nowISO + `","door":"opencode","reason":"human approval required","question":null}`
	for _, c := range []struct {
		name, prev, want string
	}{
		{"fresh project", "", `{"awaiting_human":` + gate + `}`},
		{"appended, paused deleted", `{"to":"claude","paused":{"door":"x"},"z":1}`, `{"to":"claude","z":1,"awaiting_human":` + gate + `}`},
		{"existing gate keeps its position", `{"awaiting_human":{"door":"y"},"to":"claude"}`, `{"awaiting_human":` + gate + `,"to":"claude"}`},
		{"falsy is an object", `0`, `{"awaiting_human":` + gate + `}`},
		{"array replaced by an object", `[1,2]`, `{"awaiting_human":` + gate + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProject(t)
			if c.prev != "" {
				writeFile(t, p.FlipPath, c.prev)
			}
			answer, err := p.Gate(opts("gate"))
			if err != nil {
				t.Fatal(err)
			}
			if got := compact(t, answer); got != `{"awaiting_human":true,"door":"opencode"}` {
				t.Errorf("answer = %s", got)
			}
			if got := readFile(t, p.FlipPath); got != indented(t, c.want) {
				t.Errorf("flip.json = %s, want %s", got, indented(t, c.want))
			}
			want := `{"id":"id-1","at":"` + nowISO + `","from":"conductor-opencode","to":"human","kind":"handoff","subject":"awaiting human: approval","body":"Supervisor will not relaunch until cleared: orch flip gate --clear (or a reply to the gate's question, or kickoff/flip run).","refs":[".orchestrator/STATE.md"],"thread":null}` + "\n"
			if got := p.messages(t); got != want {
				t.Errorf("mailbox = %s, want %s", got, want)
			}
		})
	}
}

func TestGateOptionsAndSubject(t *testing.T) {
	p := newTestProject(t)
	reason := strings.Repeat("r", 79) + "😀tail"
	if _, err := p.Gate(opts("gate", "--active", "claude", "--reason", reason, "--question", "q1")); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, p.FlipPath), indented(t, `{"awaiting_human":{"at":"`+nowISO+`","door":"claude","reason":"`+reason+`","question":"q1"}}`); got != want {
		t.Errorf("flip.json = %s, want %s", got, want)
	}
	// The cut at 80 UTF-16 units splits the emoji: its high surrogate becomes U+FFFD (PE-1).
	if got := p.messages(t); !strings.Contains(got, `"from":"conductor-claude"`) || !strings.Contains(got, `"subject":"awaiting human: `+strings.Repeat("r", 79)+"�\"") {
		t.Errorf("mailbox = %s", got)
	}
}

func TestGateWithBareReasonUsesDefaultAndPostsMessage(t *testing.T) {
	p := newTestProject(t)
	answer, err := p.Gate(opts("gate", "--question", "q", "--reason"))
	if err != nil {
		t.Fatal(err)
	}
	if got := compact(t, answer); got != `{"awaiting_human":true,"door":"opencode"}` {
		t.Errorf("answer = %s", got)
	}
	if got, want := readFile(t, p.FlipPath), indented(t, `{"awaiting_human":{"at":"`+nowISO+`","door":"opencode","reason":"human approval required","question":"q"}}`); got != want {
		t.Errorf("flip.json = %s, want %s", got, want)
	}
	if got := p.messages(t); !strings.Contains(got, `"subject":"awaiting human: approval"`) {
		t.Errorf("mailbox = %s, want default message", got)
	}
}

func TestGateOnAPrimitiveRecordsTheGate(t *testing.T) {
	for _, content := range []string{`5`, `1.5`, `1e21`, `"abc"`, `"a\nb"`, `true`} {
		p := newTestProject(t)
		writeFile(t, p.FlipPath, content)
		if _, err := p.Gate(opts("gate")); err != nil {
			t.Fatal(err)
		}
		record, err := p.Read()
		if err != nil || jsjson.Path(record.Value, "awaiting_human", "reason") != "human approval required" {
			t.Errorf("gate over %s = %+v, %v", content, record, err)
		}
		if p.messages(t) == "" {
			t.Error("no gate message")
		}
	}
}

func TestGateClearWithoutAGateRewritesAnyShapeAndQueuesNothing(t *testing.T) {
	for prev, want := range map[string]string{
		"":           `{}`,
		`{"to":"c"}`: `{"to":"c"}`,
		`"abc"`:      `"abc"`,
		`5`:          `5`,
		`[1]`:        `[1]`,
		`null`:       `{}`,
	} {
		p := newTestProject(t)
		if prev != "" {
			writeFile(t, p.FlipPath, prev)
		}
		answer, err := p.Gate(opts("gate", "--clear", "--reason", "x"))
		if err != nil {
			t.Fatalf("Gate(--clear) over %q: %v", prev, err)
		}
		if got := compact(t, answer); got != `{"awaiting_human":false}` {
			t.Errorf("answer = %s", got)
		}
		if got := readFile(t, p.FlipPath); got != indented(t, want) {
			t.Errorf("flip.json over %q = %s, want %s", prev, got, indented(t, want))
		}
		if got := p.messages(t); got != "" {
			t.Errorf("mailbox = %s, want no message", got)
		}
	}
}

func TestGateClearQueuesARelaunchOfTheGatedDoor(t *testing.T) {
	relaunch := func(door string) string {
		return `{"to":"` + door + `","from":"` + door + `","reason":"human gate cleared","resume":null,"at":"` + nowISO + `"}`
	}
	for _, c := range []struct {
		name, prev, door, want string
	}{
		{"gated door", `{"awaiting_human":{"door":"claude"},"to":"c"}`, "claude", `{"to":"c","pending_launch":` + relaunch("claude") + `}`},
		{"no door is opencode", `{"awaiting_human":"yes"}`, "opencode", `{"pending_launch":` + relaunch("opencode") + `}`},
		{"an earlier pending launch is kept", `{"pending_launch":{"to":"opencode","from":"claude"},"awaiting_human":{"door":"claude"}}`, "opencode", `{"pending_launch":{"to":"opencode","from":"claude"}}`},
		{"a falsy pending launch is replaced in place", `{"pending_launch":null,"awaiting_human":{"door":"claude"},"z":1}`, "claude", `{"pending_launch":` + relaunch("claude") + `,"z":1}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProject(t)
			writeFile(t, p.FlipPath, c.prev)
			answer, err := p.Gate(opts("gate", "--clear"))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := compact(t, answer), `{"awaiting_human":false,"relaunch":"`+c.door+`"}`; got != want {
				t.Errorf("answer = %s, want %s", got, want)
			}
			if got := readFile(t, p.FlipPath); got != indented(t, c.want) {
				t.Errorf("flip.json = %s, want %s", got, indented(t, c.want))
			}
		})
	}
}

// Kickoff (--no-relaunch) launches the conductor itself, so any queued launch or pause is
// superseded along with the gate.
func TestGateClearNoRelaunchDropsTheGateAQueuedLaunchAndAPause(t *testing.T) {
	for _, c := range []struct {
		name, prev, want string
	}{
		{"gate only", `{"awaiting_human":{"door":"claude"},"to":"c"}`, `{"to":"c"}`},
		{"gate, launch and pause", `{"awaiting_human":{"door":"claude"},"pending_launch":{"to":"opencode"},"paused":{"door":"claude"},"z":1}`, `{"z":1}`},
		{"no gate", `{"pending_launch":{"to":"opencode"},"paused":{"door":"claude"},"z":1}`, `{"z":1}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProject(t)
			writeFile(t, p.FlipPath, c.prev)
			answer, err := p.Gate(opts("gate", "--clear", "--no-relaunch"))
			if err != nil {
				t.Fatal(err)
			}
			if got := compact(t, answer); got != `{"awaiting_human":false}` {
				t.Errorf("answer = %s", got)
			}
			if got := readFile(t, p.FlipPath); got != indented(t, c.want) {
				t.Errorf("flip.json = %s, want %s", got, indented(t, c.want))
			}
		})
	}
}

// mailbox reply (relaunch on) keeps a launch queued before the gate, and a pause.
func TestClearGateWithRelaunchKeepsAQueuedLaunchAndAPause(t *testing.T) {
	p := newTestProject(t)
	writeFile(t, p.FlipPath, `{"awaiting_human":{"door":"claude"},"pending_launch":{"to":"opencode"},"paused":{"door":"claude"}}`)
	door, queued, err := p.ClearGate(true)
	if err != nil || door != "opencode" || !queued {
		t.Fatalf("ClearGate = %v, %v, %v", door, queued, err)
	}
	if got, want := readFile(t, p.FlipPath), indented(t, `{"pending_launch":{"to":"opencode"},"paused":{"door":"claude"}}`); got != want {
		t.Errorf("flip.json = %s, want %s", got, want)
	}
}

func TestGateRefusesANonDirectoryStatePath(t *testing.T) {
	p := newTestProject(t)
	writeFile(t, filepath.Join(p.Root, ".orchestrator"), "")
	_, err := p.Gate(opts("gate"))
	if want := "EEXIST: file already exists, mkdir '" + filepath.Join(p.Root, ".orchestrator") + "'"; err == nil || err.Error() != want {
		t.Errorf("Gate() = %v, want %s", err, want)
	}
}

// stableRouting has both vendors green, OpenAI's window resetting in two hours: stably green.
func stableRouting(t *testing.T) *usage.Routing {
	t.Helper()
	parse := func(text string) *jsjson.Object {
		v, err := jsjson.Unmarshal([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return v.(*jsjson.Object)
	}
	resetsAt := jsjson.ISOString(now.Add(120 * time.Minute))
	return &usage.Routing{
		Registry: parse(`{"conductor":{"home":"opencode","green_grace_minutes":10,"flip_cooldown_minutes":10}}`),
		Resolved: routing.Resolved{Postures: routing.Postures{
			OpenAI:    routing.VendorPosture{Available: true, Overall: routing.Green, Detail: parse(`{"five_hour":{"resets_at":"` + resetsAt + `","window_mins":300}}`)},
			Anthropic: routing.VendorPosture{Available: true, Overall: routing.Green, Detail: parse(`{}`)},
		}},
	}
}

func TestDecideCoolsDownOnlyForAnObjectWithAParsableAt(t *testing.T) {
	twoMinutesAgo := jsjson.ISOString(now.Add(-2 * time.Minute))
	thirtyMinutesAgo := jsjson.ISOString(now.Add(-30 * time.Minute))
	for prev, want := range map[string]string{
		"":                          flip.ActionHandback,
		`{`:                         flip.ActionGate,
		`null`:                      flip.ActionHandback,
		`5`:                         flip.ActionHandback,
		`"` + twoMinutesAgo + `"`:   flip.ActionHandback, // a string's at is a function
		`["` + twoMinutesAgo + `"]`: flip.ActionHandback, // so is an array's
		`{"paused":{"at":"` + twoMinutesAgo + `"}}`: flip.ActionHandback,
		`{"at":"` + twoMinutesAgo + `"}`:            flip.ActionStay,
		`{"at":"` + thirtyMinutesAgo + `"}`:         flip.ActionHandback,
		`{"at":2030}`:                               flip.ActionStay, // the future cools down
		`{"at":["2030"]}`:                           flip.ActionStay,
		`{"at":"garbage"}`:                          flip.ActionHandback,
		`{"at":true}`:                               flip.ActionHandback,
		`{"at":{}}`:                                 flip.ActionHandback,
		`{"at":0}`:                                  flip.ActionHandback,
		`{"at":"2026-10-19 11:58:00"}`:              flip.ActionHandback, // V8's legacy form: PE-15
	} {
		p := newTestProject(t)
		if prev != "" {
			writeFile(t, p.FlipPath, prev)
		}
		d, err := p.Decide(opts("decide", "--active", "claude"), func() (*usage.Routing, error) { return stableRouting(t), nil })
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != want {
			t.Errorf("Decide over %s = %+v, want %s", prev, d, want)
		}
		if want == flip.ActionStay && d.Reason != "cooldown after a recent flip" {
			t.Errorf("Decide over %s reason = %q", prev, d.Reason)
		}
	}
}

func TestDecideReasonsForNonDoors(t *testing.T) {
	for _, c := range []struct {
		args   []string
		reason string
	}{
		{[]string{"--active", "constructor"}, "function Object() { [native code] } is red; openai has headroom"},
		{[]string{"--active", "__proto__"}, "[object Object] is red; openai has headroom"},
		{[]string{"--active", "gemini"}, "undefined is red; openai has headroom"},
		{[]string{"--active"}, "undefined is red; openai has headroom"},
	} {
		d, err := newTestProject(t).Decide(opts(append([]string{"decide"}, c.args...)...), func() (*usage.Routing, error) { return stableRouting(t), nil })
		if err != nil {
			t.Fatal(err)
		}
		if want := (flip.Decision{Action: flip.ActionFlip, To: flip.DoorOpencode, Reason: c.reason}); d != want {
			t.Errorf("Decide(%v) = %+v, want %+v", c.args, d, want)
		}
	}
}

func TestDecideRefusesAMissingActiveBeforeRouting(t *testing.T) {
	for _, args := range [][]string{{"decide"}, {"decide", "--active", ""}} {
		_, err := newTestProject(t).Decide(opts(args...), func() (*usage.Routing, error) {
			t.Fatal("routing read before --active was checked")
			return nil, nil
		})
		if !errors.Is(err, ErrActiveRequired) {
			t.Errorf("Decide(%v) = %v, want ErrActiveRequired", args, err)
		}
	}
}

func TestDecideReturnsRoutingFailures(t *testing.T) {
	failure := errors.New("registry unreadable")
	_, err := newTestProject(t).Decide(opts("decide", "--active", "claude"), func() (*usage.Routing, error) { return nil, failure })
	if !errors.Is(err, failure) {
		t.Errorf("Decide() = %v, want the routing failure", err)
	}
}

func TestDecideAnswersGateWithoutRoutingWhileAHumanGateIsSet(t *testing.T) {
	for prev, reason := range map[string]string{
		`{"awaiting_human":{"reason":"push to main?","door":"claude"}}`: "awaiting human: push to main?",
		`{"awaiting_human":{"door":"claude"}}`:                          "awaiting human: undefined",
		`{"awaiting_human":{"reason":null}}`:                            "awaiting human: null",
		`{"awaiting_human":{"reason":{}}}`:                              "awaiting human: [object Object]",
		`{"awaiting_human":true}`:                                       "awaiting human: undefined",
		`{"awaiting_human":"yes"}`:                                      "awaiting human: undefined",
	} {
		p := newTestProject(t)
		writeFile(t, p.FlipPath, prev)
		d, err := p.Decide(opts("decide", "--active", "claude"), func() (*usage.Routing, error) {
			t.Fatalf("routing read while gated by %s", prev)
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := (flip.Decision{Action: flip.ActionGate, Reason: reason}); d != want {
			t.Errorf("Decide over %s = %+v, want %+v", prev, d, want)
		}
	}
}

func TestDecideRoutesWhenNoHumanGateIsSet(t *testing.T) {
	for _, prev := range []string{
		`{"awaiting_human":false}`,
		`{"awaiting_human":null}`,
		`{"awaiting_human":""}`,
		`{"awaiting_human":0}`,
		`[{"awaiting_human":true}]`,
		`"awaiting_human"`,
	} {
		p := newTestProject(t)
		writeFile(t, p.FlipPath, prev)
		d, err := p.Decide(opts("decide", "--active", "claude"), func() (*usage.Routing, error) { return stableRouting(t), nil })
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != flip.ActionHandback {
			t.Errorf("Decide over %s = %+v, want %s", prev, d, flip.ActionHandback)
		}
	}
}

// A supervisor tick reading flip.json while gate and pause rewrite it sees the old or the new
// record, never an empty or partial file (PE-21; the JS truncates and writes in place).
func TestFlipJSONIsReplacedAtomically(t *testing.T) {
	p := newTestProject(t)
	if _, err := p.Pause(opts("pause")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	torn := make(chan string, 1)
	go func() {
		defer close(torn)
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, ok := nodefs.ReadJSON(p.FlipPath); !ok {
				data, _ := os.ReadFile(p.FlipPath)
				torn <- string(data)
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		write := p.Pause
		if i%2 == 0 {
			write = p.Gate
		}
		if _, err := write(opts("x")); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	if data, seen := <-torn; seen {
		t.Errorf("a reader saw flip.json = %q", data)
	}
}
