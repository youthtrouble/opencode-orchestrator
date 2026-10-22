//go:build parity

package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Seeded ids are not uuid v4-shaped, so the fresh-id normalizer never rewrites them and their
// prefixes are compared literally.
const (
	idNote     = "aaaaaaaa-0000-0000-0000-000000000001"
	idReview   = "aaaaaaaa-0000-0000-0000-000000000002"
	idQuestion = "bbbbbbbb-0000-0000-0000-000000000003"
	idStar     = "cccccccc-0000-0000-0000-000000000004"
	idTwice    = "dddddddd-0000-0000-0000-000000000005"
	idBare     = "ffffffff-0000-0000-0000-000000000006"
)

var mailboxMessages = lines(
	`{"id":"`+idNote+`","at":"2026-10-18T10:00:00.000Z","from":"conductor","to":"*","kind":"note","subject":"hello","body":"l1\nl2\nl3\nl4\nl5\nl6\nl7","refs":["a.md","b.md"],"thread":null}`,
	`{"id":"`+idReview+`","at":"2026-10-18T11:00:00.000Z","from":"coder","to":"reviewer","kind":"review","subject":"please review","body":"","refs":[],"thread":null}`,
	`{"id":"`+idQuestion+`","at":"2026-10-18T12:00:00.000Z","from":"conductor","to":"human","kind":"question","subject":"Ship it? 😀","body":"multi\nline","refs":[],"thread":null,"acked_by":["stale"]}`,
	`{"id":"`+idStar+`","at":"2026-10-18T13:00:00.000Z","from":"planner","to":"*","kind":"question","subject":"Which db?","refs":[],"thread":null}`,
	`{"id":"`+idTwice+`","at":"2026-10-18T14:00:00.000Z","from":"a","to":"b","kind":"handoff-long","subject":"first copy","body":"","refs":[],"thread":null}`,
	`{"id":"`+idTwice+`","at":"2026-10-18T15:00:00.000Z","from":"a","to":"b","kind":"é","subject":"second copy","body":"","refs":[],"thread":null}`,
	`{"id":"`+idBare+`","at":"2026-10-18T16:00:00.000Z","kind":"note","refs":[]}`,
)

// Duplicate acks, an ack without `by`, and an ack of a question by someone other than the human.
var mailboxAcks = lines(
	`{"id":"`+idNote+`","by":"reviewer","at":"2026-10-18T10:05:00.000Z","note":""}`,
	`{"id":"`+idNote+`","by":"reviewer","at":"2026-10-18T10:06:00.000Z","note":"again"}`,
	`{"id":"`+idNote+`","at":"2026-10-18T10:07:00.000Z","note":"nobody"}`,
	`{"id":"`+idNote+`","by":"coder","at":"2026-10-18T10:08:00.000Z","note":""}`,
	`{"id":"`+idQuestion+`","by":"conductor","at":"2026-10-18T12:05:00.000Z","note":""}`,
)

// Hand-edited oddities: unparsable and falsy lines are dropped, other values are kept whatever
// their shape; acks keyed by a missing or null id and `by` values that are objects.
var malformedMessages = lines(
	`{"id":"`+idNote+`","at":"2026-10-18T10:00:00.000Z","from":"x","to":"*","kind":"note","subject":"ok","body":"","refs":[],"thread":null}`,
	`not json`, `0`, `null`, `false`, `""`, `true`, `42`, `"stré"`, `[1,2]`,
	`{"kind":"note","subject":"no id"}`,
	`{"id":null,"subject":"null id"}`,
	`{"id":"`+idReview+`","at":5,"kind":"note","refs":[]}`,
)

var malformedAcks = lines(
	`{"by":"ghost"}`,
	`{"id":null,"by":{"o":1}}`,
	`{"id":null,"by":{"o":1}}`,
	`{"id":null,"by":null}`,
	`{"id":null}`,
	`{"id":"`+idReview+`","by":7}`,
	`{"id":"`+idReview+`","by":"7"}`,
	`nope`,
)

// Shapes that the text list prints without throwing.
var oddShapes = lines(
	`{"id":["ab","cd"],"kind":"note","from":null,"refs":5,"body":0}`,
	`{"id":"`+idNote+`","kind":"note","refs":"","body":false,"at":{"x":1}}`,
	`{"id":"`+idReview+`","kind":"note","refs":{"length":0},"subject":["s",null,"t"]}`,
	`{"id":"`+idStar+`","kind":"note","refs":[null,"r",["n","m"]],"body":"b"}`,
)

func lines(all ...string) string { return strings.Join(all, "\n") + "\n" }

func seedMailbox(messages, acks string) seed {
	return func(t *testing.T, sb sandbox) {
		dir := filepath.Join(sb.project, ".orchestrator", "mailbox")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"messages.jsonl": messages, "acks.jsonl": acks} {
			if content == "" {
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Fresh values, rewritten only where they have their exact format.
var (
	freshUUID    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)
	isoMillis    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)
	replyPrefix  = regexp.MustCompile(`^replied \([0-9a-f]{8}\)`)
	freshIDField = regexp.MustCompile(`(?m)^(\{"id":")(` + freshUUID.String() + `)(")`)
	freshAtField = regexp.MustCompile(`(?m)^(\{(?:"id":"[^"\n]*",)?(?:"by":"(?:[^"\\]|\\.)*",)?"at":")(` + isoMillis.String() + `)(")`)
)

func normalizeFresh(s string) string {
	s = freshIDField.ReplaceAllString(s, "${1}<uuid>${3}")
	s = freshAtField.ReplaceAllString(s, "${1}<iso>${3}")
	return replyPrefix.ReplaceAllString(s, "replied (<id8>)")
}

// Only appended records and write receipts contain nondeterministic fields. Seed bytes and
// user content (even UUIDs and ISO timestamps) remain literal comparison inputs.
func normalizeMailboxWrites(o outcome, before map[string]string, command string) outcome {
	if command == "ack" {
		o.stdout = freshAtField.ReplaceAllString(o.stdout, "${1}<iso>${3}")
	} else {
		o.stdout = normalizeFresh(o.stdout)
	}
	for name, content := range o.files {
		if !strings.HasSuffix(name, "/messages.jsonl") && !strings.HasSuffix(name, "/acks.jsonl") {
			continue
		}
		prefix := before[name]
		if strings.HasPrefix(content, prefix) {
			appended := strings.TrimPrefix(content, prefix)
			if strings.HasSuffix(name, "/acks.jsonl") {
				appended = freshAtField.ReplaceAllString(appended, "${1}<iso>${3}")
			} else {
				appended = normalizeFresh(appended)
			}
			o.files[name] = prefix + appended
		}
	}
	return o
}

type mailboxCase struct {
	name       string
	args       []string
	stdin      string
	env        []string
	extraFiles []string
	seed       seed
	crash      crashClass
	errorLine  string
}

func (h *harness) mailboxInvocation(c mailboxCase) invocation {
	inv := invocation{
		js:         append([]string{h.node, filepath.Join(h.orchJS, "bin", "mailbox.mjs")}, c.args...),
		orch:       append([]string{h.orchBin, "mailbox"}, c.args...),
		stdin:      []byte(c.stdin),
		env:        c.env,
		extraFiles: c.extraFiles,
		seed:       c.seed,
		crash:      c.crash,
		errorLine:  c.errorLine,
	}
	if len(c.args) > 0 && (c.args[0] == "post" || c.args[0] == "ack" || c.args[0] == "reply") {
		inv.normalizeWrites = func(o outcome, before map[string]string) outcome {
			return normalizeMailboxWrites(o, before, c.args[0])
		}
	}
	return inv
}

// testMailbox compares `orch mailbox` with bin/mailbox.mjs.
func (h *harness) testMailbox(t *testing.T) {
	t.Run("harness/crash-comparator", TestCrashComparator)
	t.Run("harness/fresh-only-normalization", TestMailboxNormalizationPreservesSeedAndContent)
	seeded := seedMailbox(mailboxMessages, mailboxAcks)
	malformed := seedMailbox(malformedMessages, malformedAcks)
	cases := []mailboxCase{
		{name: "post/all-flags", args: []string{"post", "--from", "a", "--to", "b", "--kind", "release", "--subject", "s 😀", "--body", "text", "--ref", "x.md", "--ref", "y.md", "--thread", "t1", "positional"}},
		{name: "post/defaults", args: []string{"post", "--from", "a", "--subject", "s", "--to", "", "--kind", ""}, seed: seeded},
		{name: "post/body-stdin", args: []string{"post", "--from", "a", "--subject", "s", "--body", "-"}, stdin: "hi\xff\xe2\x82 there\xf0\x9f\x98\x80\n\ufeffbom"},
		{name: "post/bare-flags", args: []string{"post", "--from", "a", "--subject", "s", "--body", "--thread", "--json", "x"}},
		{name: "post/root", args: []string{"post", "--root", "sub", "--from", "a", "--subject", "s"}, env: []string{"ORCHESTRATOR_PROJECT=$PROJECT/env"},
			extraFiles: []string{"sub/.orchestrator/mailbox/messages.jsonl", "env/.orchestrator/mailbox/messages.jsonl"}},
		{name: "post/env-project", args: []string{"post", "--from", "a", "--subject", "s"}, env: []string{"ORCHESTRATOR_PROJECT=$PROJECT/env"},
			extraFiles: []string{"env/.orchestrator/mailbox/messages.jsonl"}},
		{name: "post/no-from", args: []string{"post", "--subject", "s"}, crash: crashC1},
		{name: "post/no-subject", args: []string{"post", "--from", "a", "--subject", ""}, crash: crashC1},
		{name: "post/bare-from", args: []string{"post", "--from", "--subject", "s", "--body", "-"}, stdin: "read first", crash: crashC1},
		{name: "post/question-both-options", args: append(postQuestion, "--recommended", "ship it 😀", "--blocking", "true")},
		{name: "post/question-bare-blocking", args: append(postQuestion, "--recommended", "r", "--blocking")},
		{name: "post/question-blocking-false", args: append(postQuestion, "--blocking", "false", "--recommended", "r")},
		{name: "post/question-blocking-maybe", args: append(postQuestion, "--recommended", "r", "--blocking", "maybe")},
		{name: "post/question-no-recommended", args: append(postQuestion, "--body", "-"), stdin: "never read"},
		{name: "post/question-bare-recommended", args: append(postQuestion, "--recommended", "--blocking")},
		{name: "post/question-empty-recommended", args: append(postQuestion, "--recommended", "")},
		{name: "post/note-with-recommended", args: []string{"post", "--from", "a", "--subject", "s", "--recommended", "r"}},
		{name: "post/note-with-bare-blocking", args: []string{"post", "--from", "a", "--kind", "review", "--subject", "s", "--blocking"}},
		{name: "post/note-with-ask", args: []string{"post", "--from", "a", "--subject", "s", "--ask", "action"}},
		{name: "post/question-ask-action", args: append(postQuestion, "--ask", "action", "--body", "run ./setup.sh", "--blocking", "false")},
		{name: "post/question-ask-action-bare-blocking", args: append(postQuestion, "--ask", "action", "--blocking")},
		{name: "post/question-ask-decision", args: append(postQuestion, "--ask", "decision", "--recommended", "r")},
		{name: "post/question-ask-decision-no-recommended", args: append(postQuestion, "--ask", "decision")},
		{name: "post/question-ask-action-recommended", args: append(postQuestion, "--ask", "action", "--recommended", "Yes, go.")},
		{name: "post/question-ask-action-bare-recommended", args: append(postQuestion, "--ask", "action", "--recommended")},
		{name: "post/question-ask-other", args: append(postQuestion, "--ask", "Action", "--recommended", "r")},
		{name: "post/question-ask-empty", args: append(postQuestion, "--ask", "", "--recommended", "r")},
		{name: "post/question-bare-ask", args: append(postQuestion, "--recommended", "r", "--ask")},

		{name: "list/empty", args: []string{"list"}, seed: seedProjectDir(".orchestrator")},
		{name: "list/text", args: []string{"list"}, seed: seeded},
		{name: "list/json", args: []string{"list", "--json"}, seed: seeded},
		{name: "list/json-value-is-text", args: []string{"list", "--json", "x"}, seed: seeded},
		{name: "list/to", args: []string{"list", "--to", "reviewer", "--json"}, seed: seeded},
		{name: "list/kind", args: []string{"list", "--kind", "question"}, seed: seeded},
		{name: "list/json-question-keys", args: []string{"list", "--json"}, seed: seedMailbox(mailboxMessages+recommendedQuestions, mailboxAcks)},
		{name: "list/since", args: []string{"list", "--since", "2026-10-18T12:00:00.000Z"}, seed: seeded},
		{name: "list/unacked-by", args: []string{"list", "--unacked-by", "reviewer"}, seed: seeded},
		{name: "list/bare-filters", args: []string{"list", "--since", "--to", "--kind", "--limit", "--json"}, seed: seeded},
		{name: "list/malformed-json", args: []string{"list", "--json"}, seed: malformed},
		{name: "list/malformed-since", args: []string{"list", "--since", "1", "--json"}, seed: malformed},
		{name: "list/malformed-text", args: []string{"list"}, seed: malformed, crash: crashC2},
		{name: "list/odd-shapes-text", args: []string{"list"}, seed: seedMailbox(oddShapes, "")},
		{name: "list/body-not-string", args: []string{"list"}, seed: seedMailbox(lines(`{"id":"x","kind":"k","refs":[],"body":3}`), ""), crash: crashC2},
		{name: "list/refs-missing", args: []string{"list"}, seed: seedMailbox(lines(`{"id":"x","kind":"k"}`), ""), crash: crashC2},
		{name: "list/refs-string", args: []string{"list"}, seed: seedMailbox(lines(`{"id":"x","kind":"k","refs":"r"}`), ""), crash: crashC2},
		{name: "list/id-number", args: []string{"list"}, seed: seedMailbox(lines(`{"id":1,"kind":"k","refs":[]}`), ""), crash: crashC2},

		{name: "ack/full-id", args: []string{"ack", "--id", idNote, "--by", "me", "--note", "done"}, seed: seeded},
		{name: "ack/prefix", args: []string{"ack", "--id", "bbbb", "--by", "me"}, seed: seeded},
		{name: "ack/no-by", args: []string{"ack", "--id", "cccccccc", "--by", "--note"}, seed: seeded},
		{name: "ack/exact-beats-prefix", args: []string{"ack", "--id", idNote, "--by", "me"}, seed: seeded},
		{name: "ack/ambiguous", args: []string{"ack", "--id", "aaaaaaaa", "--by", "me"}, seed: seeded, crash: crashC1},
		{name: "ack/ambiguous-duplicate-id", args: []string{"ack", "--id", "dddd", "--by", "me"}, seed: seeded, crash: crashC1},
		{name: "ack/missing-id-matches-idless", args: []string{"ack", "--by", "me"}, seed: malformed},
		{name: "ack/empty-id-one-message", args: []string{"ack", "--id", "", "--by", "me"}, seed: seedMailbox(lines(`{"id":"only"}`), "")},
		{name: "ack/prefix-hits-idless", args: []string{"ack", "--id", "zz"}, seed: malformed, crash: crashC2},

		{name: "reply/words", args: []string{"reply", "bbbb", "yes,", "ship", "it"}, seed: seeded},
		{name: "reply/stdin", args: []string{"reply", idStar}, stdin: "postgres\nplease", seed: seeded},
		{name: "reply/option-value-is-id", args: []string{"reply", "--by", "bbbbbbbb", "ccc", "hi"}, seed: seeded},
		{name: "reply/no-from-no-subject", args: []string{"reply", "ffff", "ok"}, seed: seeded},
		{name: "reply/missing-id", args: []string{"reply"}, seed: seeded},
		{name: "reply/missing-body", args: []string{"reply", "bbbb"}, seed: seeded},
		{name: "reply/empty-words", args: []string{"reply", "bbbb", ""}, stdin: "from stdin", seed: seeded},
		{name: "reply/subject-140", args: []string{"reply", "eeee", "ok"}, seed: seedMailbox(lines(`{"id":"eeeeeeee-0000-0000-0000-000000000007","from":"p","subject":"`+strings.Repeat("s", 150)+`"}`), "")},

		{name: "inbox/none", args: []string{"inbox"}, seed: seedProjectDir(".orchestrator")},
		{name: "inbox/several", args: []string{"inbox"}, seed: seedMailbox(mailboxMessages+inboxExtras, mailboxAcks+`{"id":"eeeeeeee-0000-0000-0000-00000000000a","by":"human"}`+"\n")},
		{name: "inbox/limit-before-filter", args: []string{"inbox"}, seed: seedMailbox(manyQuestions(), "")},
		{name: "inbox/all-hidden-by-limit", args: []string{"inbox"}, seed: seedMailbox(hiddenQuestions(), "")},
		{name: "inbox/blocking-recommended-legacy", args: []string{"inbox"}, seed: seedMailbox(mailboxMessages+recommendedQuestions, mailboxAcks)},
		{name: "inbox/action", args: []string{"inbox"}, seed: seedMailbox(actionQuestions, "")},
		{name: "list/json-action", args: []string{"list", "--json"}, seed: seedMailbox(actionQuestions, "")},

		{name: "unknown-command", args: []string{"frob"}},
		{name: "no-command", args: nil},
	}
	// Each clear completion claim is refused however it is cased and padded; a decision that opens
	// with the same word, a word that only begins like one, or a claim later in the text is not.
	for _, claim := range []string{
		"Done: token created", "  ran the script", "COMPLETED.", "finished it", "Created the env", "deployed successfully",
		"Rebuilt, ok", "installed: all", "Ran\tit", "I ran it", "I did it", "I've done it", "i’ve done it", "I have done it",
		"All done!", "\ufeff\u00a0done", "\u0085done", "Done", "Done.", "Done—yes", "Done it", "Done is better than perfect",
		"Ran Kirsh", "Created in-house", "Finished goods only", "installed base first", "Deployed", "ranked first", "Donate",
		"createdAt is fine", "finiſhed", "Ship it; done later", "Yes, done", "I'd ran", "I've  done it",
	} {
		cases = append(cases, mailboxCase{name: fmt.Sprintf("post/question-claim=%q", claim), args: append(postQuestion, "--recommended", claim)})
	}
	for _, limit := range []string{"0", "abc", "-5", "2.5", "Infinity", "-Infinity", "", " 7 ", "0x10", "-0", "1e0", "0.4", "-0.4"} {
		cases = append(cases, mailboxCase{name: fmt.Sprintf("list/limit=%q", limit), args: []string{"list", "--limit", limit, "--json"}, seed: seedMailbox(numberedMessages(20), "")})
	}
	for _, c := range cases {
		if c.crash == crashC1 {
			c.errorLine = map[string]string{
				"post/no-from":               "Error: post needs from and subject",
				"post/no-subject":            "Error: post needs from and subject",
				"post/bare-from":             "Error: post needs from and subject",
				"ack/ambiguous":              "Error: ambiguous message id prefix aaaaaaaa",
				"ack/ambiguous-duplicate-id": "Error: ambiguous message id prefix dddd",
			}[c.name]
		}
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.mailboxInvocation(c)) })
	}
	for _, c := range []struct {
		name, id string
		args     []string
	}{
		{"ack/unknown", "zz", []string{"ack", "--id", "zz", "--by", "me"}},
		{"ack/missing-id", "undefined", []string{"ack", "--by", "me"}},
		{"reply/unknown-id-in-project", "zz", []string{"reply", "zz", "hi"}},
	} {
		t.Run(c.name, func(t *testing.T) { h.compareUnknownMessageID(t, mailboxCase{args: c.args, seed: seeded}, c.id) })
	}
	for _, args := range [][]string{{"list"}, {"ack", "--id", "zz", "--by", "me"}, {"reply", "zz", "hi"}, {"inbox"}} {
		t.Run(args[0]+"/outside-project", func(t *testing.T) { h.compareOutsideProject(t, h.mailboxInvocation(mailboxCase{args: args}), "--root") })
	}
	t.Run("post/json-PE-3", h.testMailboxPostJSON)
	t.Run("reply/subject-split-surrogate-PE-1", h.testMailboxReplySplitSurrogate)
	h.testMailboxReplyGate(t)
	t.Run("inbox/orch-reply-hint-unconditional-PE-19", func(t *testing.T) {
		seed := seedMailbox(mailboxMessages+inboxExtras, mailboxAcks)
		h.compareReplyHintWithOrchOnPath(t, h.mailboxInvocation(mailboxCase{args: []string{"inbox"}, seed: seed}))
	})
	t.Run("list/astral-spread-PE-1", h.testMailboxAstralSpread)
	h.testMailboxRegressions(t)
}

// compareUnknownMessageID compares an ack or reply whose id matches no message in the project:
// both runtimes name the mailbox they searched on stderr and exit 1.
func (h *harness) compareUnknownMessageID(t *testing.T, c mailboxCase, id string) {
	t.Helper()
	inv := h.mailboxInvocation(c)
	inv.projectPaths = true
	js, orch := h.runPair(t, inv)
	want := "no message with id " + id + " in $PROJECT/.orchestrator/mailbox; if it belongs to another project, cd there or pass --root <project dir>\n"
	for name, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 1 || o.stdout != "" || o.stderr != want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 1 and %q", name, o.exitCode, o.stdout, o.stderr, want)
		}
	}
	compareOutcomes(t, js, orch)
}

// postQuestion starts a question post. Cases append their own options; the literal has no
// spare capacity, so each append copies it.
var postQuestion = []string{"post", "--from", "conductor", "--to", "human", "--kind", "question", "--subject", "ship?"}

// Questions that carry a recommended answer: blocking, not blocking, a blocking value that is
// not a boolean, a recommendation that is not a string, and one whose newlines and an emoji
// sit across the 200-unit cut. The seeded legacy questions have neither key.
var recommendedQuestions = lines(
	`{"id":"12121212-0000-0000-0000-000000000001","at":"2026-10-18T17:00:00.000Z","from":"conductor","to":"human","kind":"question","subject":"push?","body":"b","refs":[],"thread":null,"recommended":"yes","blocking":true}`,
	`{"id":"12121212-0000-0000-0000-000000000002","at":"2026-10-18T17:01:00.000Z","from":"conductor","to":"*","kind":"question","subject":"name?","body":"","refs":[],"thread":null,"recommended":"`+strings.Repeat("a\\n", 99)+`x😀tail","blocking":false}`,
	`{"id":"12121212-0000-0000-0000-000000000003","at":"2026-10-18T17:02:00.000Z","from":"conductor","to":"human","kind":"question","subject":"odd","body":"b","refs":[],"recommended":7,"blocking":"true"}`,
)

// Questions for the several-questions inbox: long bodies with newlines and an emoji across
// the 400-unit cut (stdout is identical: Node prints the lone surrogate as U+FFFD), no body,
// one already answered by the human, one to someone else.
// Action questions: the steps in the body, no recommendation, blocking or not.
var actionQuestions = lines(
	`{"id":"13131313-0000-0000-0000-000000000001","at":"2026-10-18T17:00:00.000Z","from":"conductor","to":"human","kind":"question","subject":"run setup","body":"1. ./setup.sh\n2. orch version","refs":[],"thread":null,"blocking":true,"ask":"action"}`,
	`{"id":"13131313-0000-0000-0000-000000000002","at":"2026-10-18T17:01:00.000Z","from":"conductor","to":"human","kind":"question","subject":"rotate key","body":"b","refs":[],"thread":null,"blocking":false,"ask":"action"}`,
)

var inboxExtras = lines(
	`{"id":"eeeeeeee-0000-0000-0000-000000000008","at":"2026-10-18T17:00:00.000Z","from":"coder","to":"human","kind":"question","subject":"long","body":"`+strings.Repeat("ab\\n", 133)+`😀tail","refs":[]}`,
	`{"id":"eeeeeeee-0000-0000-0000-000000000009","at":"2026-10-18T18:00:00.000Z","from":"coder","to":"human","kind":"question","subject":"no body","refs":[]}`,
	`{"id":"eeeeeeee-0000-0000-0000-00000000000a","at":"2026-10-18T19:00:00.000Z","from":"coder","to":"human","kind":"question","subject":"answered","body":"x","refs":[]}`,
	`{"id":"eeeeeeee-0000-0000-0000-00000000000b","at":"2026-10-18T20:00:00.000Z","from":"coder","to":"reviewer","kind":"question","subject":"not mine","body":"x","refs":[]}`,
)

func question(n int, to string) string {
	return fmt.Sprintf(`{"id":"99999999-0000-0000-0000-%012d","at":"2026-10-18T10:%02d:00.000Z","from":"p","to":"%s","kind":"question","subject":"q%d","body":"b","refs":[]}`, n, n, to, n)
}

// manyQuestions has 52 questions: the latest 50 hold two for the human, the two older ones
// for the human fall outside the limit.
func manyQuestions() string {
	var all []string
	for n := 0; n < 52; n++ {
		to := "reviewer"
		switch n {
		case 0, 1, 50:
			to = "human"
		case 51:
			to = "*"
		}
		all = append(all, question(n, to))
	}
	return lines(all...)
}

// hiddenQuestions has one question for the human followed by 50 for someone else.
func hiddenQuestions() string {
	all := []string{question(0, "human")}
	for n := 1; n <= 50; n++ {
		all = append(all, question(n, "reviewer"))
	}
	return lines(all...)
}

func numberedMessages(count int) string {
	var all []string
	for n := 0; n < count; n++ {
		all = append(all, fmt.Sprintf(`{"id":"77777777-0000-0000-0000-%012d","kind":"note","subject":"m%d","refs":[]}`, n, n))
	}
	return lines(all...)
}

// testMailboxPostJSON asserts PE-3: with --json orch prints the whole message it appended
// where the JS prints {id, at}; everything else matches.
func (h *harness) testMailboxPostJSON(t *testing.T) {
	inv := h.mailboxInvocation(mailboxCase{args: []string{"post", "--from", "a", "--subject", "s", "--ref", "r", "--json"}})
	inv.normalizeWrites = nil
	js, orch := h.runPair(t, inv)
	const messages = "project/mailbox/messages.jsonl"
	written := strings.TrimSuffix(orch.files[messages], "\n")
	if orch.stdout != written+"\n" {
		t.Errorf("orch stdout %q is not the appended message %q", orch.stdout, written)
	}
	if want := freshUUID.FindString(js.files[messages]); !strings.HasPrefix(js.stdout, `{"id":"`+want+`","at":"`) {
		t.Errorf("js stdout %q is not {id, at} of %q", js.stdout, js.files[messages])
	}
	js, orch = js.normalized(normalizeFresh), orch.normalized(normalizeFresh)
	js.stdout, orch.stdout = "", ""
	compareOutcomes(t, js, orch)
}

// orchReplyHint is the reply hint of mailbox inbox and watch; the JS names mailbox.mjs, which
// PE-19 maps to it.
const orchReplyHint = "orch mailbox reply "

// compareReplyHintWithOrchOnPath runs inv with a stub orch on PATH for this case only. PE-26
// once made orch's hint depend on PATH; under PE-19 it is always orch's, so the pair must
// match byte for byte, the hint included, whether or not orch is on PATH.
func (h *harness) compareReplyHintWithOrchOnPath(t *testing.T, inv invocation) {
	t.Helper()
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "orch"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inv.env = append(append([]string{}, inv.env...), "PATH="+h.path+":"+stubDir)
	js, orch := h.runPair(t, inv)
	if !strings.Contains(orch.stdout, orchReplyHint) {
		t.Errorf("orch stdout should hint %q:\n%s", orchReplyHint, orch.stdout)
	}
	compareOutcomes(t, js, orch)
}

// testMailboxReplyGate compares a reply to a human gate's question, which clears the gate and
// queues the conductor's relaunch (or keeps one already queued), with replies that leave the
// gate alone. The gate records the question as a full id or a prefix, which must resolve to
// the replied message; flip.json's fresh pending_launch.at is normalized.
func (h *harness) testMailboxReplyGate(t *testing.T) {
	gate := func(question string) seed {
		return seeds(seedMailbox(mailboxMessages, mailboxAcks), seedFlip(`{"to":"claude","awaiting_human":{"at":"2026-01-02T00:00:00.000Z","door":"claude","reason":"push","question":`+question+`}}`))
	}
	cases := []mailboxCase{
		{name: "reply/to-gate-question-clears-gate", args: []string{"reply", "bbbb", "yes"}, seed: gate(`"` + idQuestion + `"`)},
		{name: "reply/to-gate-question-prefix-clears-gate", args: []string{"reply", idQuestion, "yes"}, seed: gate(`"bbbbbbbb"`)},
		{name: "reply/other-message-keeps-gate", args: []string{"reply", idStar, "no"}, seed: gate(`"` + idQuestion + `"`)},
		{name: "reply/gate-without-question-kept", args: []string{"reply", "bbbb", "yes"}, seed: gate(`null`)},
		{name: "reply/gate-ambiguous-short-prefix-kept", args: []string{"reply", idReview, "yes"}, seed: gate(`"aaaa"`)},
		{name: "reply/gate-unknown-question-kept", args: []string{"reply", "bbbb", "yes"}, seed: gate(`"eeee"`)},
		{name: "reply/to-gate-question-keeps-pending-launch", args: []string{"reply", "bbbb", "yes"}, seed: seeds(seedMailbox(mailboxMessages, mailboxAcks),
			seedFlip(`{"pending_launch":{"to":"opencode","from":"claude","reason":"handback","resume":null,"at":"2026-01-01T00:00:00.000Z"},"awaiting_human":{"door":"claude","question":"bbbb"}}`))},
		{name: "reply/flip-json-unparsable", args: []string{"reply", "bbbb", "yes"}, seed: seeds(seedMailbox(mailboxMessages, mailboxAcks), seedFlip(`{"to":`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := h.mailboxInvocation(c)
			inv.normalize = normalizeFlip(time.Now())
			h.comparePair(t, inv)
		})
	}
}

// testMailboxReplySplitSurrogate asserts PE-1: the reply subject is cut at 140 UTF-16 units
// inside an emoji; the JS writes the lone high surrogate as "\ud83d", orch writes U+FFFD.
func (h *harness) testMailboxReplySplitSurrogate(t *testing.T) {
	subject := strings.Repeat("s", 135) + "😀"
	inv := h.mailboxInvocation(mailboxCase{args: []string{"reply", "eeee", "ok"}, seed: seedMailbox(lines(`{"id":"eeeeeeee-0000-0000-0000-000000000007","from":"p","subject":"`+subject+`"}`), "")})
	js, orch := h.runPair(t, inv)
	const messages = "project/mailbox/messages.jsonl"
	if !strings.Contains(js.files[messages], `"subject":"Re: `+strings.Repeat("s", 135)+`\ud83d"`) {
		t.Fatalf("js did not write the lone surrogate escape:\n%s", js.files[messages])
	}
	js.files[messages] = strings.Replace(js.files[messages], `\ud83d`, "\ufffd", 1)
	compareOutcomes(t, js, orch)
}
