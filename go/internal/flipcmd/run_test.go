package flipcmd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
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

// Only a trailing key is bare: the run-record parser takes the next argument as a value even
// when it starts with "--".
func TestRunWritesBareOptionsAsTrue(t *testing.T) {
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
		{"unparsable", `{"at":`, `null`},
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
		{".git is a file", func(t *testing.T, p Project) {
			if err := os.Remove(filepath.Join(p.Root, ".git")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(p.Root, ".git"), "gitdir: x\n")
		}, "ENOTDIR: not a directory, mkdir '$ROOT/.git/info'", nil},
		{"exclude is a directory", func(t *testing.T, p Project) { mkdir(t, filepath.Join(p.Root, ".git", "info", "exclude")) },
			"EISDIR: illegal operation on a directory, read", nil},
		{".orchestrator is a file", func(t *testing.T, p Project) { writeFile(t, filepath.Join(p.Root, ".orchestrator"), "") },
			"ENOTDIR: not a directory, mkdir '$ROOT/.orchestrator/briefs'", []string{"exclude"}},
		{"brief is a directory", func(t *testing.T, p Project) { mkdir(t, p.briefPath()) },
			"EISDIR: illegal operation on a directory, rename '$ROOT/.orchestrator/briefs/conductor-continue.md.tmp-*' -> '$ROOT/.orchestrator/briefs/conductor-continue.md'", []string{"exclude"}},
		{"flip.json is a directory", func(t *testing.T, p Project) { mkdir(t, p.FlipPath) },
			"EISDIR: illegal operation on a directory, rename '$ROOT/.orchestrator/flip.json.tmp-*' -> '$ROOT/.orchestrator/flip.json'", []string{"exclude", "brief"}},
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
