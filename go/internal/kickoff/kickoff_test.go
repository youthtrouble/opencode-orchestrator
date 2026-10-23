package kickoff

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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
	inherited   *int                        // every SelfInherited's exit status
	interactive struct {
		code *int
		err  error
	}
}

func (f *fakeChildren) Self(args []string) procs.SyncResult {
	f.calls = append(f.calls, append([]string{"self"}, args...))
	return f.answers[strings.Join(args[:2], " ")]
}

func (f *fakeChildren) SelfInherited(args []string) *int {
	f.calls = append(f.calls, append([]string{"inherited"}, args...))
	return f.inherited
}

func exitStatus(code int) *int { return &code }

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
			"flip gate":        {Status: exitStatus(0), Stdout: "{\"cleared\":false}\n"},
			"supervisor start": {Status: exitStatus(0), Stdout: "  {\"started\":false}\n"},
		}, inherited: exitStatus(0)},
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
		[]string{"self", "flip", "gate", "--clear", "--no-relaunch", "--cwd", k.root},
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
	if code, _ := k.run("--brief", ".git", "--brief", "nope.md"); code != 2 || k.err.String() != "brief not found: nope.md (looked in "+k.root+"; cd into the project or pass --cwd <project dir>)\n" {
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

func TestDryRunChangesNothingAndPrintsThePrompt(t *testing.T) {
	k := newRun(t, "green", "green")
	code, err := k.run("--brief", "a.md", "--brief", "b.md", "--headless", "--dry-run", "--unattended")
	want := "line 1\nline 2\nsupervisor: not started (dry-run)\ndoor: opencode (openai green; home opencode) headless\nprompt: " + cmdref.KickoffPrompt("a.md", []string{"b.md"}) + "\n"
	if err != nil || code != 0 || k.out.String() != want {
		t.Fatalf("code %d err %v out\n%s\nwant\n%s", code, err, k.out.String(), want)
	}
	if len(k.children.calls) != 0 {
		t.Errorf("calls %q, want none", k.children.calls)
	}
	for _, path := range []string{filepath.Join(".git", "info"), ".orchestrator"} {
		if _, err := os.Stat(filepath.Join(k.root, path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: stat err %v, want it never created", path, err)
		}
	}
}

// A first kickoff has no .orchestrator/ to clear a gate in: the clear's not-a-project exit 2
// is nothing to stop for.
func TestPreflightRunsBeforeTheLaunch(t *testing.T) {
	k := newRun(t, "green", "green")
	k.children.answers["flip gate"] = procs.SyncResult{Status: exitStatus(2), Stderr: "not an orchestrator project: " + k.root + " has no .orchestrator/ directory; cd into the project or pass --cwd <project dir>\n"}
	k.children.interactive.code = new(int)
	if code, err := k.run("--brief", "a.md", "--unattended"); err != nil || code != 0 {
		t.Fatalf("code %d err %v", code, err)
	}
	if !strings.Contains(k.out.String(), "\nsupervisor: {\"started\":false}\n") {
		t.Errorf("out %q, want the supervisor's answer", k.out.String())
	}
	want := append(k.preflightCalls([]string{"self", "policy", "unattended", "--cwd", k.root}), []string{"interactive", "opencode", "--prompt", cmdref.KickoffPrompt("a.md", nil)})
	if !reflect.DeepEqual(k.children.calls, want) {
		t.Errorf("calls %q, want %q", k.children.calls, want)
	}
	if exclude, _ := os.ReadFile(filepath.Join(k.root, ".git", "info", "exclude")); string(exclude) != ".orchestrator/\n" {
		t.Errorf("exclude = %q", exclude)
	}
}

// A refused gate clear (an unreadable flip.json since S13d, a crash, a clear that gave no
// status, or exit 2 while .orchestrator/ exists) stops every real kickoff right after it: no
// supervisor, no exclude, no brief, no pause and no launch (PE-61).
func TestARefusedGateClearStopsTheKickoff(t *testing.T) {
	unreadable := procs.SyncResult{Status: exitStatus(1), Stderr: "\nError: flip state unreadable: /p/.orchestrator/flip.json: EJSON\nmore\n"}
	refusals := []struct {
		name          string
		clear         procs.SyncResult
		orchestrator  bool // .orchestrator/ exists before the run
		wantDiagnosis string
	}{
		{"unreadable", unreadable, true, "Error: flip state unreadable: /p/.orchestrator/flip.json: EJSON"},
		{"exit 2 in a project", procs.SyncResult{Status: exitStatus(2), Stdout: " usage \n"}, true, "usage"},
		{"other code", procs.SyncResult{Status: exitStatus(3)}, false, "exit status 3"},
		{"no status", procs.SyncResult{Err: errors.New("no executable")}, false, "no executable"},
		{"signal", procs.SyncResult{}, false, "no exit status"},
	}
	modes := []struct {
		name, anthropic string
		argv            []string
	}{
		{"headless", "green", []string{"--headless"}},
		{"interactive", "green", nil},
		{"both red", "red", []string{"--unattended"}},
	}
	for _, refusal := range refusals {
		for _, mode := range modes {
			t.Run(refusal.name+"/"+mode.name, func(t *testing.T) {
				k := newRun(t, mode.anthropic, mode.anthropic)
				if refusal.orchestrator {
					if err := os.Mkdir(filepath.Join(k.root, ".orchestrator"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				k.children.answers["flip gate"] = refusal.clear
				k.children.interactive.code = new(int)
				code, err := k.run(append([]string{"--brief", "a.md"}, mode.argv...)...)
				if err != nil || code != 1 {
					t.Fatalf("code %d err %v, want 1", code, err)
				}
				want := "kickoff: the human gate could not be cleared: " + refusal.wantDiagnosis + "; nothing was launched\n"
				if k.err.String() != want {
					t.Errorf("stderr %q, want %q", k.err.String(), want)
				}
				calls := k.preflightCalls()[:1]
				if mode.anthropic == "red" {
					calls = append([][]string{{"self", "policy", "unattended", "--cwd", k.root}}, calls...)
				}
				if !reflect.DeepEqual(k.children.calls, calls) {
					t.Errorf("calls %q, want %q", k.children.calls, calls)
				}
				for _, path := range []string{filepath.Join(".git", "info"), filepath.Join(".orchestrator", "briefs")} {
					if _, err := os.Stat(filepath.Join(k.root, path)); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("%s: stat err %v, want it never created", path, err)
					}
				}
			})
		}
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

func TestInvalidDoorIsUsageErrorBeforeAnyWrite(t *testing.T) {
	for _, door := range [][]string{{"--door", "gemini"}, {"--door"}, {"--door", ""}} {
		k := newRun(t, "green", "green")
		code, err := k.run(append([]string{"--brief", "a.md"}, door...)...)
		if err != nil || code != 2 || !strings.Contains(k.err.String(), "opencode|claude|auto") {
			t.Errorf("%q: code %d err %v stderr %q", door, code, err, k.err.String())
		}
		if len(k.children.calls) != 0 {
			t.Errorf("%q: calls %q", door, k.children.calls)
		}
		if _, err := os.Stat(filepath.Join(k.root, ".orchestrator")); !os.IsNotExist(err) {
			t.Errorf("project changed: %v", err)
		}
	}
}

func TestBothRedDryRunOnlySaysSo(t *testing.T) {
	k := newRun(t, "red", "")
	code, err := k.run("--brief", "a.md", "--dry-run")
	if err != nil || code != exitBothRed || len(k.children.calls) != 0 ||
		k.out.String() != "line 1\nline 2\ndry-run: both vendors red; a real run would pause and let the supervisor relaunch at the first reset.\n" {
		t.Errorf("code %d err %v calls %q out %q", code, err, k.children.calls, k.out.String())
	}
}

// A real both-red run clears the gate (which drops any older pause) before it records its own
// pause, and names the saved prompt as the brief the supervisor's relaunch starts from.
func TestBothRedRunsThePreflightSavesTheBriefAndPausesWithIt(t *testing.T) {
	for _, unattended := range []bool{false, true} {
		t.Run(fmt.Sprintf("unattended=%v", unattended), func(t *testing.T) {
			k := newRun(t, "red", "red")
			argv := []string{"--brief", "a.md", "--brief", "b.md", "--headless"}
			var policy [][]string
			if unattended {
				argv = append(argv, "--unattended")
				policy = [][]string{{"self", "policy", "unattended", "--cwd", k.root}}
			}
			code, err := k.run(argv...)
			if err != nil || code != exitBothRed {
				t.Fatalf("code %d err %v", code, err)
			}
			pause := []string{"inherited", "flip", "pause", "--active", "opencode", "--brief", ".orchestrator/briefs/conductor-kickoff.md", "--cwd", k.root}
			if want := append(k.preflightCalls(policy...), pause); !reflect.DeepEqual(k.children.calls, want) {
				t.Errorf("calls %q, want %q", k.children.calls, want)
			}
			wantOut := "line 1\nline 2\nsupervisor: {\"started\":false}\nBoth vendors are red: paused. The supervisor will relaunch headless at the first reset. Nothing else to do.\n"
			if k.out.String() != wantOut {
				t.Errorf("out %q, want %q", k.out.String(), wantOut)
			}
			brief, _ := os.ReadFile(filepath.Join(k.root, ".orchestrator", "briefs", "conductor-kickoff.md"))
			if want := cmdref.KickoffPrompt("a.md", []string{"b.md"}) + "\n"; string(brief) != want {
				t.Errorf("brief %q, want %q", brief, want)
			}
			if exclude, _ := os.ReadFile(filepath.Join(k.root, ".git", "info", "exclude")); string(exclude) != ".orchestrator/\n" {
				t.Errorf("exclude = %q", exclude)
			}
		})
	}
}

func TestBothRedPreflightFailureIsReturnedBeforeAnyPause(t *testing.T) {
	k := newRun(t, "red", "red")
	if err := os.MkdirAll(filepath.Join(k.root, ".git", "info", "exclude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := k.run("--brief", "a.md"); err == nil || !strings.HasPrefix(err.Error(), "EISDIR") {
		t.Errorf("err = %v, want the exclude's EISDIR", err)
	}
	if !reflect.DeepEqual(k.children.calls, k.preflightCalls()) {
		t.Errorf("calls %q, want the preflight only", k.children.calls)
	}
	if _, err := os.Stat(filepath.Join(k.root, ".orchestrator", "briefs", "conductor-kickoff.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("brief written (%v)", err)
	}
}

// Exit 75 tells the caller (/away) that the supervisor will start the run at the reset, so a
// supervisor that did not start fails the run before the brief is written or a pause recorded.
func TestBothRedWithoutASupervisorFailsBeforeThePause(t *testing.T) {
	for name, answer := range map[string]procs.SyncResult{
		"exit 1":  {Status: exitStatus(1), Stderr: "Error: EEXIST\n"},
		"signal":  {},
		"no orch": {Err: errors.New("no executable")},
	} {
		t.Run(name, func(t *testing.T) {
			k := newRun(t, "red", "red")
			k.children.answers["supervisor start"] = answer
			code, err := k.run("--brief", "a.md")
			if err != nil || code != 1 {
				t.Fatalf("code %d err %v, want 1", code, err)
			}
			if !reflect.DeepEqual(k.children.calls, k.preflightCalls()) {
				t.Errorf("calls %q, want the preflight only", k.children.calls)
			}
			want := "Both vendors are red, but the supervisor did not start: nothing will relaunch at the reset. Fix it and run kickoff again.\n"
			if k.err.String() != want || strings.Contains(k.out.String(), "paused") {
				t.Errorf("err %q out %q, want %q and no pause message", k.err.String(), k.out.String(), want)
			}
			if _, err := os.Stat(filepath.Join(k.root, ".orchestrator", "briefs", "conductor-kickoff.md")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("brief written (%v)", err)
			}
		})
	}
}

func TestBothRedWithoutARecordedPauseFails(t *testing.T) {
	for name, status := range map[string]*int{"exit 1": exitStatus(1), "signal or no status": nil} {
		t.Run(name, func(t *testing.T) {
			k := newRun(t, "red", "red")
			k.children.inherited = status
			code, err := k.run("--brief", "a.md")
			if err != nil || code != 1 {
				t.Fatalf("code %d err %v, want 1", code, err)
			}
			want := "Both vendors are red, but the pause was not recorded: nothing will relaunch at the reset. Fix it and run kickoff again.\n"
			if k.err.String() != want || strings.Contains(k.out.String(), "paused") {
				t.Errorf("err %q out %q, want %q and no pause message", k.err.String(), k.out.String(), want)
			}
		})
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

// The brief is owner-only and replaces a planted symlink instead of writing through it
// (PE-21: the JS writes the symlink's target).
func TestHeadlessBriefIsPrivateAndReplacesASymlink(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	k := newRun(t, "green", "green")
	brief := filepath.Join(k.root, ".orchestrator", "briefs", "conductor-kickoff.md")
	target := filepath.Join(k.root, "elsewhere")
	if err := os.WriteFile(target, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(brief), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, brief); err != nil {
		t.Fatal(err)
	}
	if _, err := k.run("--brief", "a.md", "--headless"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(brief)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("brief %v, want a regular 0600 file", info.Mode())
	}
	if data, _ := os.ReadFile(target); string(data) != "untouched" {
		t.Errorf("symlink target %q", data)
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
