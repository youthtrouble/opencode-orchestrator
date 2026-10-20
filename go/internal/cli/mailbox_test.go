package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
)

const gateClearedLine = "cleared the human gate; the supervisor relaunches the claude conductor within a minute\n"

// gatedProject is a project stopped at a human gate of the claude door whose question is
// questionRef(id) of a new question, and that question's id.
func gatedProject(t *testing.T, questionRef func(id string) string) (root, questionID string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("ORCHESTRATOR_PROJECT", root)
	questionID = postQuestion(t, root, "push to main?")
	flipJSON := `{"awaiting_human":{"at":"2026-01-01T00:00:00.000Z","door":"claude","reason":"push","question":"` + questionRef(questionID) + `"}}`
	if err := os.WriteFile(filepath.Join(root, ".orchestrator", "flip.json"), []byte(flipJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, questionID
}

func postQuestion(t *testing.T, root, subject string) string {
	t.Helper()
	m, err := mailbox.New(root).Post(mailbox.Draft{From: "conductor-claude", To: "human", Kind: "question", Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := m.Get("id")
	return id.(string)
}

func readFlipJSON(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".orchestrator", "flip.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMailboxReplyToTheGateQuestionClearsTheGate(t *testing.T) {
	prefix := func(id string) string { return id[:8] }
	whole := func(id string) string { return id }
	for _, c := range []struct {
		name                   string
		questionRef, replyWith func(string) string
	}{
		{"full id", whole, whole},
		{"8-character prefix", prefix, prefix},
		{"gate holds the full id, reply uses a prefix", whole, prefix},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, questionID := gatedProject(t, c.questionRef)
			code, stdout, stderr := run(t, Mailbox, []string{"reply", c.replyWith(questionID), "yes"}, "")
			if code != 0 || stderr != "" {
				t.Fatalf("code %d stderr %q", code, stderr)
			}
			if !strings.HasSuffix(stdout, gateClearedLine) {
				t.Errorf("stdout = %q, want it to end with %q", stdout, gateClearedLine)
			}
			flipJSON := readFlipJSON(t, root)
			if strings.Contains(flipJSON, "awaiting_human") || !strings.Contains(flipJSON, `"reason": "human gate cleared"`) || !strings.Contains(flipJSON, `"to": "claude"`) {
				t.Errorf("flip.json = %s, want the gate replaced by a pending launch of claude", flipJSON)
			}
		})
	}
}

func TestMailboxReplyToAnotherMessageKeepsTheGate(t *testing.T) {
	root, _ := gatedProject(t, func(id string) string { return id })
	before := readFlipJSON(t, root)
	other := postQuestion(t, root, "naming?")
	code, stdout, stderr := run(t, Mailbox, []string{"reply", other, "yes"}, "")
	if code != 0 || stderr != "" || strings.Contains(stdout, "cleared the human gate") {
		t.Errorf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := readFlipJSON(t, root); got != before {
		t.Errorf("flip.json = %s, want it unchanged", got)
	}
}

// The gate's question was stored as a short prefix that later also matches another message: a
// reply to that other message must not clear the gate (the prefix no longer names one message).
func TestMailboxReplyToAMessageSharingTheGateQuestionsShortPrefixKeepsTheGate(t *testing.T) {
	root, questionID := gatedProject(t, func(id string) string { return id[:2] })
	before := readFlipJSON(t, root)
	other := postQuestion(t, root, "naming?")
	otherID := questionID[:2] + other[2:]
	messages := filepath.Join(root, ".orchestrator", "mailbox", "messages.jsonl")
	data, err := os.ReadFile(messages)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(messages, []byte(strings.Replace(string(data), other, otherID, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, Mailbox, []string{"reply", otherID, "yes"}, "")
	if code != 0 || strings.Contains(stdout, "cleared the human gate") {
		t.Errorf("code %d stdout %q", code, stdout)
	}
	if want := "could not match the human gate's question: ambiguous message id prefix " + questionID[:2] + "\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if got := readFlipJSON(t, root); got != before {
		t.Errorf("flip.json = %s, want it unchanged", got)
	}
}

func TestMailboxReplyReportsAnUnreadableFlipJSONButSucceeds(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCHESTRATOR_PROJECT", root)
	questionID := postQuestion(t, root, "ship it?")
	if err := os.WriteFile(filepath.Join(root, ".orchestrator", "flip.json"), []byte(`{"to":`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, Mailbox, []string{"reply", questionID, "yes"}, "")
	if code != 0 || !strings.HasPrefix(stdout, "replied (") {
		t.Errorf("code %d stdout %q", code, stdout)
	}
	if want := "could not check for a human gate: .orchestrator/flip.json is not valid JSON\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}
