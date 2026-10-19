package flipcmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
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
	o, _ := cliargs.ParseArgs(argv, []string{"active", "to", "from", "cwd", "reason", "resume", "question"}, []string{"force", "clear"})
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

func TestReadFlipReadsAnyJSONValueAndNilOnFailure(t *testing.T) {
	for _, c := range []struct {
		name, content, want string
	}{
		{"object", `{"to":"claude"}`, `{"to":"claude"}`},
		{"null", `null`, `null`},
		{"zero", `0`, `0`},
		{"empty string", `""`, `""`},
		{"string", `"abc"`, `"abc"`},
		{"array", `[1,2]`, `[1,2]`},
		{"number", `5`, `5`},
		{"unparsable", `{"to":`, `null`},
		{"BOM", "\xef\xbb\xbf{}", `null`},
		{"invalid UTF-8 in a string", "{\"to\":\"a\xffb\"}", "{\"to\":\"a�b\"}"},
		{"prototype keys", `{"__proto__":1,"constructor":2}`, `{"__proto__":1,"constructor":2}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProject(t)
			writeFile(t, p.FlipPath, c.content)
			if got := compact(t, p.ReadFlip()); got != c.want {
				t.Errorf("ReadFlip() = %s, want %s", got, c.want)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		if got := newTestProject(t).ReadFlip(); got != nil {
			t.Errorf("ReadFlip() = %#v, want nil", got)
		}
	})
	t.Run("directory", func(t *testing.T) {
		p := newTestProject(t)
		if err := os.MkdirAll(p.FlipPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := p.ReadFlip(); got != nil {
			t.Errorf("ReadFlip() = %#v, want nil", got)
		}
	})
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
		{"unparsable is nothing", `{`, `{"paused":` + paused + `}`},
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
func TestPauseWritesBareOptionsAsTrue(t *testing.T) {
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

func TestPauseIgnoresMailboxFailures(t *testing.T) {
	p := newTestProject(t)
	writeFile(t, filepath.Join(p.Root, ".orchestrator", "mailbox"), "not a directory")
	if _, err := p.Pause(opts("pause")); err != nil {
		t.Fatalf("Pause() = %v, want the mailbox failure ignored", err)
	}
}

func TestPauseFailsWithNodesMkdirAndOpenErrors(t *testing.T) {
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
	if want := "EISDIR: illegal operation on a directory, open '" + p.FlipPath + "'"; err == nil || err.Error() != want {
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
		{"array written back", `[1,2]`, `[1,2]`},
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
			want := `{"id":"id-1","at":"` + nowISO + `","from":"conductor-opencode","to":"human","kind":"handoff","subject":"awaiting human: approval","body":"Supervisor will not relaunch until cleared: node ~/.config/opencode/orchestrator/bin/flip.mjs gate --clear (or kickoff/flip run).","refs":[".orchestrator/STATE.md"],"thread":null}` + "\n"
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

func TestGateWithBareReasonPostsNoMessage(t *testing.T) {
	p := newTestProject(t)
	answer, err := p.Gate(opts("gate", "--question", "q", "--reason"))
	if err != nil {
		t.Fatal(err)
	}
	if got := compact(t, answer); got != `{"awaiting_human":true,"door":"opencode"}` {
		t.Errorf("answer = %s", got)
	}
	if got, want := readFile(t, p.FlipPath), indented(t, `{"awaiting_human":{"at":"`+nowISO+`","door":"opencode","reason":true,"question":"q"}}`); got != want {
		t.Errorf("flip.json = %s, want %s", got, want)
	}
	if got := p.messages(t); got != "" {
		t.Errorf("mailbox = %s, want no message", got)
	}
}

func TestGateOnAPrimitiveIsTheStrictModeTypeError(t *testing.T) {
	for content, want := range map[string]string{
		`5`:      "Cannot create property 'awaiting_human' on number '5'",
		`1.5`:    "Cannot create property 'awaiting_human' on number '1.5'",
		`1e21`:   "Cannot create property 'awaiting_human' on number '1e+21'",
		`"abc"`:  "Cannot create property 'awaiting_human' on string 'abc'",
		`"a\nb"`: "Cannot create property 'awaiting_human' on string 'a\nb'",
		`true`:   "Cannot create property 'awaiting_human' on boolean 'true'",
	} {
		p := newTestProject(t)
		writeFile(t, p.FlipPath, content)
		_, err := p.Gate(opts("gate"))
		var typeError *jsjson.PropertyError
		if !errors.As(err, &typeError) || err.Error() != want {
			t.Errorf("Gate() over %s = %v, want %q", content, err, want)
		}
		if got := readFile(t, p.FlipPath); got != content {
			t.Errorf("flip.json over %s rewritten to %s", content, got)
		}
		if got := p.messages(t); got != "" {
			t.Errorf("mailbox = %s, want no message", got)
		}
	}
}

func TestGateClearDeletesTheGateAndRewritesAnyShape(t *testing.T) {
	for prev, want := range map[string]string{
		"": `{}`,
		`{"awaiting_human":{"door":"x"},"to":"c"}`: `{"to":"c"}`,
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

func TestGateReadsBeforeMakingTheStateDir(t *testing.T) {
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
		`{`:                         flip.ActionHandback,
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
