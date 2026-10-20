package incident

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

var fixedNow = time.Date(2026, 10, 19, 1, 2, 3, 456_000_000, time.UTC)

// testLog is a log in a temp directory with a temp home: no test reads or writes the real HOME.
func testLog(t *testing.T) *Log {
	t.Helper()
	return &Log{File: filepath.Join(t.TempDir(), "state", "incidents.jsonl"), Home: t.TempDir(), Version: "0.5.0 (test)", Now: func() time.Time { return fixedNow }}
}

// TestJSIncidentSuite ports orchestrator/tests/incident.test.mjs, test names unchanged.
func TestJSIncidentSuite(t *testing.T) {
	home := t.TempDir()
	t.Run("redact strips home paths, emails, tokens, uuids and session ids", func(t *testing.T) {
		s := Redact(home, home+"/x and /Users/someone/y me@example.com ses_abc123XYZ 123e4567-e89b-12d3-a456-426614174000")
		if strings.Contains(s, home) {
			t.Errorf("%q still holds the home directory", s)
		}
		if !strings.Contains(s, "~/x and ~/y <email> <session> <uuid>") {
			t.Errorf("redact = %q", s)
		}
	})
	t.Run("fingerprint is stable across numbers and case", func(t *testing.T) {
		if Fingerprint(home, "bridge", "crash", "exit 2 after 31s") != Fingerprint(home, "bridge", "crash", "EXIT 7 after 90s") {
			t.Error("numbers or case changed the fingerprint")
		}
		if Fingerprint(home, "bridge", "crash", "exit 2") == Fingerprint(home, "worker", "crash", "exit 2") {
			t.Error("the component did not change the fingerprint")
		}
	})
	t.Run("summarize groups by fingerprint, most frequent first", func(t *testing.T) {
		e := func(fp, at string) any {
			return object(t, `{"fp":"`+fp+`","at":"`+at+`","component":"x","kind":"bug","summary":"s","project":"p"}`)
		}
		var got []string
		for _, s := range Summarize([]any{e("a", "1"), e("b", "2"), e("a", "3")}) {
			got = append(got, jsjson.TemplateString(s.fp.v)+":"+jsjson.TemplateString(float64(s.count)))
		}
		if want := []string{"a:2", "b:1"}; !slices.Equal(got, want) {
			t.Errorf("summarize = %v, want %v", got, want)
		}
	})
}

func TestRedactPatternsInOrder(t *testing.T) {
	const home = "/srv/h.o$me"
	cases := []struct{ in, want string }{
		{home + "/x " + home + "y", "~/x ~y"},     // every occurrence, even as a prefix
		{"/srv/hXo$me/x", "/srv/hXo$me/x"},        // the home is literal, not a pattern
		{"/Users/bob/x /home/alice/y", "~/x ~/y"}, // home segments
		{"/Users/bob@example.com/x", "~/x"},       // the segment goes before the email pattern sees it
		{"/tmp/sk-abcdefghijklmnopqrstuvwxyz/f", "/tmp/<token>/f"},
		{"me@example.com", "<email>"},
		{"123E4567-E89B-12D3-A456-426614174000", "<uuid>"},
		{"ses_abc123", "<session>"},
		{"/Users/x\vtail /Users/y\u00a0tail /Users/z\u3000tail", "~\vtail ~\u00a0tail ~\u3000tail"}, // JS white space ends a segment
		{"/Users/x\u0085tail", "~"},                                                                 // NEL is not JS white space
		{"$& $1 /Users/bob$&", "$& $1 ~"},                                                           // input "$" stays literal
	}
	for _, c := range cases {
		if got := Redact(home, c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Redact(home, strings.Repeat("a", 1999)+"😀"); got != strings.Repeat("a", 1999)+"\ufffd" {
		t.Errorf("a cut through a surrogate pair should leave U+FFFD (PE-1), got tail %q", got[1995:])
	}
}

func TestScrubTokensReplacesOnlyTokens(t *testing.T) {
	apiKey := "sk-" + "ant-" + strings.Repeat("x", 24)
	jwt := "eyJ" + strings.Repeat("a", 20) + "." + strings.Repeat("b", 8)
	cases := []struct{ in, want string }{
		{"401: invalid key " + apiKey + " (expired)", "401: invalid key <token> (expired)"},
		{"Bearer " + jwt, "Bearer <token>"},
		{"sk-x too short", "sk-x too short"},
		{"/Users/bob me@example.com $&", "/Users/bob me@example.com $&"}, // only the token rule applies
	}
	for _, c := range cases {
		if got := ScrubTokens(c.in); got != c.want {
			t.Errorf("ScrubTokens(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if long := strings.Repeat("a", 3000); ScrubTokens(long) != long {
		t.Error("ScrubTokens should not cut its text")
	}
}

// corpus is testdata/incident/corpus.json, written by gen.mjs from lib/incident.mjs.
func corpus(t *testing.T) *jsjson.Object {
	t.Helper()
	data, err := os.ReadFile("../../testdata/incident/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	return object(t, string(data))
}

// TestCorpusMatchesNode replays every case of the Node corpus: all redaction patterns and their
// overlaps, JS-only white space, final sigma, İ, scripts with case, the 2000 and 200 cuts through
// surrogate pairs (Node's lone surrogate is decoded as U+FFFD, as orch writes it: PE-1),
// redaction and fingerprints with an empty home, tie-heavy summaries below 64 groups and
// mixed-primitive `at` summaries of 60 to 224 groups, whose order follows V8's sort.
func TestCorpusMatchesNode(t *testing.T) {
	c := corpus(t)
	home := jsjson.Path(c, "home").(string)
	for i, item := range jsjson.Path(c, "redact").([]any) {
		in, want := jsjson.Path(item, "input").(string), jsjson.Path(item, "output").(string)
		if got := Redact(home, in); got != want {
			t.Errorf("redact case %d (%.40q): got %.80q, Node %.80q", i, in, got, want)
		}
	}
	for i, item := range jsjson.Path(c, "fingerprint").([]any) {
		component, _ := jsjson.Lookup(item, "component")
		kind, message := jsjson.Path(item, "kind").(string), jsjson.Path(item, "message").(string)
		if got, want := Fingerprint(home, component, kind, message), jsjson.Path(item, "fp"); got != want {
			t.Errorf("fingerprint case %d (%v, %s, %.40q): got %s, Node %s", i, component, kind, message, got, want)
		}
	}
	for i, item := range jsjson.Path(c, "summarize").([]any) {
		var got []any
		for _, s := range Summarize(jsjson.Path(item, "entries").([]any)) {
			got = append(got, s.Object())
		}
		if g, w := marshal(t, got), marshal(t, jsjson.Path(item, "summary")); g != w {
			t.Errorf("summarize case %d:\n got %s\nNode %s", i, g, w)
		}
	}
	for i, item := range jsjson.Path(c, "summarizeOrder").([]any) {
		if g, w := marshal(t, summaryFPs(Summarize(jsjson.Path(item, "entries").([]any)))), marshal(t, jsjson.Path(item, "fps")); g != w {
			t.Errorf("summarize order case %d:\n got %s\nNode %s", i, g, w)
		}
	}
	for i, item := range jsjson.Path(c, "emptyHome", "redact").([]any) {
		in, want := jsjson.Path(item, "input").(string), jsjson.Path(item, "output").(string)
		if got := Redact("", in); got != want {
			t.Errorf("empty-home redact case %d (%.40q): got %.80q, Node %.80q", i, in, got, want)
		}
	}
	for i, item := range jsjson.Path(c, "emptyHome", "fingerprint").([]any) {
		message := jsjson.Path(item, "message").(string)
		if got, want := Fingerprint("", "c", "note", message), jsjson.Path(item, "fp"); got != want {
			t.Errorf("empty-home fingerprint case %d (%.40q): got %s, Node %s", i, message, got, want)
		}
	}
}

func summaryFPs(summaries []Summary) []any {
	fps := make([]any, len(summaries))
	for i, s := range summaries {
		fps[i] = s.fp.v
	}
	return fps
}

// TestShortIncomparableAtFollowsNode26 pins PE-5 without Node: three groups whose `at` values
// ("t0", true, "2") do not compare sort as V8 14.6 (Node 26, the parity reference) sorts them,
// fp 1,0,2; Node 22 and 24 give 2,1,0.
func TestShortIncomparableAtFollowsNode26(t *testing.T) {
	entries := []any{
		object(t, `{"fp":"0","at":"t0","component":"c","kind":"note","summary":"s"}`),
		object(t, `{"fp":"1","at":true,"component":"c","kind":"note","summary":"s"}`),
		object(t, `{"fp":"2","at":"2","component":"c","kind":"note","summary":"s"}`),
	}
	if got := marshal(t, summaryFPs(Summarize(entries))); got != `["1","0","2"]` {
		t.Errorf("fp order %s, want Node 26's [\"1\",\"0\",\"2\"]", got)
	}
}

// TestReviewerMixedAtFixture pins R4 of reviews/go-s4b.md: 80 groups of one entry each whose
// unique `at` values mix numbers and strings (incomparable pairs, no ties). V8's merges decide
// the order, which Node printed as starting 9,76,75,72,71.
func TestReviewerMixedAtFixture(t *testing.T) {
	data := readFile(t, "../../testdata/incident/mixed-at-80.jsonl")
	var entries []any
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		entries = append(entries, object(t, line))
	}
	fps := summaryFPs(Summarize(entries))
	if len(fps) != 80 || marshal(t, fps[:5]) != `["9","76","75","72","71"]` {
		t.Errorf("first fps %s of %d, want Node's [\"9\",\"76\",\"75\",\"72\",\"71\"] of 80", marshal(t, fps[:min(5, len(fps))]), len(fps))
	}
}

func TestRecordWritesTheEntryInJSOrder(t *testing.T) {
	log := testLog(t)
	context := object(t, `{"b":"`+log.Home+`/f","1":"x","n":2}`)
	entry, err := log.Record(Draft{Component: "bridge", Kind: "crash", Summary: "exit 2 in " + log.Home, Detail: "me@example.com", Context: context, Project: "/work/repo/"})
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint(log.Home, "bridge", "crash", "exit 2 in "+log.Home)
	want := `{"at":"2026-10-19T01:02:03.456Z","fp":"` + fp + `","component":"bridge","kind":"crash","summary":"exit 2 in ~","detail":"<email>",` +
		`"context":{"1":"x","b":"~/f","n":2},"project":"repo","version":"0.5.0 (test)","platform":"` + runtime.GOOS + `"}`
	if got := marshal(t, entry); got != want {
		t.Errorf("entry\n got %s\nwant %s", got, want)
	}
	if got := readFile(t, log.File); got != want+"\n" {
		t.Errorf("file = %q", got)
	}
}

func TestRecordKindsAndProjects(t *testing.T) {
	cases := []struct {
		draft                    Draft
		kind, project, component string
	}{
		{Draft{Component: "c", Summary: "s"}, "bug", "null", `"c"`},
		{Draft{Component: "c", Kind: "frob", Summary: "s"}, "note", "null", `"c"`},
		{Draft{Component: true, Kind: "limit", Summary: "s", Project: "/"}, "limit", `""`, "true"},
		{Draft{Component: "c", Summary: "s", Project: "a/b//"}, "bug", `"b"`, `"c"`},
		{Draft{Component: "c", Summary: "s", Project: "."}, "bug", `"."`, `"c"`},
	}
	for _, c := range cases {
		entry, err := testLog(t).Record(c.draft)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsjson.Path(entry, "kind"); got != c.kind {
			t.Errorf("%+v: kind %v, want %s", c.draft, got, c.kind)
		}
		if got := marshal(t, jsjson.Path(entry, "project")); got != c.project {
			t.Errorf("%+v: project %s, want %s", c.draft, got, c.project)
		}
		if got := marshal(t, jsjson.Path(entry, "component")); got != c.component {
			t.Errorf("%+v: component %s, want %s", c.draft, got, c.component)
		}
	}
}

func TestRecordCutsTheSummaryAt300UnitsAndRefusesIncompleteDrafts(t *testing.T) {
	entry, err := testLog(t).Record(Draft{Component: "c", Summary: strings.Repeat("a", 299) + "😀"})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsjson.Path(entry, "summary"); got != strings.Repeat("a", 299)+"\ufffd" {
		t.Errorf("summary tail %q, want U+FFFD after 299 a (PE-1)", got.(string)[290:])
	}
	for _, d := range []Draft{{Component: "c"}, {Summary: "s"}, {Component: "", Summary: "s"}} {
		log := testLog(t)
		if _, err := log.Record(d); err != ErrIncomplete || err.Error() != "incident needs component and summary" {
			t.Errorf("Record(%+v) error = %v", d, err)
		}
		if _, err := os.Stat(log.File); !os.IsNotExist(err) {
			t.Errorf("Record(%+v) wrote the log", d)
		}
	}
}

func TestReadFiltersAsTheJSCompares(t *testing.T) {
	log := testLog(t)
	writeLines(t, log.File,
		`{"at":"2026-10-18T10:00:00.000Z","kind":"bug","component":"bridge"}`,
		`{"at":"2026-10-18T12:00:00.000Z","kind":"crash","component":"worker"}`,
		`{"at":5,"kind":true,"component":true}`,
		`"a string"`, `[1]`, `7`, `not json`, `null`,
	)
	cases := []struct {
		filter Filter
		want   string
	}{
		{Filter{}, `[{"at":"2026-10-18T10:00:00.000Z","kind":"bug","component":"bridge"},{"at":"2026-10-18T12:00:00.000Z","kind":"crash","component":"worker"},{"at":5,"kind":true,"component":true},"a string",[1],7]`},
		{Filter{Since: "2026-10-18T11"}, `[{"at":"2026-10-18T12:00:00.000Z","kind":"crash","component":"worker"},"a string",[1]]`}, // "function at() ..." >= "2026"
		{Filter{Since: true}, `[{"at":5,"kind":true,"component":true}]`},                                                           // Number(at) >= 1
		{Filter{Since: ""}, `[{"at":"2026-10-18T10:00:00.000Z","kind":"bug","component":"bridge"},{"at":"2026-10-18T12:00:00.000Z","kind":"crash","component":"worker"},{"at":5,"kind":true,"component":true},"a string",[1],7]`},
		{Filter{Kind: "crash"}, `[{"at":"2026-10-18T12:00:00.000Z","kind":"crash","component":"worker"}]`},
		{Filter{Kind: true, Component: true}, `[{"at":5,"kind":true,"component":true}]`},
		{Filter{Component: "bridge"}, `[{"at":"2026-10-18T10:00:00.000Z","kind":"bug","component":"bridge"}]`},
	}
	for _, c := range cases {
		entries, err := log.Read(c.filter)
		if err != nil {
			t.Fatal(err)
		}
		if got := marshal(t, entries); got != c.want {
			t.Errorf("Read(%+v)\n got %s\nwant %s", c.filter, got, c.want)
		}
	}
	empty, err := testLog(t).Read(Filter{})
	if err != nil || marshal(t, empty) != "[]" {
		t.Errorf("Read of a missing log = %v, %v; want []", empty, err)
	}
}

func TestListAndReportLinesOfOddShapes(t *testing.T) {
	cases := []struct {
		entry any
		want  string
	}{
		{object(t, `{"at":"A","fp":"f","component":"c","kind":"k","project":"p","summary":"s"}`), "A f c/k p: s"},
		{object(t, `{"project":""}`), "undefined undefined undefined/undefined -: undefined"},
		{"text", "function at() { [native code] } undefined undefined/undefined -: undefined"},
		{[]any{1.0}, "function at() { [native code] } undefined undefined/undefined -: undefined"},
		{7.0, "undefined undefined undefined/undefined -: undefined"},
		{object(t, `{"at":null,"project":["a","b"],"summary":{"x":1}}`), "null undefined undefined/undefined a,b: [object Object]"},
	}
	for _, c := range cases {
		if got := ListLine(c.entry); got != c.want {
			t.Errorf("ListLine(%v) = %q, want %q", c.entry, got, c.want)
		}
	}

	summaries := Summarize([]any{
		object(t, `{"fp":"a","at":"2026-10-18T10:00:00.000Z","component":true,"kind":"bug","summary":"s","project":"p"}`),
		object(t, `{"fp":"a","at":["x","y"],"project":"q"}`),
	})
	if got, err := summaries[0].ReportLine(); err != nil || got != "  2x a true/bug [p,q] s  (first 2026-10-18T10:00, last x,y)" {
		t.Errorf("ReportLine = %q, %v", got, err)
	}
	for _, entries := range [][]any{{"text"}, {object(t, `{"fp":"a"}`)}, {object(t, `{"fp":"a","at":5}`)}} {
		if _, err := Summarize(entries)[0].ReportLine(); err == nil {
			t.Errorf("ReportLine of %v: want a TypeError (C2)", entries)
		} else if _, isShape := err.(*ShapeError); !isShape {
			t.Errorf("ReportLine of %v: error %T, want *ShapeError", entries, err)
		}
	}
}

// TestNewKeepsAnEmptyHome: an explicitly empty HOME gives the JS's relative log path, never
// the user's own log, and empty-home redaction.
func TestNewKeepsAnEmptyHome(t *testing.T) {
	home := t.TempDir()
	if log := New(home); log.Home != home || log.File != filepath.Join(home, ".local", "state", "opencode", "orchestrator", "incidents.jsonl") {
		t.Errorf("New(%q) = %+v", home, log)
	}
	if log := New(""); log.Home != "" || log.File != ".local/state/opencode/orchestrator/incidents.jsonl" {
		t.Errorf(`New("") = %+v`, log)
	}
}

// TestRecordWithEmptyHomeStaysInTheWorkingDirectory records with HOME="" from a temp working
// directory: the entry lands under it, redacted as Node does for an empty home.
func TestRecordWithEmptyHomeStaysInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	log := New("")
	log.Now = func() time.Time { return fixedNow }
	entry, err := log.Record(Draft{Component: "c", Summary: "ab"})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsjson.Path(entry, "summary"); got != "~a~b~" {
		t.Errorf("summary %q, want Node's \"~a~b~\"", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".local", "state", "opencode", "orchestrator", "incidents.jsonl")); err != nil {
		t.Errorf("log not under the working directory: %v", err)
	}
}

func object(t *testing.T, text string) *jsjson.Object {
	t.Helper()
	v, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*jsjson.Object)
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readFile(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeLines(t *testing.T, file string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
