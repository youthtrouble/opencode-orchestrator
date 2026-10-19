package cmdref

import "testing"

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
