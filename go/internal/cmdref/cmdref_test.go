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
