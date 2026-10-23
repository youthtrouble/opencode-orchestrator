package flipcmd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// fakeLauncher records each launch and answers with a fixed status and outputs.
type fakeLauncher struct {
	status         *int
	stdout, stderr string
	calls          [][]string
}

func (f *fakeLauncher) launch(args []string) (*int, string, string) {
	f.calls = append(f.calls, args)
	return f.status, f.stdout, f.stderr
}

func exitStatus(code int) *int { return &code }

// launched is a launcher that started the conductor.
func launched() *fakeLauncher {
	return &fakeLauncher{status: exitStatus(0), stdout: `{"lane":"conductor","runner_pid":42}` + "\n"}
}

// laneLocked is the launcher's refusal when the calling conductor holds the lane.
func laneLocked() *fakeLauncher {
	return &fakeLauncher{status: exitStatus(3), stderr: `{"error":"lane locked","lane":"conductor","previous":null}` + "\n"}
}

// newGitProject is a test project with a .git directory.
func newGitProject(t *testing.T) Project {
	t.Helper()
	p := newTestProject(t)
	mkdir(t, filepath.Join(p.Root, ".git"))
	return p
}

func (p Project) briefPath() string {
	return filepath.Join(p.Root, ".orchestrator", "briefs", "conductor-continue.md")
}

func TestRunRefusesAToThatIsNotADoorBeforeWriting(t *testing.T) {
	for _, argv := range [][]string{{"run"}, {"run", "--to", "gemini"}, {"run", "--to"}, {"run", "--to", ""}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			p := newGitProject(t)
			launcher := launched()
			if _, _, err := p.Run(opts(argv...), launcher.launch); !errors.Is(err, ErrToRequired) {
				t.Fatalf("err = %v, want ErrToRequired", err)
			}
			if entries, _ := os.ReadDir(filepath.Join(p.Root, ".git")); len(entries) != 0 || len(launcher.calls) != 0 {
				t.Errorf("wrote %v or launched %v", entries, launcher.calls)
			}
			if _, err := os.Stat(filepath.Join(p.Root, ".orchestrator")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf(".orchestrator created (%v)", err)
			}
		})
	}
}

func TestRunLaunchesTheConductorWorker(t *testing.T) {
	base := func(p Project, door string) []string {
		return []string{"start", "--brief", ".orchestrator/briefs/conductor-continue.md", "--lane", "conductor", "--harness", door,
			"--agent", "conductor", "--breadth", "broad", "--cwd", p.Root, "--detach"}
	}
	for _, c := range []struct {
		name string
		argv []string
		tail []string // after base
		door string
	}{
		{"claude", []string{"--to", "claude"}, []string{"--permission-mode", "auto"}, "claude"},
		{"opencode", []string{"--to", "opencode"}, nil, "opencode"},
		{"claude resume", []string{"--to", "claude", "--resume", "S1"}, []string{"--permission-mode", "auto", "--resume", "S1"}, "claude"},
		{"opencode bare resume", []string{"--to", "opencode", "--resume"}, []string{"--resume", "true"}, "opencode"},
		{"claude force", []string{"--to", "claude", "--force"}, []string{"--permission-mode", "auto", "--force"}, "claude"},
		{"opencode resume force", []string{"--to", "opencode", "--force", "--resume", "S2"}, []string{"--resume", "S2", "--force"}, "opencode"},
		{"empty resume", []string{"--to", "opencode", "--resume", ""}, nil, "opencode"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newGitProject(t)
			launcher := launched()
			answer, code, err := p.Run(opts(append([]string{"run"}, c.argv...)...), launcher.launch)
			if err != nil {
				t.Fatal(err)
			}
			want := append(base(p, c.door), c.tail...)
			if len(launcher.calls) != 1 || !slices.Equal(launcher.calls[0], want) {
				t.Errorf("launches %q, want one of %q", launcher.calls, want)
			}
			other := map[string]string{"claude": "opencode", "opencode": "claude"}[c.door]
			wantAnswer := `{"flipped":true,"to":"` + c.door + `","from":"` + other + `","launcher":"{\"lane\":\"conductor\",\"runner_pid\":42}"}`
			if got := compact(t, answer); got != wantAnswer || code != 0 {
				t.Errorf("answer %s exit %d, want %s exit 0", got, code, wantAnswer)
			}
		})
	}
}

func TestRunWritesExcludeBriefRecordAndHandoff(t *testing.T) {
	p := newGitProject(t)
	answer, code, err := p.Run(opts("run", "--to", "claude", "--from", "opencode", "--reason", "openai red"), launched().launch)
	if err != nil || code != 0 || compact(t, answer) == "" {
		t.Fatalf("Run = %v, %d, %v", answer, code, err)
	}
	if got := readFile(t, filepath.Join(p.Root, ".git", "info", "exclude")); got != ".orchestrator/\n" {
		t.Errorf("exclude = %q", got)
	}
	if got, want := readFile(t, p.briefPath()), cmdref.ConductorContinueBrief("claude", "opencode", "openai red"); got != want {
		t.Errorf("brief = %q, want %q", got, want)
	}
	wantFlip := indented(t, `{"at":"`+nowISO+`","from":"opencode","to":"claude","reason":"openai red","previous":null}`)
	if got := readFile(t, p.FlipPath); got != wantFlip {
		t.Errorf("flip.json = %q, want %q", got, wantFlip)
	}
	wantMessage := `{"id":"id-1","at":"` + nowISO + `","from":"conductor-opencode","to":"conductor-claude","kind":"handoff","subject":"conductor flip opencode -> claude",` +
		`"body":"openai red","refs":[".orchestrator/STATE.md",".orchestrator/briefs/conductor-continue.md"],"thread":null}` + "\n"
	if got := p.messages(t); got != wantMessage {
		t.Errorf("messages = %q, want %q", got, wantMessage)
	}
}

// The launched conductor is headless, so its brief must tell it to stop when decide answers gate.
func TestRunBriefStopsTheConductorAtAHumanGate(t *testing.T) {
	p := newGitProject(t)
	if _, code, err := p.Run(opts("run", "--to", "claude"), launched().launch); err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if brief := readFile(t, p.briefPath()); !strings.Contains(brief, "If it says gate: the project is waiting for the human; update STATE.md and stop") {
		t.Errorf("the continue brief has no gate instruction:\n%s", brief)
	}
}

// In a linked worktree, whose .git is a file, the exclude goes to the common dir (Sol R13).
func TestRunExcludesInALinkedWorktree(t *testing.T) {
	p := newTestProject(t)
	writeFile(t, filepath.Join(p.Root, ".git"), "gitdir: .gitmeta/worktrees/w\n")
	writeFile(t, filepath.Join(p.Root, ".gitmeta", "worktrees", "w", "commondir"), "../..\n")
	if answer, code, err := p.Run(opts("run", "--to", "claude"), launched().launch); err != nil || code != 0 {
		t.Fatalf("Run = %v, %d, %v", answer, code, err)
	}
	if got := readFile(t, filepath.Join(p.Root, ".gitmeta", "info", "exclude")); got != ".orchestrator/\n" {
		t.Errorf("exclude = %q", got)
	}
}

// kickoffBrief is the brief a both-red kickoff saves and names in its pause.
const kickoffBrief = ".orchestrator/briefs/conductor-kickoff.md"

func (p Project) saveKickoffBrief(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(p.Root, kickoffBrief), "Ship a.md.\n")
}

// The supervisor's relaunch of a both-red kickoff starts the conductor from the brief the
// pause named, not from a generic continue brief.
func TestRunLaunchesTheBriefAPauseNamed(t *testing.T) {
	p := newGitProject(t)
	p.saveKickoffBrief(t)
	writeFile(t, p.FlipPath, `{"paused":{"at":"x","door":"opencode","reason":"r","brief":".orchestrator/briefs/conductor-kickoff.md"}}`)
	launcher := launched()
	if _, code, err := p.Run(opts("run", "--to", "claude", "--from", "opencode"), launcher.launch); err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	want := [][]string{{"start", "--brief", ".orchestrator/briefs/conductor-kickoff.md", "--lane", "conductor", "--harness", "claude",
		"--agent", "conductor", "--breadth", "broad", "--cwd", p.Root, "--detach", "--permission-mode", "auto"}}
	if !slices.EqualFunc(launcher.calls, want, slices.Equal[[]string]) {
		t.Errorf("launched %q, want %q", launcher.calls, want)
	}
	if _, err := os.Stat(p.briefPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("continue brief written (%v)", err)
	}
	if got := p.messages(t); !strings.Contains(got, `"refs":[".orchestrator/STATE.md",".orchestrator/briefs/conductor-kickoff.md"]`) {
		t.Errorf("messages = %s, want refs naming the kickoff brief", got)
	}
	if got, want := readFile(t, p.FlipPath), indented(t, `{"at":"`+nowISO+`","from":"opencode","to":"claude","reason":null,"previous":{}}`); got != want {
		t.Errorf("flip.json = %s, want %s", got, want)
	}
}

// A lane-locked launch keeps the kickoff's brief in pending_launch, so the supervisor's retry
// (`flip run` once the lane is free) still starts from it and never from the continue brief.
func TestRunKeepsTheKickoffBriefAcrossALaneLockedRetry(t *testing.T) {
	p := newGitProject(t)
	p.saveKickoffBrief(t)
	writeFile(t, p.FlipPath, `{"paused":{"at":"x","door":"opencode","reason":"r","brief":".orchestrator/briefs/conductor-kickoff.md"}}`)
	locked := laneLocked()
	if answer, _, err := p.Run(opts("run", "--to", "opencode", "--from", "opencode"), locked.launch); err != nil || !strings.Contains(compact(t, answer), `"pending":true`) {
		t.Fatalf("first Run = %v, %v; want pending", answer, err)
	}
	if got := readFile(t, p.FlipPath); !strings.Contains(got, `"brief": ".orchestrator/briefs/conductor-kickoff.md"`) {
		t.Fatalf("flip.json = %s, want the pending launch to keep the kickoff brief", got)
	}
	retry := launched()
	if _, code, err := p.Run(opts("run", "--to", "opencode", "--from", "opencode", "--reason", "pending launch"), retry.launch); err != nil || code != 0 {
		t.Fatalf("retry Run = %d, %v", code, err)
	}
	for i, call := range append(locked.calls, retry.calls...) {
		if call[2] != kickoffBrief {
			t.Errorf("launch %d briefed %q, want %q", i+1, call[2], kickoffBrief)
		}
	}
	if _, err := os.Stat(p.briefPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("continue brief written (%v)", err)
	}
	if got := readFile(t, p.FlipPath); strings.Contains(got, "pending_launch") {
		t.Errorf("flip.json = %s, want the launched retry to drop the pending launch", got)
	}
}

// Only a non-empty string in an object record's paused.brief or pending_launch.brief, naming a
// file that exists, is launched; anything else starts from the continue brief.
func TestRunWritesTheContinueBriefWithoutASavedBrief(t *testing.T) {
	for _, prev := range []string{`{"paused":{"door":"opencode"}}`, `{"paused":{"brief":""}}`, `{"paused":{"brief":true}}`, `"paused"`, `[1]`,
		`{"paused":{"brief":"gone.md"}}`, `{"pending_launch":{"to":"opencode","brief":"gone.md"}}`} {
		t.Run(prev, func(t *testing.T) {
			p := newGitProject(t)
			p.saveKickoffBrief(t) // present, but not named
			writeFile(t, p.FlipPath, prev)
			launcher := launched()
			if _, _, err := p.Run(opts("run", "--to", "opencode"), launcher.launch); err != nil {
				t.Fatal(err)
			}
			if len(launcher.calls) != 1 || launcher.calls[0][2] != ".orchestrator/briefs/conductor-continue.md" {
				t.Errorf("launched %q", launcher.calls)
			}
			if _, err := os.Stat(p.briefPath()); err != nil {
				t.Errorf("continue brief: %v", err)
			}
		})
	}
}

// Internal callers retain JS option types; the CLI rejects bare value options before Run.
func TestRunInternalOptionValuesRetainJSTypes(t *testing.T) {
	pendingFlip := func(from, reason, resume string) string {
		return `{"at":"` + nowISO + `","from":` + from + `,"to":"claude","reason":` + reason + `,"previous":null,` +
			`"pending_launch":{"to":"claude","from":` + from + `,"reason":` + reason + `,"resume":` + resume + `,"at":"` + nowISO + `"}}`
	}
	for _, c := range []struct {
		bare, from, flip, brief, message string
	}{
		{"--from", "true", pendingFlip("true", "null", "null"), cmdref.ConductorContinueBrief("claude", "true", "vendor budget flip"),
			`"from":"conductor-true","to":"conductor-claude","kind":"handoff","subject":"conductor flip true -> claude","body":"vendor budget flip",`},
		{"--reason", `"opencode"`, pendingFlip(`"opencode"`, "true", "null"), cmdref.ConductorContinueBrief("claude", "opencode", "true"),
			`"subject":"conductor flip opencode -> claude","body":true,`},
		{"--resume", `"opencode"`, pendingFlip(`"opencode"`, "null", "true"), cmdref.ConductorContinueBrief("claude", "opencode", "vendor budget flip"),
			`"body":"vendor budget flip",`},
	} {
		t.Run(c.bare, func(t *testing.T) {
			p := newGitProject(t)
			answer, code, err := p.Run(opts("run", "--to", "claude", c.bare), laneLocked().launch)
			if err != nil {
				t.Fatal(err)
			}
			wantAnswer := `{"flipped":false,"pending":true,"to":"claude","from":` + c.from + `,"note":"` + pendingNote + `"}`
			if got := compact(t, answer); got != wantAnswer || code != 0 {
				t.Errorf("answer %s exit %d, want %s exit 0", got, code, wantAnswer)
			}
			if got, want := readFile(t, p.FlipPath), indented(t, c.flip); got != want {
				t.Errorf("flip.json = %q, want %q", got, want)
			}
			if got := readFile(t, p.briefPath()); got != c.brief {
				t.Errorf("brief = %q, want %q", got, c.brief)
			}
			if got := p.messages(t); !strings.Contains(got, c.message) {
				t.Errorf("messages = %q, want it to hold %q", got, c.message)
			}
		})
	}
}

func TestRunRecordsThePreviousFlip(t *testing.T) {
	for _, c := range []struct {
		name, flip, want string
	}{
		{"none", "", `null`},
		{"record", `{"at":"2026-01-01T00:00:00.000Z","from":"claude","to":"opencode","reason":null,"previous":null}`, `{"at":"2026-01-01T00:00:00.000Z","to":"opencode"}`},
		{"paused only", `{"paused":{"at":"2026-01-01T00:00:00.000Z"}}`, `{}`},
		{"to before at", `{"to":"claude","at":"x"}`, `{"at":"x","to":"claude"}`},
		{"null at", `{"at":null}`, `{"at":null}`},
		{"odd values", `{"at":5,"to":true}`, `{"at":5,"to":true}`},
		{"string", `"abc"`, `{}`},
		{"array", `[1]`, `{}`},
		{"number", `5`, `{}`},
		{"zero", `0`, `null`},
		{"json null", `null`, `null`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newGitProject(t)
			if c.flip != "" {
				writeFile(t, p.FlipPath, c.flip)
			}
			if _, _, err := p.Run(opts("run", "--to", "claude"), launched().launch); err != nil {
				t.Fatal(err)
			}
			want := indented(t, `{"at":"`+nowISO+`","from":"opencode","to":"claude","reason":null,"previous":`+c.want+`}`)
			if got := readFile(t, p.FlipPath); got != want {
				t.Errorf("flip.json = %q, want %q", got, want)
			}
		})
	}
}

func TestRunAnswersFromTheLauncherOutcome(t *testing.T) {
	for _, c := range []struct {
		name     string
		launcher *fakeLauncher
		want     string
		code     int
		pending  bool
	}{
		{"locked", laneLocked(), `{"flipped":false,"pending":true,"to":"claude","from":"opencode","note":"` + pendingNote + `"}`, 0, true},
		{"locked by a signal", &fakeLauncher{stderr: "lane locked"}, `{"flipped":false,"pending":true,"to":"claude","from":"opencode","note":"` + pendingNote + `"}`, 0, true},
		{"stdout wins over a locked stderr", &fakeLauncher{status: exitStatus(3), stdout: "x", stderr: "lane locked"},
			`{"flipped":false,"to":"claude","from":"opencode","launcher":"x"}`, 1, false},
		{"lane locked on exit 0 is a launch", &fakeLauncher{status: exitStatus(0), stdout: "lane locked"},
			`{"flipped":true,"to":"claude","from":"opencode","launcher":"lane locked"}`, 0, false},
		{"failure", &fakeLauncher{status: exitStatus(1), stderr: "Error: EEXIST\n"},
			`{"flipped":false,"to":"claude","from":"opencode","launcher":"Error: EEXIST"}`, 1, false},
		{"start failure", &fakeLauncher{}, `{"flipped":false,"to":"claude","from":"opencode","launcher":""}`, 1, false},
		{"JS white space trimmed", &fakeLauncher{status: exitStatus(0), stdout: "\u00a0\ufeff\v ok \u2028\u3000"},
			`{"flipped":true,"to":"claude","from":"opencode","launcher":"ok"}`, 0, false},
		// PE-1: the cut splits the emoji; the JS keeps its lone high surrogate.
		{"400-unit cut", &fakeLauncher{status: exitStatus(0), stdout: strings.Repeat("a", 399) + "😀b"},
			`{"flipped":true,"to":"claude","from":"opencode","launcher":"` + strings.Repeat("a", 399) + "\uFFFD" + `"}`, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newGitProject(t)
			answer, code, err := p.Run(opts("run", "--to", "claude", "--reason", "R", "--resume", "S"), c.launcher.launch)
			if err != nil {
				t.Fatal(err)
			}
			if got := compact(t, answer); got != c.want || code != c.code {
				t.Errorf("answer %s exit %d, want %s exit %d", got, code, c.want, c.code)
			}
			pending := strings.Contains(readFile(t, p.FlipPath), `"pending_launch": {`)
			if pending != c.pending {
				t.Errorf("pending_launch written %v, want %v", pending, c.pending)
			}
		})
	}
}

func TestRunPendingLaunchCarriesReasonAndResume(t *testing.T) {
	p := newGitProject(t)
	if _, _, err := p.Run(opts("run", "--to", "opencode", "--from", "claude", "--reason", "R", "--resume", "S"), laneLocked().launch); err != nil {
		t.Fatal(err)
	}
	want := indented(t, `{"at":"`+nowISO+`","from":"claude","to":"opencode","reason":"R","previous":null,`+
		`"pending_launch":{"to":"opencode","from":"claude","reason":"R","resume":"S","at":"`+nowISO+`"}}`)
	if got := readFile(t, p.FlipPath); got != want {
		t.Errorf("flip.json = %q, want %q", got, want)
	}
}

func TestRunIgnoresMailboxFailures(t *testing.T) {
	p := newGitProject(t)
	writeFile(t, filepath.Join(p.Root, ".orchestrator", "mailbox"), "x")
	answer, code, err := p.Run(opts("run", "--to", "claude"), launched().launch)
	if err != nil || code != 0 || !strings.HasPrefix(compact(t, answer), `{"flipped":true`) {
		t.Errorf("Run = %v, %d, %v", answer, code, err)
	}
}

// Each step fails with Node's error after the earlier writes and before the launch.
func TestRunStopsAtTheFirstFailingStep(t *testing.T) {
	for _, c := range []struct {
		name    string
		prepare func(t *testing.T, p Project)
		want    string // the error, with $ROOT for the project
		written []string
	}{
		{"exclude is a directory", func(t *testing.T, p Project) { mkdir(t, filepath.Join(p.Root, ".git", "info", "exclude")) },
			"EISDIR: illegal operation on a directory, read", nil},
		{".orchestrator is a file", func(t *testing.T, p Project) { writeFile(t, filepath.Join(p.Root, ".orchestrator"), "") },
			"EEXIST: file already exists, mkdir '$ROOT/.orchestrator'", nil},
		{"brief is a directory", func(t *testing.T, p Project) { mkdir(t, p.briefPath()) },
			"EISDIR: illegal operation on a directory, rename '$ROOT/.orchestrator/briefs/conductor-continue.md.tmp-*' -> '$ROOT/.orchestrator/briefs/conductor-continue.md'", []string{"exclude"}},
		{"flip.json is a directory", func(t *testing.T, p Project) { mkdir(t, p.FlipPath) },
			"flip state unreadable: $ROOT/.orchestrator/flip.json: EISDIR", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newGitProject(t)
			c.prepare(t, p)
			launcher := launched()
			_, _, err := p.Run(opts("run", "--to", "claude"), launcher.launch)
			// PE-21: the brief and flip.json are replaced through a private temp, so a
			// directory there fails at the rename, not the open.
			if want := strings.ReplaceAll(c.want, "$ROOT", p.Root); err == nil || tempNamesSpelledOut(err.Error()) != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if len(launcher.calls) != 0 {
				t.Errorf("launched after the failure: %q", launcher.calls)
			}
			written := map[string]string{
				"exclude": filepath.Join(p.Root, ".git", "info", "exclude"),
				"brief":   p.briefPath(),
				"flip":    p.FlipPath,
				"mailbox": filepath.Join(p.Root, ".orchestrator", "mailbox", "messages.jsonl"),
			}
			for name, path := range written {
				info, err := os.Stat(path)
				isFile := err == nil && info.Mode().IsRegular()
				if want := slices.Contains(c.written, name); isFile != want {
					t.Errorf("%s written %v, want %v", name, isFile, want)
				}
			}
		})
	}
}

// E-L2 (PE-63): a launch answer carries the detach line's run_id and runner_pid at the top
// level, so a cut launcher quote no longer hides them.
func TestRunAnswerCarriesTheLaunchedRun(t *testing.T) {
	deep := t.TempDir()
	for jsstr.Len(deep) < 600 {
		deep = filepath.Join(deep, strings.Repeat("d", 60))
	}
	mkdir(t, filepath.Join(deep, ".git"))
	p := NewProject(deep)
	p.Now = func() time.Time { return now }
	p.NewID = func() string { return "id-1" }
	detach := `{"run_id":"run-1","lane":"conductor","cwd":` + strconv.Quote(deep) + `,"runner_pid":42}`
	answer, code, err := p.Run(opts("run", "--to", "claude"), (&fakeLauncher{status: exitStatus(0), stdout: "noise\n" + detach + "\n"}).launch)
	if err != nil || code != 0 {
		t.Fatalf("Run = %v, %d, %v", answer, code, err)
	}
	launcher, _ := jsjson.Path(answer, "launcher").(string)
	if jsstr.Len(launcher) != launcherUnits || jsjson.Path(answer, "run_id") != "run-1" || jsjson.Path(answer, "runner_pid") != float64(42) {
		t.Fatalf("answer %s", compact(t, answer))
	}
	if keys := answer.Keys(); !slices.Equal(keys, []string{"flipped", "to", "from", "launcher", "run_id", "runner_pid"}) {
		t.Errorf("keys %q", keys)
	}
	for _, c := range []struct {
		name     string
		launcher *fakeLauncher
	}{
		{"no runner_pid", &fakeLauncher{status: exitStatus(0), stdout: `{"run_id":"run-1"}`}},
		{"string runner_pid", &fakeLauncher{status: exitStatus(0), stdout: `{"run_id":"run-1","runner_pid":"42"}`}},
		{"not the last line", &fakeLauncher{status: exitStatus(0), stdout: `{"run_id":"run-1","runner_pid":42}` + "\ntrailer"}},
		{"exit 1", &fakeLauncher{status: exitStatus(1), stdout: `{"run_id":"run-1","runner_pid":42}`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer, _, err := newGitProject(t).Run(opts("run", "--to", "claude"), c.launcher.launch)
			if err != nil {
				t.Fatal(err)
			}
			if _, has := jsjson.Lookup(answer, "run_id"); has {
				t.Errorf("answer %s", compact(t, answer))
			}
			if _, has := jsjson.Lookup(answer, "runner_pid"); has {
				t.Errorf("answer %s", compact(t, answer))
			}
		})
	}
}
