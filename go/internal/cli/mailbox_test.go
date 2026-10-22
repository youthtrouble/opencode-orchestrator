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
	recommended := "yes"
	m, err := mailbox.New(root).Post(mailbox.Draft{From: "conductor-claude", To: "human", Kind: "question", Subject: subject, Recommended: &recommended})
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

func TestMailboxReplyOutsideAProjectRefusesAndCreatesNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCHESTRATOR_PROJECT", root)
	code, stdout, stderr := run(t, Mailbox, []string{"reply", "abc", "yes"}, "")
	want := "not an orchestrator project: " + root + " has no .orchestrator/ directory; cd into the project or pass --root <project dir>\n"
	if code != 2 || stdout != "" || stderr != want {
		t.Errorf("code %d stdout %q stderr %q; want 2 and %q", code, stdout, stderr, want)
	}
	if _, err := os.Stat(filepath.Join(root, ".orchestrator")); !os.IsNotExist(err) {
		t.Errorf(".orchestrator/ exists after a refused reply (stat: %v)", err)
	}
}

func TestMailboxReplyAndAckWithAnUnknownIDNameTheMailboxSearched(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCHESTRATOR_PROJECT", root)
	postQuestion(t, root, "ship it?")
	want := "no message with id zzz in " + filepath.Join(root, ".orchestrator", "mailbox") + "; if it belongs to another project, cd there or pass --root <project dir>\n"
	for _, argv := range [][]string{{"reply", "zzz", "yes"}, {"ack", "--id", "zzz", "--by", "human"}} {
		code, stdout, stderr := run(t, Mailbox, argv, "")
		if code != 1 || stdout != "" || stderr != want {
			t.Errorf("%v: code %d stdout %q stderr %q; want 1 and %q", argv, code, stdout, stderr, want)
		}
	}
}

func TestMailboxPostRecordsAQuestionsRecommendationAndBlocking(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"blocking by default", []string{"--recommended", "ship it"}, `"recommended":"ship it","blocking":true}`},
		{"bare --blocking", []string{"--recommended", "ship it", "--blocking"}, `"recommended":"ship it","blocking":true}`},
		{"--blocking false", []string{"--blocking", "false", "--recommended", "ship it"}, `"recommended":"ship it","blocking":false}`},
		{"--ask decision is the default, not written", []string{"--ask", "decision", "--recommended", "ship it"}, `"recommended":"ship it","blocking":true}`},
		{"a word that only begins like a claim", []string{"--recommended", "Donate it"}, `"recommended":"Donate it","blocking":true}`},
		{"--ask action records ask, no recommendation", []string{"--ask", "action", "--blocking", "false"}, `"blocking":false,"ask":"action"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_PROJECT", t.TempDir())
			argv := append([]string{"post", "--from", "conductor", "--to", "human", "--kind", "question", "--subject", "ship?"}, c.args...)
			code, stdout, stderr := run(t, Mailbox, append(argv, "--json"), "")
			if code != 0 || stderr != "" {
				t.Fatalf("code %d stderr %q", code, stderr)
			}
			if !strings.HasSuffix(stdout, `"thread":null,`+c.want+"\n") {
				t.Errorf("stdout = %q, want it to end with thread then %s", stdout, c.want)
			}
		})
	}
}

func TestMailboxPostRefusesMisusedQuestionOptionsAndWritesNothing(t *testing.T) {
	const (
		needsRecommendation = "mailbox post: a question needs --recommended \"<the answer you recommend>\"\n"
		onlyForQuestions    = "mailbox post: --recommended, --blocking and --ask are only for --kind question\n"
		badAsk              = "mailbox post: --ask must be decision or action\n"
		claimsDone          = "mailbox post: --recommended reads as a report that work is done; recommended answers are for decisions. To ask the owner to do something, use --ask action with the steps in --body\n"
	)
	for _, c := range []struct {
		name, kind string
		args       []string
		want       string
	}{
		{"question without --recommended", "question", nil, needsRecommendation},
		{"bare --recommended", "question", []string{"--recommended"}, needsRecommendation},
		{"empty --recommended", "question", []string{"--recommended", ""}, needsRecommendation},
		{"--blocking maybe", "question", []string{"--recommended", "r", "--blocking", "maybe"}, "mailbox post: --blocking must be true or false\n"},
		{"--recommended on a note", "note", []string{"--recommended", "r"}, onlyForQuestions},
		{"bare --blocking on a note", "note", []string{"--blocking"}, onlyForQuestions},
		{"--ask on a note", "note", []string{"--ask", "action"}, onlyForQuestions},
		{"bare --ask", "question", []string{"--recommended", "r", "--ask"}, badAsk},
		{"--ask other", "question", []string{"--ask", "both", "--recommended", "r"}, badAsk},
		{"--ask decision without --recommended", "question", []string{"--ask", "decision"}, needsRecommendation},
		{"--ask action with --recommended", "question", []string{"--ask", "action", "--recommended", "Yes"},
			"mailbox post: an --ask action question takes no --recommended: put the steps in --body; the owner's reply is a claim to verify\n"},
		{"a recommendation that reports work done", "question", []string{"--recommended", "Done: token created, scripts ran"}, claimsDone},
		{"a padded, upper-case claim", "question", []string{"--recommended", " \u00a0I HAVE DONE the setup"}, claimsDone},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ORCHESTRATOR_PROJECT", root)
			argv := append([]string{"post", "--from", "conductor", "--kind", c.kind, "--subject", "s"}, c.args...)
			code, stdout, stderr := run(t, Mailbox, argv, "")
			if code != 2 || stdout != "" || stderr != c.want {
				t.Errorf("code %d stdout %q stderr %q; want 2 and %q", code, stdout, stderr, c.want)
			}
			if _, err := os.Stat(filepath.Join(root, ".orchestrator")); !os.IsNotExist(err) {
				t.Errorf(".orchestrator/ exists after a refused post (stat: %v)", err)
			}
		})
	}
}

func TestMailboxInboxShowsBlockingAndTheRecommendedAnswer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCHESTRATOR_PROJECT", root)
	dir := filepath.Join(root, ".orchestrator", "mailbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	messages := strings.Join([]string{
		`{"id":"q1000000","at":"2026-10-18T12:00:00.000Z","from":"c","to":"human","kind":"question","subject":"ship?","body":"b","recommended":"yes\nnow","blocking":true}`,
		`{"id":"q2000000","at":"2026-10-18T13:00:00.000Z","from":"c","to":"human","kind":"question","subject":"name?","body":"b","recommended":"` + strings.Repeat("r", 205) + `","blocking":false}`,
		`{"id":"q3000000","at":"2026-10-18T14:00:00.000Z","from":"c","to":"human","kind":"question","subject":"legacy","body":"b"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte(messages), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, Mailbox, []string{"inbox"}, "")
	const hint = `    reply: orch mailbox reply `
	want := "[q1000000] 2026-10-18T12:00Z from c: ship? (blocking)\n    b\n    recommended: yes now\n" + hint + "q1000000 \"your answer\"\n\n" +
		"[q2000000] 2026-10-18T13:00Z from c: name?\n    b\n    recommended: " + strings.Repeat("r", 200) + "\n" + hint + "q2000000 \"your answer\"\n\n" +
		"[q3000000] 2026-10-18T14:00Z from c: legacy\n    b\n" + hint + "q3000000 \"your answer\"\n\n"
	if code != 0 || stderr != "" || stdout != want {
		t.Errorf("code %d stderr %q stdout:\n%s\nwant:\n%s", code, stderr, stdout, want)
	}
}
