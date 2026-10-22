package watch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

var now = time.Date(2026, 10, 19, 15, 30, 45, 0, time.UTC)

type project struct {
	t          *testing.T
	root, home string
	alivePids  map[float64]bool
}

func newProject(t *testing.T) *project {
	root := filepath.Join(t.TempDir(), "demo")
	return &project{t: t, root: root, home: t.TempDir(), alivePids: map[float64]bool{}}
}

func (p *project) write(rel, content string) {
	p.t.Helper()
	file := filepath.Join(p.root, ".orchestrator", rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *project) deps() Deps {
	return Deps{
		Root:    p.root,
		Home:    p.home,
		Now:     func() time.Time { return now },
		Routing: func() (*usage.Routing, error) { return fixtureRouting(p.t), nil },
		Alive:   func(pid any) bool { n, _ := pid.(float64); return p.alivePids[n] },
	}
}

func (p *project) render() (string, error) {
	p.t.Helper()
	return Render(p.deps())
}

func fixtureRouting(t *testing.T) *usage.Routing {
	t.Helper()
	parse := func(text string) *jsjson.Object {
		v, err := jsjson.Unmarshal([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return v.(*jsjson.Object)
	}
	return &usage.Routing{
		Usage: parse(`{"generated_at":"2026-10-19T15:29:01.123Z"}`),
		Registry: parse(`{"vendors":{"anthropic":{"tiers":{"strong":{"opencode":"anthropic/claude-opus-5-5"}}},
			"openai":{"tiers":{"mid":{"opencode":"openai/gpt-6.1-sol"}}}}}`),
		Resolved: routing.Resolved{Roles: []routing.Assignment{
			{Role: "planner", Entry: &routing.RoleEntry{Vendor: "anthropic", Tier: "strong", Variant: "medium"}},
			{Role: "coder", Entry: &routing.RoleEntry{Vendor: "openai", Tier: "mid", Effort: "high", Variant: "x"}},
			{Role: "reviewer"},
		}},
		Prompt: "line one\nline two\nline three",
	}
}

const emptyProjectFrame = `== demo  2026-10-19T15:30:45Z  (usage from 15:29:01Z)
line one
line two
routing: planner=claude-opus-5-5#medium  coder=gpt-6.1-sol@high  reviewer=PAUSED
supervisor: not started
flip: never
lanes: none
mailbox (last 0):
incidents (24h): 0`

func TestEmptyProject(t *testing.T) {
	got, err := newProject(t).render()
	if err != nil || got != emptyProjectFrame {
		t.Errorf("Render = %q, %v\nwant %q", got, err, emptyProjectFrame)
	}
}

func TestEveryGroup(t *testing.T) {
	p := newProject(t)
	p.alivePids[42] = true
	p.write("supervisor-state.json", `{"status":"running","pid":41}`)
	p.write("flip.json", `{"from":"opencode","to":"claude","at":"2026-10-19T14:05:00.000Z","awaiting_human":{"reason":"push?"}}`)
	p.write("workers/a.json", `{"lane":"a","harness":"claude","status":"running","pid":42,"last_tool":"Bash","updated_at":"2026-10-19T15:00:00.000Z"}`)
	p.write("workers/b.json", `7`)
	p.write("workers/c.json", `{`)
	p.write("workers/d.json", `{"lane":"d","harness":"codex","status":"starting","pid":43}`)
	astral := strings.Repeat("a", 99) + "😀"
	p.write("mailbox/messages.jsonl", strings.Join([]string{
		`{"id":"q1234567890","at":"2026-10-19T15:10:00.000Z","from":"conductor","to":"human","kind":"question","subject":"` + astral + `"}`,
		`{"id":"q2","at":"2026-10-19T15:11:00.000Z","from":"conductor","to":"coder","kind":"question","subject":"not for you"}`,
		`{"id":"n1","at":"2026-10-19T15:12:00.000Z","from":"coder","to":"*","kind":"handoff-long","subject":"done"}`,
	}, "\n")+"\n")
	p.write("STATE.md", "# State\n## Next action (exact)\nrun the tests\n## Open\n")
	writeIncidents(t, p.home, `{"at":"2026-10-18T15:30:44.000Z","summary":"too old"}`, `{"at":"2026-10-19T10:00:00.000Z","summary":"recent `+strings.Repeat("s", 100)+`"}`)

	got, err := p.render()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"== demo  2026-10-19T15:30:45Z  (usage from 15:29:01Z)",
		"line one",
		"line two",
		"routing: planner=claude-opus-5-5#medium  coder=gpt-6.1-sol@high  reviewer=PAUSED",
		"supervisor: running pid 41 (DEAD) checked Z",
		"flip: opencode->claude at 14:05Z",
		"lanes: a[claude] running last Bash 15:00Z | undefined[undefined] undefined  Z | d[codex] starting (DEAD)  Z",
		"QUESTIONS FOR YOU (1):",
		"  [q1234567] " + strings.Repeat("a", 99) + "\uFFFD\n     reply: orch mailbox reply q1234567 \"...\"",
		"AWAITING HUMAN: push?  (clear: orch flip gate --clear)",
		"mailbox (last 3):",
		"  15:10Z question conductor -> human: " + astral,
		"  15:11Z question conductor -> coder: not for you",
		"  15:12Z handoff-long coder -> *: done",
		"incidents (24h): 1  latest: recent " + strings.Repeat("s", 83),
		"next: (exact)",
	}, "\n")
	if got != want {
		t.Errorf("Render:\n%s\nwant:\n%s", got, want)
	}
}

func writeIncidents(t *testing.T, home string, lines ...string) {
	t.Helper()
	file := incident.New(home).File
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The question's reply hint names orch.
func TestReplyHintNamesOrch(t *testing.T) {
	p := newProject(t)
	p.write("mailbox/messages.jsonl", `{"id":"q1234567890","at":"2026-10-19T15:10:00.000Z","from":"conductor","to":"human","kind":"question","subject":"ship?"}`+"\n")
	got, err := p.render()
	if err != nil {
		t.Fatal(err)
	}
	const hint = "\n     reply: orch mailbox reply q1234567 \"...\"\n"
	if !strings.Contains(got, hint) {
		t.Errorf("Render lacks %q:\n%s", hint, got)
	}
}

func TestPausedFlip(t *testing.T) {
	p := newProject(t)
	p.write("flip.json", `{"paused":{"door":"opencode","at":"2026-10-19T13:00:00.000Z"}}`)
	got, _ := p.render()
	if !strings.Contains(got, "\nflip: PAUSED (opencode, 13:00Z)\n") {
		t.Errorf("Render = %q", got)
	}
}

// The JS TypeErrors, with V8's messages.
func TestShapesTheJSThrowsOn(t *testing.T) {
	for _, c := range []struct {
		name, file, content, want string
	}{
		{"gate-only flip", "flip.json", `{"awaiting_human":{"reason":"r"}}`, "Cannot read properties of undefined (reading 'slice')"},
		{"paused at a number", "flip.json", `{"paused":{"at":5}}`, "flip.paused.at.slice is not a function"},
		{"paused at null", "flip.json", `{"paused":{"at":null}}`, "Cannot read properties of null (reading 'slice')"},
		{"message without at", "mailbox/messages.jsonl", `{"id":"x","kind":"note","subject":"s"}` + "\n", "Cannot read properties of undefined (reading 'slice')"},
		{"message without kind", "mailbox/messages.jsonl", `{"id":"x","at":"2026-10-19T15:10:00.000Z","subject":"s"}` + "\n", "Cannot read properties of undefined (reading 'padEnd')"},
		{"question without subject", "mailbox/messages.jsonl", `{"id":"x","at":"2026-10-19T15:10:00.000Z","kind":"question","to":"human"}` + "\n", "Cannot read properties of undefined (reading 'slice')"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t)
			p.write(c.file, c.content)
			_, err := p.render()
			var typeErr *jsjson.TypeError
			var propertyErr *jsjson.PropertyError
			if err == nil || err.Error() != c.want || !(errors.As(err, &typeErr) || errors.As(err, &propertyErr)) {
				t.Errorf("err = %v, want TypeError %q", err, c.want)
			}
		})
	}
}

func TestNextActionVariants(t *testing.T) {
	for _, c := range []struct{ state, want string }{
		{"## Next action\n\n  do it now  \nmore\n## Other", "next: do it now  "}, // trim applies to the section, not the line,
		{"no heading", ""},
		{"## Next action\n\n## Empty", ""},
		{"## Next action\n" + strings.Repeat("é", 200), "next: " + strings.Repeat("é", 160)},
	} {
		p := newProject(t)
		p.write("STATE.md", c.state)
		got, _ := p.render()
		last := got[strings.LastIndex(got, "\n")+1:]
		if c.want == "" && strings.HasPrefix(last, "next:") || c.want != "" && last != c.want {
			t.Errorf("state %q: last line %q, want %q", c.state, last, c.want)
		}
	}
}

func TestRoutingFailureIsARoutingError(t *testing.T) {
	d := newProject(t).deps()
	d.Routing = func() (*usage.Routing, error) { return nil, errors.New("feeds down") }
	var routingErr *RoutingError
	if _, err := Render(d); !errors.As(err, &routingErr) || err.Error() != "feeds down" {
		t.Errorf("err = %v", err)
	}
}

func TestDelayClampsAsNodeDoes(t *testing.T) {
	for _, c := range []struct {
		interval any
		want     time.Duration
	}{
		{nil, 20 * time.Second},
		{"0", time.Millisecond}, // "0" is a non-empty string, so truthy; 0 ms clamps to 1
		{"abc", time.Millisecond},
		{"-5", time.Millisecond},
		{"1e10", time.Millisecond},
		{"0.5", 500 * time.Millisecond},
		{true, time.Second},
		{"", 20 * time.Second},
	} {
		if got := Delay(c.interval); got != c.want {
			t.Errorf("Delay(%#v) = %v, want %v", c.interval, got, c.want)
		}
	}
}

func TestFollowDrawsFramesAndErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	results := []error{&jsjson.TypeError{Message: "Cannot read properties of undefined (reading 'slice')"}, nil, nil}
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	Follow(ctx, FollowDeps{
		Render: func() (string, error) {
			err := results[calls]
			calls++
			if err != nil {
				return "", err
			}
			return "frame", nil
		},
		Interval: "1",
		Out:      &out,
		Err:      &errOut,
		Sleep: func(_ context.Context, d time.Duration) {
			waits = append(waits, d)
			if len(waits) == 3 {
				cancel()
			}
		},
	})
	if errOut.String() != "Cannot read properties of undefined (reading 'slice')\n" {
		t.Errorf("stderr %q", errOut.String())
	}
	if want := strings.Repeat("\x1b[2J\x1b[Hframe\n", 2); out.String() != want {
		t.Errorf("stdout %q, want %q", out.String(), want)
	}
	if len(waits) != 3 || waits[0] != time.Second {
		t.Errorf("waits %v", waits)
	}
}
