package mailbox

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

var fixedNow = time.Date(2026, 10, 18, 12, 0, 0, 500_000_000, time.UTC)

// testMailbox returns a mailbox in a temp project with a fixed clock and sequential ids.
func testMailbox(t *testing.T) *Mailbox {
	t.Helper()
	box := New(t.TempDir())
	box.Now = func() time.Time { return fixedNow }
	n := 0
	box.NewID = func() string { n++; return fmt.Sprintf("id-%d", n) }
	return box
}

func TestPostAppendsTheMessageInJSKeyOrder(t *testing.T) {
	box := testMailbox(t)
	msg, err := box.Post(Draft{From: "a", To: "*", Kind: "note", Subject: "s", Body: "b", Refs: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"id-1","at":"2026-10-18T12:00:00.500Z","from":"a","to":"*","kind":"note","subject":"s","body":"b","refs":["x"],"thread":null}`
	if got := marshal(t, msg); got != want {
		t.Errorf("Post returned %s", got)
	}
	if got := readFile(t, filepath.Join(box.Dir, "messages.jsonl")); got != want+"\n" {
		t.Errorf("messages.jsonl = %q", got)
	}
}

// A question records the recommended answer and whether it blocks after thread, blocking by
// default; other kinds ignore both.
func TestPostRecordsAQuestionsRecommendationAndBlocking(t *testing.T) {
	answer, notBlocking := "ship it", false
	head := `{"id":"id-1","at":"2026-10-18T12:00:00.500Z","from":"a","to":"human","kind":"`
	for _, c := range []struct {
		name  string
		draft Draft
		want  string
	}{
		{"blocking by default", Draft{Kind: "question", Recommended: &answer}, head + `question","subject":"s","body":"","refs":[],"thread":null,"recommended":"ship it","blocking":true}`},
		{"not blocking", Draft{Kind: "question", Recommended: &answer, Blocking: &notBlocking}, head + `question","subject":"s","body":"","refs":[],"thread":null,"recommended":"ship it","blocking":false}`},
		{"other kinds ignore all three", Draft{Kind: "note", Recommended: &answer, Blocking: &notBlocking, Ask: AskAction}, head + `note","subject":"s","body":"","refs":[],"thread":null}`},
		{"a decision is written without ask", Draft{Kind: "question", Recommended: &answer, Ask: AskDecision}, head + `question","subject":"s","body":"","refs":[],"thread":null,"recommended":"ship it","blocking":true}`},
		{"an action records ask instead of a recommendation", Draft{Kind: "question", Blocking: &notBlocking, Ask: AskAction}, head + `question","subject":"s","body":"","refs":[],"thread":null,"blocking":false,"ask":"action"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			draft := c.draft
			draft.From, draft.To, draft.Subject, draft.Refs = "a", "human", "s", []string{}
			msg, err := testMailbox(t).Post(draft)
			if err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, msg); got != c.want {
				t.Errorf("Post returned %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestPostRefusesAQuestionWithoutARecommendation(t *testing.T) {
	box := testMailbox(t)
	empty := ""
	for _, recommended := range []*string{nil, &empty} {
		if _, err := box.Post(Draft{From: "a", Kind: "question", Subject: "s", Recommended: recommended}); !errors.Is(err, ErrQuestionNeedsRecommendation) {
			t.Errorf("Post(recommended %v) error = %v, want ErrQuestionNeedsRecommendation", recommended, err)
		}
	}
	if _, err := os.Stat(filepath.Join(box.Dir, "messages.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("messages.jsonl exists after refused posts (stat: %v)", err)
	}
}

// An action takes no recommendation, a decision's may not report work as done, and ask is one of
// the two kinds; each refusal writes nothing.
func TestPostRefusesARecommendationThatCannotStand(t *testing.T) {
	box := testMailbox(t)
	claim, answer := "Done: token created", "ship it"
	for _, c := range []struct {
		draft Draft
		want  error
	}{
		{Draft{Recommended: &answer, Ask: AskAction}, ErrActionTakesNoRecommendation},
		{Draft{Recommended: &claim}, ErrRecommendationClaimsDone},
		{Draft{Recommended: &answer, Ask: "both"}, ErrUnknownAsk},
	} {
		draft := c.draft
		draft.From, draft.Kind, draft.Subject = "a", "question", "s"
		if _, err := box.Post(draft); !errors.Is(err, c.want) {
			t.Errorf("Post(%+v) error = %v, want %v", c.draft, err, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(box.Dir, "messages.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("messages.jsonl exists after refused posts (stat: %v)", err)
	}
}

func TestReadsAsCompletionClaim(t *testing.T) {
	for text, want := range map[string]bool{
		"Done": true, "done.": true, "Done!": true, "Done—token created": true, "Done: token created": true,
		"done, all of it": true, "Done it": true, "  Ran the script": true, "ran: setup.sh": true, "Ran\tit": true,
		"COMPLETED": true, "Finished it": true, "created the env": true, "Deployed successfully": true,
		"rebuilt, ok": true, "Installed all": true, "I ran it": true, "I did it": true, "I've done it": true,
		"I’ve done it": true, "i have done it": true, "All done!": true, "\ufeffdone": true,
		// Openers that start a decision rather than report one, and look-alikes.
		"Done is better than perfect": false, "Ran Kirsh": false, "Created in-house": false,
		"Finished goods only": false, "installed base first": false, "Deployed": false, "rebuilt orch": false,
		"Donate": false, "ranked": false, "Ship it; done later": false, "Yes, done": false, "finiſhed": false,
		"\u0085done": false, "": false,
	} {
		if got := ReadsAsCompletionClaim(text); got != want {
			t.Errorf("ReadsAsCompletionClaim(%q) = %v, want %v", text, got, want)
		}
	}
}

// A worker posts from its lane, which is the JS true for a bare --lane: any truthy from is
// accepted and written as given, as `!from` is the JS test.
func TestPostAcceptsAnyTruthyFrom(t *testing.T) {
	box := testMailbox(t)
	msg, err := box.Post(Draft{From: true, To: "coordinator", Kind: "blocked", Subject: "s", Refs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"id-1","at":"2026-10-18T12:00:00.500Z","from":true,"to":"coordinator","kind":"blocked","subject":"s","body":"","refs":[],"thread":null}`
	if got := marshal(t, msg); got != want {
		t.Errorf("Post returned %s", got)
	}
}

// A flip posts its --reason as the body, which is true for a bare --reason; an absent body is
// the JS default "".
func TestPostWritesTheBodyAsGivenAndDefaultsItToEmpty(t *testing.T) {
	for _, c := range []struct {
		name string
		body any
		want string
	}{
		{"absent", nil, `""`},
		{"string", "why", `"why"`},
		{"bare option", true, `true`},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg, err := testMailbox(t).Post(Draft{From: "a", Subject: "s", Body: c.body})
			if err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, jsjson.Path(msg, "body")); got != c.want {
				t.Errorf("body = %s, want %s", got, c.want)
			}
		})
	}
}

// The JS error lines, captured from Node v26.5.1 (bin/mailbox.mjs), minus the "Error: " the
// CLI adds.
func TestErrorMessagesAreTheJSMessages(t *testing.T) {
	box := testMailbox(t)
	for _, draft := range []Draft{{Subject: "s"}, {From: "", Subject: "s"}, {From: false, Subject: "s"}, {From: 0.0, Subject: "s"}, {From: "a"}} {
		if _, err := box.Post(draft); !errors.Is(err, ErrIncompletePost) || err.Error() != "post needs from and subject" {
			t.Errorf("Post(%+v) error = %v", draft, err)
		}
	}
	writeLines(t, box, "messages.jsonl", `{"id":"abc-1"}`, `{"id":"abc-2"}`, `{"id":"dup"}`, `{"id":"dup"}`)
	cases := []struct {
		prefix string
		is     error
		msg    string
	}{
		{"abc", ErrAmbiguousID, "ambiguous message id prefix abc"},
		{"du", ErrAmbiguousID, "ambiguous message id prefix du"},
		{"zz", ErrNoMessage, "no message with id zz"},
	}
	for _, c := range cases {
		if _, err := box.ResolveID(c.prefix); !errors.Is(err, c.is) || err.Error() != c.msg {
			t.Errorf("ResolveID(%q) error = %v, want %q", c.prefix, err, c.msg)
		}
	}
	if _, err := box.Ack(AckRequest{}); !errors.Is(err, ErrNoMessage) || err.Error() != "no message with id undefined" {
		t.Errorf("Ack without id error = %v", err)
	}
}

func TestResolveID(t *testing.T) {
	box := testMailbox(t)
	writeLines(t, box, "messages.jsonl", `{"id":"abc"}`, `{"id":"abcdef"}`, `{"id":"xyz"}`)
	for prefix, want := range map[string]string{"abc": "abc", "abcd": "abcdef", "x": "xyz"} {
		if got, err := box.ResolveID(prefix); err != nil || got != want {
			t.Errorf("ResolveID(%q) = %q, %v; want %q", prefix, got, err, want)
		}
	}
}

func TestResolveIDPrefixScanThrowsOnAnIDThatIsNotAString(t *testing.T) {
	box := testMailbox(t)
	writeLines(t, box, "messages.jsonl", `{"id":"abc"}`, `{"subject":"no id"}`)
	if got, err := box.ResolveID("abc"); err != nil || got != "abc" {
		t.Errorf("exact match = %q, %v; the exact pass must not throw", got, err)
	}
	var shape *ShapeError
	if _, err := box.ResolveID("ab"); !errors.As(err, &shape) {
		t.Errorf("prefix scan error = %v, want *ShapeError", err)
	}
}

func TestAckOmitsAnAbsentByAndAppendsDuplicates(t *testing.T) {
	box := testMailbox(t)
	writeLines(t, box, "messages.jsonl", `{"id":"m1"}`)
	id := "m"
	for range 2 {
		entry, err := box.Ack(AckRequest{ID: &id, Note: "n"})
		if err != nil {
			t.Fatal(err)
		}
		if got := marshal(t, entry); got != `{"id":"m1","at":"2026-10-18T12:00:00.500Z","note":"n"}` {
			t.Errorf("Ack = %s", got)
		}
	}
	if got := strings.Count(readFile(t, filepath.Join(box.Dir, "acks.jsonl")), "\n"); got != 2 {
		t.Errorf("acks.jsonl has %d lines, want 2", got)
	}
}

func TestListFiltersThenLimitsThenAddsAckedBy(t *testing.T) {
	box := testMailbox(t)
	writeLines(t, box, "messages.jsonl",
		`{"id":"m1","at":"2026-01-01T00:00:00.000Z","to":"*","kind":"note"}`,
		`{"id":"m2","at":"2026-01-02T00:00:00.000Z","to":"bob","kind":"question","acked_by":"kept position"}`,
		`{"id":"m3","at":"2026-01-03T00:00:00.000Z","to":"eve","kind":"question"}`,
	)
	writeLines(t, box, "acks.jsonl", `{"id":"m2","by":"bob"}`, `{"id":"m2","by":"bob"}`, `{"id":"m2"}`, `{"id":"m2","by":"amy"}`)
	cases := []struct {
		q    Query
		want []string
	}{
		{Query{Limit: DefaultLimit}, []string{"m1", "m2", "m3"}},
		{Query{To: "bob", Limit: DefaultLimit}, []string{"m1", "m2"}},
		{Query{Kind: "question", Limit: DefaultLimit}, []string{"m2", "m3"}},
		{Query{Since: "2026-01-01T00:00:00.000Z", Limit: DefaultLimit}, []string{"m2", "m3"}},
		{Query{UnackedBy: "bob", Limit: DefaultLimit}, []string{"m1", "m3"}},
		{Query{Limit: 1}, []string{"m3"}},
		{Query{Kind: "question", Limit: 1}, []string{"m3"}},
	}
	for _, c := range cases {
		msgs, err := box.List(c.q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range msgs {
			ids = append(ids, jsjson.TemplateString(jsjson.Path(m, "id")))
		}
		if strings.Join(ids, ",") != strings.Join(c.want, ",") {
			t.Errorf("List(%+v) = %v, want %v", c.q, ids, c.want)
		}
	}
	msgs, _ := box.List(Query{To: "bob", Kind: "question", Limit: DefaultLimit})
	if got := marshal(t, msgs[0]); got != `{"id":"m2","at":"2026-01-02T00:00:00.000Z","to":"bob","kind":"question","acked_by":["bob",null,"amy"]}` {
		t.Errorf("acked_by = %s", got)
	}
}

// TestSliceStartMatchesNode checks the --limit handling, `list.slice(-Number(limit || 50))`,
// for the limits the parity cases use and more.
func TestSliceStartMatchesNode(t *testing.T) {
	const length = 20
	cases := []struct {
		limit float64
		kept  int
	}{
		{0, 20}, {math.NaN(), 20}, {math.Inf(1), 20}, {math.Inf(-1), 0}, {5, 5}, {2.5, 2}, {-5, 15},
		{-2.5, 18}, {25, 20}, {-25, 0}, {20, 20}, {1, 1}, {0.4, 20}, {-0.4, 20},
	}
	for _, c := range cases {
		if got := length - sliceStart(-c.limit, length); got != c.kept {
			t.Errorf("slice(-%v) keeps %d of %d, want %d", c.limit, got, length, c.kept)
		}
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable; fixed expectations still tested")
	}
	var limits []string
	for _, c := range cases {
		limits = append(limits, jsjson.TemplateString(c.limit))
	}
	script := `const a = Array.from({length: 20}); console.log(process.argv.slice(1).map(l => a.slice(-Number(l)).length).join(","))`
	out, err := exec.Command("node", append([]string{"-e", script}, limits...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range strings.Split(strings.TrimSpace(string(out)), ",") {
		if n, _ := strconv.Atoi(got); n != cases[i].kept {
			t.Errorf("Node slice(-%s) keeps %s, table says %d", limits[i], got, cases[i].kept)
		}
	}
}

// TestAppendIsOneWritePerLine appends from many goroutines and requires every line intact:
// concurrent appenders (orch and the JS) must never interleave.
func TestAppendIsOneWritePerLine(t *testing.T) {
	box := New(t.TempDir())
	big := strings.Repeat("x", 8000)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for range 50 {
				if _, err := box.Post(Draft{From: strconv.Itoa(w), To: "*", Subject: "s", Body: big}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	content := readFile(t, filepath.Join(box.Dir, "messages.jsonl"))
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) != 400 {
		t.Fatalf("%d lines, want 400", len(lines))
	}
	for i, line := range lines {
		if _, err := jsjson.Unmarshal([]byte(line)); err != nil {
			t.Fatalf("line %d is corrupt: %v", i, err)
		}
	}
}

func TestFilesAreCreatedWithTheUmaskApplied(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	box := testMailbox(t)
	if _, err := box.Post(Draft{From: "a", Subject: "s"}); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]os.FileMode{box.Dir: os.ModeDir | 0o755, filepath.Join(box.Dir, "messages.jsonl"): 0o644} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("%s mode %v, want %v", file, info.Mode(), want)
		}
	}
}

func TestNewUUIDIsVersion4(t *testing.T) {
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		id := NewUUID()
		if !v4.MatchString(id) || seen[id] {
			t.Fatalf("NewUUID = %q (repeat: %v)", id, seen[id])
		}
		seen[id] = true
	}
}

func writeLines(t *testing.T, box *Mailbox, name string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(box.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box.Dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
