package cmdref

import "testing"

// The JS supervisor writes this exact command into its reminders.
func TestScriptIsTheInstalledNodeCommand(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/mailbox.mjs"
	if got := Script("mailbox"); got != want {
		t.Errorf("Script(mailbox) = %q, want %q", got, want)
	}
}
