package kickoff

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// fakeChildren records every child kickoff runs and answers with canned results.
type fakeChildren struct {
	calls       [][]string
	answers     map[string]procs.SyncResult // by the first two args joined by a space
	interactive struct {
		code *int
		err  error
	}
}

func (f *fakeChildren) Self(args []string) procs.SyncResult {
	f.calls = append(f.calls, append([]string{"self"}, args...))
	return f.answers[strings.Join(args[:2], " ")]
}

func (f *fakeChildren) SelfInherited(args []string) {
	f.calls = append(f.calls, append([]string{"inherited"}, args...))
}

func (f *fakeChildren) Interactive(name string, args []string) (*int, error) {
	f.calls = append(f.calls, append([]string{"interactive", name}, args...))
	return f.interactive.code, f.interactive.err
}

// kickoffRun is one kickoff in a temporary project with a.md and b.md and a .git directory.
type kickoffRun struct {
	root     string
	children *fakeChildren
	out, err bytes.Buffer
	routing  *usage.Routing
}

func newRun(t *testing.T, anthropic, openai string) *kickoffRun {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md", ".git/HEAD"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := jsjson.Unmarshal([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return &kickoffRun{
		root: root,
		children: &fakeChildren{answers: map[string]procs.SyncResult{
			"supervisor start": {Stdout: "  {\"started\":false}\n"},
		}},
		routing: &usage.Routing{
			Registry: registry.(*jsjson.Object),
			Resolved: routing.Resolved{Postures: routing.Postures{
				Anthropic: routing.VendorPosture{Available: anthropic != "", Overall: anthropic},
				OpenAI:    routing.VendorPosture{Available: openai != "", Overall: openai},
			}},
			Prompt: "line 1\nline 2\nline 3",
		},
	}
}

func (k *kickoffRun) run(argv ...string) (int, error) {
	return Run(ParseArgv(argv), Deps{
		Root:     k.root,
		Routing:  func() (*usage.Routing, error) { return k.routing, nil },
		Children: k.children,
		Out:      &k.out,
		Err:      &k.err,
	})
}

func (k *kickoffRun) preflightCalls(extra ...[]string) [][]string {
	return append(extra,
		[]string{"self", "flip", "gate", "--clear", "--cwd", k.root},
		[]string{"self", "supervisor", "start", "--cwd", k.root},
	)
}

func TestParseArgvTakesBriefsAsTheJSLoopsDo(t *testing.T) {
	cases := []struct {
		argv   []string
		briefs []string
		door   any
	}{
		{[]string{"--brief", "a.md", "--brief", "b.md", "--door", "claude"}, []string{"a.md", "b.md"}, "claude"},
		{[]string{"--brief", "", "--door", "claude"}, nil, "claude"},    // an empty brief is not taken, and dropped
		{[]string{"--brief", "--brief", "x"}, []string{"--brief"}, nil}, // the second --brief is the value
		{[]string{"--door", "--brief", "a.md"}, []string{"a.md"}, true}, // --door is left bare
		{[]string{"--brief"}, nil, nil},
	}
	for _, c := range cases {
		args := ParseArgv(c.argv)
		if !reflect.DeepEqual(args.Briefs, c.briefs) || args.Opts.JS("door") != c.door {
			t.Errorf("ParseArgv(%q) = briefs %q door %v, want %q %v", c.argv, args.Briefs, args.Opts.JS("door"), c.briefs, c.door)
		}
	}
}

func TestUsageWithoutBriefExits2AndWithHelpExits0(t *testing.T) {
	for _, c := range []struct {
		argv []string
		code int
	}{{nil, 2}, {[]string{"--help"}, 2}, {[]string{"--help", "--brief", "a.md"}, 0}} {
		k := newRun(t, "green", "green")
		code, err := k.run(c.argv...)
		if err != nil || code != c.code || k.out.String() != cmdref.KickoffUsage()+"\n" || len(k.children.calls) != 0 {
			t.Errorf("%q: code %d err %v out %q calls %q", c.argv, code, err, k.out.String(), k.children.calls)
		}
	}
}

func TestMissingBriefExits2AndADirectoryPasses(t *testing.T) {
	k := newRun(t, "green", "green")
	if code, _ := k.run("--brief", ".git", "--brief", "nope.md"); code != 2 || k.err.String() != "brief not found: nope.md\n" {
		t.Errorf("code %d stderr %q", code, k.err.String())
	}
}

func TestRoutingFailureIsARoutingError(t *testing.T) {
	k := newRun(t, "green", "green")
	_, err := Run(ParseArgv([]string{"--brief", "a.md"}), Deps{Root: k.root, Routing: func() (*usage.Routing, error) { return nil, errors.New("no registry") }, Out: &k.out, Err: &k.err})
	var routingErr *RoutingError
	if !errors.As(err, &routingErr) || err.Error() != "no registry" {
		t.Errorf("err = %v", err)
	}
}

func TestDryRunRunsThePreflightAndPrintsThePrompt(t *testing.T) {
	k := newRun(t, "green", "green")
	code, err := k.run("--brief", "a.md", "--brief", "b.md", "--dry-run", "--unattended")
	want := "line 1\nline 2\nsupervisor: {\"started\":false}\ndoor: opencode (openai green; home opencode) interactive\nprompt: " + cmdref.KickoffPrompt("a.md", []string{"b.md"}) + "\n"
	if err != nil || code != 0 || k.out.String() != want {
		t.Fatalf("code %d err %v out\n%s\nwant\n%s", code, err, k.out.String(), want)
	}
	if want := k.preflightCalls([]string{"self", "policy", "unattended", "--cwd", k.root}); !reflect.DeepEqual(k.children.calls, want) {
		t.Errorf("calls %q, want %q", k.children.calls, want)
	}
	if exclude, _ := os.ReadFile(filepath.Join(k.root, ".git", "info", "exclude")); string(exclude) != ".orchestrator/\n" {
		t.Errorf("exclude = %q", exclude)
	}
}

func TestDoorChoice(t *testing.T) {
	cases := []struct {
		name, anthropic, openai, home string
		argv                          []string
		want                          string
	}{
		{"home usable", "green", "amber", "", nil, "door: opencode (openai amber; home opencode)"},
		{"home red", "green", "red", "", nil, "door: claude (anthropic green; home opencode)"},
		{"home unavailable", "amber", "", "", nil, "door: claude (anthropic amber; home opencode)"},
		{"home claude", "green", "green", "claude", nil, "door: claude (anthropic green; home claude)"},
		{"given door", "red", "green", "", []string{"--door", "claude"}, "door: claude (anthropic red; home opencode)"},
		{"auto", "green", "red", "", []string{"--door", "auto"}, "door: claude (anthropic green; home opencode)"},
	}
	for _, c := range cases {
		k := newRun(t, c.anthropic, c.openai)
		if c.home != "" {
			registry, _ := jsjson.Unmarshal([]byte(`{"conductor":{"home":"` + c.home + `"}}`))
			k.routing.Registry = registry.(*jsjson.Object)
		}
		code, err := k.run(append([]string{"--brief", "a.md", "--dry-run"}, c.argv...)...)
		if err != nil || code != 0 || !strings.Contains(k.out.String(), c.want+" interactive\n") {
			t.Errorf("%s: code %d err %v out\n%s", c.name, code, err, k.out.String())
		}
	}
}

func TestUnknownDoorCrashesAfterThePreflight(t *testing.T) {
	for _, door := range [][]string{{"--door", "gemini"}, {"--door"}} {
		k := newRun(t, "green", "green")
		_, err := k.run(append([]string{"--brief", "a.md", "--dry-run"}, door...)...)
		var typeErr *jsjson.TypeError
		if !errors.As(err, &typeErr) || err.Error() != "Cannot read properties of undefined (reading 'overall')" {
			t.Errorf("%q: err = %v", door, err)
		}
		if !reflect.DeepEqual(k.children.calls, k.preflightCalls()) {
			t.Errorf("%q: calls %q", door, k.children.calls)
		}
	}
}

func TestBothRed(t *testing.T) {
	k := newRun(t, "red", "")
	code, err := k.run("--brief", "a.md", "--dry-run")
	if err != nil || code != exitBothRed || len(k.children.calls) != 0 ||
		k.out.String() != "line 1\nline 2\ndry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset.\n" {
		t.Errorf("dry run: code %d err %v calls %q out %q", code, err, k.children.calls, k.out.String())
	}
	k = newRun(t, "red", "red")
	code, err = k.run("--brief", "a.md", "--headless")
	if want := [][]string{{"inherited", "flip", "pause", "--active", "opencode", "--cwd", k.root}}; err != nil || code != exitBothRed || !reflect.DeepEqual(k.children.calls, want) ||
		!strings.HasSuffix(k.out.String(), "\nBoth vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do.\n") {
		t.Errorf("real: code %d err %v calls %q out %q", code, err, k.children.calls, k.out.String())
	}
}

func TestHeadlessStartsADetachedConductorLane(t *testing.T) {
	for _, c := range []struct {
		door  string
		extra []string
	}{{"opencode", nil}, {"claude", []string{"--permission-mode", "auto"}}} {
		k := newRun(t, "green", "green")
		status := 4
		k.children.answers["worker start"] = procs.SyncResult{Status: &status, Stderr: " launched \n"}
		code, err := k.run("--brief", "a.md", "--headless", "--door", c.door)
		if err != nil || code != 4 {
			t.Fatalf("%s: code %d err %v", c.door, code, err)
		}
		worker := append([]string{"self", "worker", "start", "--brief", ".orchestrator/briefs/conductor-kickoff.md", "--lane", "conductor", "--harness", c.door, "--agent", "conductor", "--breadth", "broad", "--cwd", k.root, "--detach"}, c.extra...)
		if want := append(k.preflightCalls(), worker); !reflect.DeepEqual(k.children.calls, want) {
			t.Errorf("%s: calls %q, want %q", c.door, k.children.calls, want)
		}
		if !strings.HasSuffix(k.out.String(), " headless\nlaunched\nwatch with: "+cmdref.WatchFollow()+"\n") {
			t.Errorf("%s: out %q", c.door, k.out.String())
		}
		if brief, _ := os.ReadFile(filepath.Join(k.root, ".orchestrator", "briefs", "conductor-kickoff.md")); string(brief) != cmdref.KickoffPrompt("a.md", nil)+"\n" {
			t.Errorf("%s: brief %q", c.door, brief)
		}
	}
}

func TestHeadlessWithoutAWorkerStatusExits1(t *testing.T) {
	k := newRun(t, "green", "green")
	if code, err := k.run("--brief", "a.md", "--headless"); err != nil || code != 1 {
		t.Errorf("code %d err %v", code, err)
	}
}

func TestInteractiveLaunchesTheDoorsTUI(t *testing.T) {
	three := 3
	prompt := cmdref.KickoffPrompt("a.md", nil)
	for _, c := range []struct {
		door string
		code *int
		want int
		call []string
	}{
		{"claude", &three, 3, []string{"interactive", "claude", "--agent", "conductor", prompt}},
		{"opencode", nil, 0, []string{"interactive", "opencode", "--prompt", prompt}}, // a signal exits 0
	} {
		k := newRun(t, "green", "green")
		k.children.interactive.code = c.code
		code, err := k.run("--brief", "a.md", "--door", c.door)
		if err != nil || code != c.want || !reflect.DeepEqual(k.children.calls[len(k.children.calls)-1], c.call) {
			t.Errorf("%s: code %d err %v calls %q", c.door, code, err, k.children.calls)
		}
		if want := "launching: " + c.door + " ... (watch in another terminal: " + cmdref.WatchFollow() + ")\n"; !strings.HasSuffix(k.out.String(), want) {
			t.Errorf("%s: out %q", c.door, k.out.String())
		}
	}
}

func TestInteractiveStartFailureIsTheError(t *testing.T) {
	k := newRun(t, "green", "green")
	k.children.interactive.err = &procs.StartError{Name: "opencode", Code: "ENOENT", Err: os.ErrNotExist}
	if _, err := k.run("--brief", "a.md"); err == nil || err.Error() != "spawn opencode ENOENT" {
		t.Errorf("err = %v", err)
	}
}
