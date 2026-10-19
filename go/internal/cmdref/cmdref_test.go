package cmdref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The JS supervisor writes this exact command into its reminders.
func TestScriptIsTheInstalledNodeCommand(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/mailbox.mjs"
	if got := Script("mailbox"); got != want {
		t.Errorf("Script(mailbox) = %q, want %q", got, want)
	}
}

// bin/mailbox.mjs inbox prints this exact command before each question's id prefix.
func TestMailboxReplyIsTheInboxHint(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/mailbox.mjs reply"
	if got := MailboxReply(); got != want {
		t.Errorf("MailboxReply() = %q, want %q", got, want)
	}
}

// bin/flip.mjs gate names this exact command in the human's message body.
func TestFlipGateClearIsTheGateMessageCommand(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/flip.mjs gate --clear"
	if got := FlipGateClear(); got != want {
		t.Errorf("FlipGateClear() = %q, want %q", got, want)
	}
}

// flip.mjs run writes its brief from a template literal (l.74) whose only escapes are \n and
// whose only substitutions are to, from and the reason. Rendering that literal from the
// source pins the text byte for byte.
func TestConductorContinueBriefIsTheFlipTemplate(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "orchestrator", "bin", "flip.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, found := strings.Cut(string(source), "fs.writeFileSync(brief, `")
	template, _, closed := strings.Cut(after, "`);")
	if !found || !closed {
		t.Fatal("flip.mjs no longer writes the brief from one template literal")
	}
	rendered := strings.NewReplacer(
		`${to}`, "claude",
		`${from}`, "opencode",
		`${opts.reason || "vendor budget flip"}`, "handback: home door",
		`\n`, "\n",
	).Replace(template)
	if strings.Contains(rendered, "${") || strings.Contains(rendered, `\`) {
		t.Fatalf("the template has a substitution or escape this test does not render: %q", rendered)
	}
	if got := ConductorContinueBrief("claude", "opencode", "handback: home door"); got != rendered {
		t.Errorf("ConductorContinueBrief differs from flip.mjs\n got: %q\nwant: %q", got, rendered)
	}
}

// bin/claude-exec.mjs records its incidents under its own file name.
func TestScriptFileIsTheScriptBasename(t *testing.T) {
	if got := ScriptFile("claude-exec"); got != "claude-exec.mjs" {
		t.Errorf("ScriptFile(claude-exec) = %q", got)
	}
}

// claude-exec.mjs --help prints lines 2-12 of its own source joined by "\n".
func TestClaudeExecHelpIsTheScriptHeader(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "orchestrator", "bin", "claude-exec.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(strings.Split(string(source), "\n")[1:12], "\n")
	if ClaudeExecHelp != want {
		t.Errorf("ClaudeExecHelp differs from the script header\n got: %q\nwant: %q", ClaudeExecHelp, want)
	}
}

// codex-exec.mjs --help prints lines 2-12 of its own source joined by "\n".
func TestCodexExecHelpIsTheScriptHeader(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "orchestrator", "bin", "codex-exec.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(strings.Split(string(source), "\n")[1:12], "\n")
	if CodexExecHelp != want {
		t.Errorf("CodexExecHelp differs from the script header\n got: %q\nwant: %q", CodexExecHelp, want)
	}
}

// kickoffSource is bin/kickoff.mjs.
func kickoffSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "orchestrator", "bin", "kickoff.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

// between returns the text of source between the first start and the end after it.
func between(t *testing.T, source, start, end string) string {
	t.Helper()
	_, after, found := strings.Cut(source, start)
	text, _, closed := strings.Cut(after, end)
	if !found || !closed {
		t.Fatalf("kickoff.mjs no longer contains %q ... %q", start, end)
	}
	return text
}

// kickoff.mjs builds the prompt from one template literal with a nested one for the side
// briefs; rendering both from the source pins the text byte for byte.
func TestKickoffPromptIsTheKickoffTemplate(t *testing.T) {
	source := kickoffSource(t)
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
		if got := KickoffPrompt("a.md", side); got != want {
			t.Errorf("KickoffPrompt(a.md, %q) differs from kickoff.mjs\n got: %q\nwant: %q", side, got, want)
		}
	}
}

// kickoff.mjs prints its usage line from a string literal.
func TestKickoffUsageIsTheKickoffUsageLine(t *testing.T) {
	want := between(t, kickoffSource(t), `console.log("usage: `, `");`)
	if got := KickoffUsage(); got != "usage: "+want {
		t.Errorf("KickoffUsage() = %q, want %q", got, "usage: "+want)
	}
}

// kickoff.mjs names the watch command in both its headless and interactive hints.
func TestWatchFollowIsTheKickoffHint(t *testing.T) {
	source := kickoffSource(t)
	if got := WatchFollow(); strings.Count(source, got) != 2 {
		t.Errorf("WatchFollow() = %q appears %d times in kickoff.mjs, want 2", got, strings.Count(source, got))
	}
}
