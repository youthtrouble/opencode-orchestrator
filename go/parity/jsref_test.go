//go:build parity

package parity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/runrecord"
)

// TestJSReference derives orch's texts from the JS reference's own source (ORCH_JS, which
// run.sh extracts from the commit in go/parity/js-ref), so the literal expectations in the
// unit tests cannot drift from the JS they were copied from.
func TestJSReference(t *testing.T) {
	orchJS := os.Getenv("ORCH_JS")
	if orchJS == "" {
		t.Fatal("ORCH_JS must be set; run go/parity/run.sh")
	}
	source := func(t *testing.T, script string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(orchJS, "bin", script))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	t.Run("flip template", func(t *testing.T) { testFlipTemplate(t, source(t, "flip.mjs")) })
	t.Run("claude-exec help", func(t *testing.T) { testHelpHeader(t, source(t, "claude-exec.mjs"), cmdref.ClaudeExecHelp) })
	t.Run("codex-exec help", func(t *testing.T) { testHelpHeader(t, source(t, "codex-exec.mjs"), cmdref.CodexExecHelp) })
	t.Run("kickoff prompt", func(t *testing.T) { testKickoffPrompt(t, source(t, "kickoff.mjs")) })
	t.Run("kickoff usage", func(t *testing.T) { testKickoffUsage(t, source(t, "kickoff.mjs")) })
	t.Run("kickoff watch hint", func(t *testing.T) { testKickoffWatchHint(t, source(t, "kickoff.mjs")) })
	t.Run("run record", func(t *testing.T) { testRunRecord(t, filepath.Join(orchJS, "lib", "run-record.mjs")) })
}

// flip.mjs run writes its brief from a template literal whose only escapes are \n and whose
// only substitutions are to, from and the reason. Rendering that literal pins the text byte
// for byte.
func testFlipTemplate(t *testing.T, source string) {
	template := between(t, source, "fs.writeFileSync(brief, `", "`);")
	rendered := strings.NewReplacer(
		`${to}`, "claude",
		`${from}`, "opencode",
		`${opts.reason || "vendor budget flip"}`, "handback: home door",
		`\n`, "\n",
	).Replace(template)
	if strings.Contains(rendered, "${") || strings.Contains(rendered, `\`) {
		t.Fatalf("the template has a substitution or escape this test does not render: %q", rendered)
	}
	if got := cmdref.ConductorContinueBrief("claude", "opencode", "handback: home door"); got != rendered {
		t.Errorf("ConductorContinueBrief differs from flip.mjs\n got: %q\nwant: %q", got, rendered)
	}
}

// The exec scripts' --help prints lines 2-12 of their own source joined by "\n".
func testHelpHeader(t *testing.T, source, help string) {
	if want := strings.Join(strings.Split(source, "\n")[1:12], "\n"); help != want {
		t.Errorf("help differs from the script header\n got: %q\nwant: %q", help, want)
	}
}

// kickoff.mjs builds the prompt from one template literal with a nested one for the side
// briefs; rendering both pins the text byte for byte.
func testKickoffPrompt(t *testing.T, source string) {
	template := between(t, source, "const prompt = `", "`;\n")
	head, rest, _ := strings.Cut(template, "${side.length ? `")
	sideTemplate, tail, _ := strings.Cut(rest, "` : \"\"}")
	render := func(main string, side []string) string {
		sideText := ""
		if len(side) > 0 {
			sideText = strings.ReplaceAll(sideTemplate, `${side.join(", ")}`, strings.Join(side, ", "))
		}
		return strings.ReplaceAll(head, "${main}", main) + sideText + tail
	}
	for _, side := range [][]string{nil, {"b.md"}, {"b.md", "c.md"}} {
		want := render("a.md", side)
		if strings.Contains(want, "${") || strings.Contains(want, "`") {
			t.Fatalf("the template has a substitution this test does not render: %q", want)
		}
		if got := cmdref.KickoffPrompt("a.md", side); got != want {
			t.Errorf("KickoffPrompt(a.md, %q) differs from kickoff.mjs\n got: %q\nwant: %q", side, got, want)
		}
	}
}

// kickoff.mjs prints its usage line from a string literal.
func testKickoffUsage(t *testing.T, source string) {
	want := "usage: " + between(t, source, `console.log("usage: `, `");`)
	if got := cmdref.KickoffUsage(); got != want {
		t.Errorf("KickoffUsage() = %q, want %q", got, want)
	}
}

// kickoff.mjs names the watch command in both its headless and interactive hints.
func testKickoffWatchHint(t *testing.T, source string) {
	if got := cmdref.WatchFollow(); strings.Count(source, got) != 2 {
		t.Errorf("WatchFollow() = %q appears %d times in kickoff.mjs, want 2", got, strings.Count(source, got))
	}
}

// runrecord.Write names and writes a record exactly as lib/run-record.mjs writeRunRecord does.
func testRunRecord(t *testing.T, lib string) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for parity runs:", err)
	}
	now := time.Date(2026, 10, 19, 1, 2, 3, 4_000_000, time.UTC)
	full := `{"started_at":"2026-10-18T10:11:12.345Z","harness":"claude","session_id":"ses_0123456789","prompt":"é\n😀","cost":{"usd":0.5,"tokens":[1,2]},"thread_id":null,"10":"index key"}`
	cases := []struct{ projectRoot, record string }{
		{"proj", full},
		{"proj/../proj/", full},
		{"proj", `{"started_at":"s","harness":"h","session_id":[]}`},
		{"proj", `{"started_at":"s","harness":"h","session_id":["s"]}`},
		{"proj", `{"started_at":"s","harness":"h","session_id":["a","b"]}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":[],"session_id":"fallback"}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":0,"session_id":["0123456789abcdef",1,2,3,4,5,6,7,8,9]}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":[["x","y"],null,{"k":1},1.5,true,"😀😀😀😀😀"]}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":"/../../x"}`},
		{"proj", `{"started_at":"s","harness":"h","thread_id":["a/../../b"]}`},
	}
	const script = `const { writeRunRecord } = await import(process.argv[1]); console.log(writeRunRecord(process.argv[2], JSON.parse(process.argv[3])))`
	for _, c := range cases {
		jsDir, goDir := t.TempDir(), t.TempDir()
		cmd := exec.Command(node, "--input-type=module", "-e", script, lib, c.projectRoot, c.record)
		cmd.Dir = jsDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node %s: %v", c.record, err)
		}
		jsFile := strings.TrimSuffix(string(out), "\n")

		record, err := jsjson.Unmarshal([]byte(c.record))
		if err != nil {
			t.Fatal(err)
		}
		t.Chdir(goDir)
		goFile, err := runrecord.Write(c.projectRoot, record.(*jsjson.Object), now)
		if err != nil {
			t.Fatalf("Write(%s): %v", c.record, err)
		}
		if goFile != jsFile {
			t.Errorf("%q %s: path %q, Node %q", c.projectRoot, c.record, goFile, jsFile)
		}
		jsData, err := os.ReadFile(filepath.Join(jsDir, jsFile))
		if err != nil {
			t.Fatal(err)
		}
		goData, err := os.ReadFile(filepath.Join(goDir, goFile))
		if err != nil {
			t.Fatal(err)
		}
		if string(goData) != string(jsData) {
			t.Errorf("%q %s: content\n%s\nNode\n%s", c.projectRoot, c.record, goData, jsData)
		}
	}
}

// between returns the text of source between the first start and the end after it.
func between(t *testing.T, source, start, end string) string {
	t.Helper()
	_, after, found := strings.Cut(source, start)
	text, _, closed := strings.Cut(after, end)
	if !found || !closed {
		t.Fatalf("the JS source no longer contains %q ... %q", start, end)
	}
	return text
}
