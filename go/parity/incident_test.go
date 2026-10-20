//go:build parity

package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const incidentsFile = ".local/state/opencode/orchestrator/incidents.jsonl"

// Seeded entries use timestamps of the JS format, but the record cases that append never seed
// ISO ones, so the fresh-value normalizers below only ever rewrite appended entries.
var seededIncidents = lines(
	`{"at":"2026-10-18T10:00:00.000Z","fp":"aaaaaaaaaaaa","component":"bridge","kind":"crash","summary":"exit 2","detail":"","context":{},"project":"alpha","version":"v","platform":"darwin"}`,
	`{"at":"2026-10-18T11:00:00.000Z","fp":"bbbbbbbbbbbb","component":"worker","kind":"timeout","summary":"slow","detail":"","context":{},"project":null,"version":"v","platform":"linux"}`,
	`{"at":"2026-10-18T12:00:00.000Z","fp":"aaaaaaaaaaaa","component":"bridge","kind":"crash","summary":"exit 7","detail":"","context":{},"project":"beta","version":"v","platform":"darwin"}`,
	`{"at":"2026-10-18T12:00:00.000Z","fp":"cccccccccccc","component":"plugin","kind":"bug","summary":"tie with b","detail":"","context":{},"project":"","version":"v","platform":"darwin"}`,
	`{"at":"2026-10-18T11:00:00.000Z","fp":"dddddddddddd","component":true,"kind":true,"summary":"bare flags","project":"alpha"}`,
	`{"at":"2026-10-18T12:00:00.000Z","fp":"eeeeeeeeeeee","component":"plugin","kind":"bug","summary":"same count and last as c","project":"gamma"}`,
)

// Hand-edited oddities: unparsable and falsy lines are dropped, other values kept whatever
// their shape (strings and arrays inherit an `at` method).
var malformedIncidents = lines(
	`{"at":"2026-10-18T10:00:00.000Z","fp":"aaaaaaaaaaaa","component":"c","kind":"bug","summary":"ok","project":"p"}`,
	`not json`, `0`, `null`, `false`, `""`, `"stré"`, `5`, `[1,2]`, `{}`, `{"fp":"aaaaaaaaaaaa","at":5,"project":"p"}`,
)

func readTestdata(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func seedProjectFile(rel, content string) seed {
	return func(t *testing.T, sb sandbox) { writeSeedFile(t, filepath.Join(sb.project, rel), content) }
}

func manyIncidents(count int) string {
	var all []string
	for i := range count {
		all = append(all, fmt.Sprintf(`{"at":"2026-10-18T10:%02d:%02d.000Z","fp":"f%011d","component":"c","kind":"note","summary":"n%d","project":"p"}`, i/60, i%60, i%7, i))
	}
	return lines(all...)
}

// Fresh values, rewritten only in their exact place and format. The project basename of a
// record without --cwd is the sandbox's temp directory name, which differs between runs.
var (
	freshIncidentAt  = regexp.MustCompile(`(?m)^(\{"at":")` + isoMillis.String() + `(","fp":")`)
	freshReceiptAt   = regexp.MustCompile(`(?m)^(\{"recorded":"[0-9a-f]{12}","at":")` + isoMillis.String() + `("\}$)`)
	incidentVersion  = regexp.MustCompile(`(?m)("version":)"(?:[^"\\]|\\.)*"(,"platform":"(?:darwin|linux)"\}$)`)
	tempProject      = regexp.MustCompile(`("project":)"\d{3}"(,"version":)`)
	shareGeneratedAt = regexp.MustCompile(`(?m)^(  "generated_at": ")` + isoMillis.String() + `(",)$`)
	incidentsLogPath = regexp.MustCompile(`\(log: /[^()\n]*/\.local/state/opencode/orchestrator/incidents\.jsonl\)`)
)

func normalizeIncident(s string) string {
	s = freshIncidentAt.ReplaceAllString(s, "${1}<iso>${2}")
	s = freshReceiptAt.ReplaceAllString(s, "${1}<iso>${2}")
	s = incidentVersion.ReplaceAllString(s, `${1}"<version>"${2}`)
	s = tempProject.ReplaceAllString(s, `${1}"<tmp>"${2}`)
	s = shareGeneratedAt.ReplaceAllString(s, "${1}<iso>${2}")
	return incidentsLogPath.ReplaceAllString(s, "(log: $$HOME/.local/state/opencode/orchestrator/incidents.jsonl)")
}

type incidentCase struct {
	name       string
	args       []string
	env        []string
	stdin      string
	seed       seed
	extraFiles []string
	crash      crashClass
	errorLine  string
}

func (h *harness) incidentInvocation(c incidentCase) invocation {
	return invocation{
		js:         append([]string{h.node, filepath.Join(h.orchJS, "bin", "incident.mjs")}, c.args...),
		orch:       append([]string{h.orchBin, "incident"}, c.args...),
		stdin:      []byte(c.stdin),
		env:        c.env,
		seed:       c.seed,
		extraFiles: c.extraFiles,
		normalize:  normalizeIncident,
		crash:      c.crash,
		errorLine:  c.errorLine,
	}
}

// testIncident compares `orch incident` with bin/incident.mjs. Entries differ in "version"
// (PE-4), which is normalized.
func (h *harness) testIncident(t *testing.T) {
	seeded := seedFile(incidentsFile, seededIncidents)
	malformed := seedFile(incidentsFile, malformedIncidents)
	share := []string{"incidents-share.json", "out/bundle.json"}
	const (
		bareCwd    = `TypeError [ERR_INVALID_ARG_TYPE]: The "path" argument must be of type string. Received type boolean (true)`
		bareOut    = `TypeError [ERR_INVALID_ARG_TYPE]: The "path" argument must be of type string or an instance of Buffer or URL. Received type boolean (true)`
		ctxPush    = "TypeError: opts.ctx.push is not a function"
		ctxMap     = "TypeError: opts.ctx.map is not a function"
		incomplete = "Error: incident needs component and summary"
	)
	cases := []incidentCase{
		{name: "record/all-flags", args: []string{"record", "--component", "bridge", "--kind", "crash", "--summary", "exit 2 after 31s in /Users/bob/x for me@example.com", "--detail", "ses_abc123 123E4567-E89B-12D3-A456-426614174000 $& sk-" + strings.Repeat("k", 20), "--cwd", "/work/repo", "--ctx", "thread=/home/alice/t", "positional"}},
		{name: "record/defaults", args: []string{"record", "--summary", "s", "--component", "", "--kind", ""}},
		{name: "record/unknown-kind", args: []string{"record", "--summary", "s", "--kind", "frob"}},
		{name: "record/bare-flags", args: []string{"record", "--component", "--kind", "--summary", "--detail"}},
		{name: "record/detail-stdin", args: []string{"record", "--summary", "s", "--detail", "-"}, stdin: "hi\xff\xe2\x82 there\xf0\x9f\x98\x80\n\ufeffbom /Users/x\vy"},
		{name: "record/ctx-forms", args: []string{"record", "--summary", "s", "--ctx", "a=b", "--ctx", "c=d=e", "--ctx", "bare", "--ctx", "=x", "--ctx", "1=one", "--ctx", "a=again", "--ctx", "u=1", "--ctx", "u", "--ctx", "z", "--ctx", "z=late", "--ctx", "__proto__=p"}},
		{name: "record/ctx-empty-value", args: []string{"record", "--summary", "s", "--ctx", ""}},
		{name: "record/cwd-trailing-slash", args: []string{"record", "--summary", "s", "--cwd", "/x/y/"}},
		{name: "record/cwd-root", args: []string{"record", "--summary", "s", "--cwd", "/"}},
		{name: "record/cwd-relative", args: []string{"record", "--summary", "s", "--cwd", "sub/dir"}},
		{name: "record/cwd-empty-is-cwd", args: []string{"record", "--summary", "s", "--cwd", ""}},
		{name: "record/appends", args: []string{"record", "--summary", "again"}, seed: seedFile(incidentsFile, `{"at":"seed","fp":"x"}`+"\n")},
		{name: "record/greek-and-dotted-i", args: []string{"record", "--summary", "ΟΔΟΣ İΣΑΣ 42", "--component", "ΣΑΣ"}},
		{name: "record/cwd-bare", args: []string{"record", "--summary", "s", "--cwd"}, crash: crashC1, errorLine: bareCwd},
		{name: "record/no-summary", args: []string{"record", "--component", "c"}, crash: crashC1, errorLine: incomplete},
		{name: "record/empty-summary", args: []string{"record", "--summary", ""}, crash: crashC1, errorLine: incomplete},
		{name: "record/no-summary-before-bare-cwd", args: []string{"record", "--cwd"}, crash: crashC1, errorLine: incomplete},
		{name: "record/stdin-read-before-error", args: []string{"record", "--detail", "-"}, stdin: "read first", crash: crashC1, errorLine: incomplete},
		{name: "record/trailing-bare-ctx", args: []string{"record", "--summary", "s", "--ctx", "a=b", "--ctx"}, crash: crashC1, errorLine: ctxMap},
		{name: "record/trailing-bare-ctx-before-summary-check", args: []string{"record", "--ctx"}, crash: crashC1, errorLine: ctxMap},
		{name: "record/ctx-after-bare-ctx", args: []string{"record", "--ctx", "--ctx", "a=b", "--summary", "s"}, crash: crashC1, errorLine: ctxPush},

		{name: "list/empty", args: []string{"list"}},
		{name: "list/text", args: []string{"list"}, seed: seeded},
		{name: "list/json", args: []string{"list", "--json"}, seed: seeded},
		{name: "list/json-value-is-json", args: []string{"list", "--json", "x"}, seed: seeded},
		{name: "list/json-empty", args: []string{"list", "--json"}},
		{name: "list/since", args: []string{"list", "--since", "2026-10-18T11:00:00.000Z"}, seed: seeded},
		{name: "list/kind", args: []string{"list", "--kind", "crash"}, seed: seeded},
		{name: "list/component", args: []string{"list", "--component", "plugin", "--json"}, seed: seeded},
		{name: "list/bare-filters", args: []string{"list", "--since", "--kind"}, seed: seeded},
		{name: "list/bare-kind-and-component-match-true", args: []string{"list", "--kind", "--component"}, seed: seeded},
		{name: "list/empty-filters", args: []string{"list", "--since", "", "--kind", "", "--component", ""}, seed: seeded},
		{name: "list/over-100", args: []string{"list"}, seed: seedFile(incidentsFile, manyIncidents(105))},
		{name: "list/over-100-json", args: []string{"list", "--json"}, seed: seedFile(incidentsFile, manyIncidents(105))},
		{name: "list/malformed-text", args: []string{"list"}, seed: malformed},
		{name: "list/malformed-json", args: []string{"list", "--json"}, seed: malformed},
		{name: "list/malformed-since", args: []string{"list", "--since", "2026", "--json"}, seed: malformed},
		{name: "list/trailing-bare-ctx", args: []string{"list", "--ctx"}, seed: seeded},
		{name: "list/ctx-after-bare-ctx", args: []string{"list", "--ctx", "a", "--ctx", "--ctx", "b"}, seed: seeded, crash: crashC1, errorLine: ctxPush},

		{name: "report/empty", args: []string{"report"}},
		{name: "report/ties-and-projects", args: []string{"report"}, seed: seeded},
		{name: "report/since", args: []string{"report", "--since", "2026-10-18T11:30:00.000Z", "--kind", "ignored"}, seed: seeded},
		{name: "report/many", args: []string{"report"}, seed: seedFile(incidentsFile, manyIncidents(105))},
		{name: "report/malformed", args: []string{"report"}, seed: malformed, crash: crashC2},
		{name: "report/bad-at-after-good-line", args: []string{"report"}, crash: crashC2, seed: seedFile(incidentsFile, lines(
			`{"fp":"a","at":"2026-10-18T10:00:00.000Z","summary":"printed"}`, `{"fp":"a","at":"2026-10-18T11:00:00.000Z"}`, `{"fp":"z","at":5,"summary":"crashes"}`))},
		{name: "report/at-array", args: []string{"report"}, seed: seedFile(incidentsFile, lines(`{"fp":"a","at":["2026-10-18T10:00:00.000Z","x"],"summary":"s","project":["p","q"]}`))},

		{name: "share/default", args: []string{"share"}, seed: seeded, extraFiles: share},
		{name: "share/out-since", args: []string{"share", "--out", "out/bundle.json", "--since", "2026-10-18T11:30:00.000Z"}, seed: seeds(seeded, seedProjectFile("out/.keep", "")), extraFiles: share},
		{name: "share/empty-out-is-default", args: []string{"share", "--out", ""}, extraFiles: share},
		{name: "share/reviewer-mixed-at-80", args: []string{"share"}, seed: seedFile(incidentsFile, readTestdata(t, "incident/mixed-at-80.jsonl")), extraFiles: share},
		{name: "share/malformed", args: []string{"share"}, seed: malformed, extraFiles: share},
		{name: "share/out-missing-dir", args: []string{"share", "--out", "missing/x.json"}, seed: seeded, extraFiles: share, crash: crashC1, errorLine: "Error: ENOENT: no such file or directory, open 'missing/x.json'"},
		{name: "share/bare-out", args: []string{"share", "--out"}, seed: seeded, extraFiles: share, crash: crashC1, errorLine: bareOut},

		{name: "empty-home/record", args: []string{"record", "--summary", "exit 2 in /Users/bob me@example.com", "--detail", "", "--ctx", "k=/home/x"}, env: emptyHome, extraFiles: emptyHomeLog},
		{name: "empty-home/list", args: []string{"list"}, env: emptyHome, seed: seedProjectFile(relativeIncidentsFile, seededIncidents)},
		{name: "empty-home/report", args: []string{"report"}, env: emptyHome, seed: seedProjectFile(relativeIncidentsFile, seededIncidents)},
		{name: "empty-home/share", args: []string{"share"}, env: emptyHome, seed: seedProjectFile(relativeIncidentsFile, seededIncidents), extraFiles: share},
		{name: "empty-home/unknown-command", args: []string{"frob"}, env: emptyHome},
		{name: "unknown-command", args: []string{"frob"}},
		{name: "no-command", args: nil},
		{name: "unknown-command-ctx-after-bare-ctx", args: []string{"frob", "--ctx", "--ctx", "a=b"}, crash: crashC1, errorLine: ctxPush},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.incidentInvocation(c)) })
	}
	t.Run("record/summary-split-surrogate-PE-1", h.testIncidentSplitSurrogate)
	t.Run("empty-home/record-astral-PE-1", h.testIncidentEmptyHomeAstral)
	t.Run("share/short-incomparable-at-PE-5", h.testIncidentShortIncomparableAt)
	t.Run("jssort-reference-PE-5", h.testJSSortReference)
	t.Run("share/no-project-key", func(t *testing.T) { h.testIncidentShareHasNoProject(t, seeded) })
	t.Run("record/request-PE-25", h.testIncidentRequest)
	t.Run("record/request-equals-flags-PE-25", h.testIncidentRequestEqualsFlags)
}

// testIncidentRequest runs `record --request -` on orch alone: bin/incident.mjs has no such
// option (PE-25).
func (h *harness) testIncidentRequest(t *testing.T) {
	const usage = "orch incident record: "
	cases := []struct {
		name, stdin string
		args        []string
		exit        int
		entry       string // what the log's one line holds, after normalizeIncident
		stderr      string
	}{
		{name: "full", stdin: `{"component":"bridge","kind":"crash","summary":"exit 2 for me@example.com","detail":"d","context":{"thread":"/home/alice/t","n":1},"project":"/work/repo"}`,
			entry: `"component":"bridge","kind":"crash","summary":"exit 2 for <email>","detail":"d","context":{"thread":"~/t","n":1},"project":"repo","version":"<version>"`},
		{name: "defaults", stdin: `{"summary":"s"}`,
			entry: `"component":"agent","kind":"note","summary":"s","detail":"","context":{},"project":"<tmp>","version":"<version>"`},
		{name: "missing-summary", stdin: `{"component":"c"}`, exit: 1, stderr: "Error: incident needs component and summary\n"},
		{name: "invalid-json", stdin: `{"summary":`, exit: 2},
		{name: "conflicting-flag", stdin: `{"summary":"s"}`, args: []string{"--kind", "bug"}, exit: 2, stderr: usage + "--request cannot be combined with --kind\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb := h.newSandbox(t)
			argv := append([]string{h.orchBin, "incident", "record", "--request", "-"}, c.args...)
			o := h.run(t, sb, argv, []byte(c.stdin), nil)
			entry := normalizeIncident(o.files["state/incidents.jsonl"])
			switch {
			case o.exitCode != c.exit:
				t.Errorf("exit %d, want %d; stderr %q", o.exitCode, c.exit, o.stderr)
			case c.exit != 0 && (o.stdout != "" || entry != "" || strings.Count(o.stderr, "\n") != 1 || (c.stderr != "" && o.stderr != c.stderr)):
				t.Errorf("stdout %q, stderr %q, log %q; want one error line %q and no entry", o.stdout, o.stderr, entry, c.stderr)
			case c.exit == 0 && (!freshReceiptAt.MatchString(o.stdout) || !strings.Contains(entry, c.entry) || o.stderr != ""):
				t.Errorf("stdout %q, stderr %q, log %q; want a receipt and %s", o.stdout, o.stderr, entry, c.entry)
			}
		})
	}
}

// testIncidentRequestEqualsFlags checks that orch's `--request -` writes the line, and prints the
// receipt, that bin/incident.mjs writes for the same fields given as flags.
func (h *harness) testIncidentRequestEqualsFlags(t *testing.T) {
	apiKey := "sk-" + "ant-" + strings.Repeat("k", 20)
	flags := h.incidentInvocation(incidentCase{args: []string{"record", "--component", "plugin", "--kind", "timeout", "--summary", "slow in /Users/bob " + apiKey, "--detail", "ses_abc123", "--ctx", "k=v", "--cwd", "/work/repo"}})
	request := `{"component":"plugin","kind":"timeout","summary":"slow in /Users/bob ` + apiKey + `","detail":"ses_abc123","context":{"k":"v"},"project":"/work/repo"}`
	inv := invocation{
		js:        flags.js,
		orch:      []string{h.orchBin, "incident", "record", "--request", "-"},
		stdin:     []byte(request),
		normalize: normalizeIncident,
	}
	h.comparePair(t, inv)
}

// testIncidentShareHasNoProject checks the privacy promise of share on top of parity: the
// bundle holds no "project" key at any depth, though every seeded entry has one.
func (h *harness) testIncidentShareHasNoProject(t *testing.T, seeded seed) {
	js, orch := h.runPair(t, h.incidentInvocation(incidentCase{args: []string{"share"}, seed: seeded, extraFiles: []string{"incidents-share.json"}}))
	for runtime, o := range map[string]outcome{"js": js, "orch": orch} {
		bundle := o.files["extra/incidents-share.json"]
		if !strings.Contains(bundle, `"fp": "aaaaaaaaaaaa"`) || strings.Contains(bundle, `"project":`) {
			t.Errorf("%s bundle lacks the entries or keeps a project key:\n%s", runtime, bundle)
		}
	}
	compareOutcomes(t, js, orch)
}

// An explicitly empty HOME: Node's os.homedir() is "", so the log is relative to the working
// directory (the sandbox project) and the home pattern is empty.
var (
	emptyHome             = []string{"HOME="}
	relativeIncidentsFile = ".local/state/opencode/orchestrator/incidents.jsonl"
	emptyHomeLog          = []string{relativeIncidentsFile}
)

// testIncidentEmptyHomeAstral asserts PE-1 for the empty home pattern, which matches between
// the two halves of a surrogate pair: the JS writes "~\ud83d~\ude00~", orch U+FFFD for each
// half. stdout (fingerprint included) is identical.
func (h *harness) testIncidentEmptyHomeAstral(t *testing.T) {
	js, orch := h.runPair(t, h.incidentInvocation(incidentCase{args: []string{"record", "--summary", "a😀"}, env: emptyHome, extraFiles: emptyHomeLog}))
	file := "extra/" + relativeIncidentsFile
	if !strings.Contains(js.files[file], `"summary":"~a~\ud83d~\ude00~"`) || !strings.Contains(orch.files[file], "\"summary\":\"~a~\ufffd~\ufffd~\"") {
		t.Fatalf("PE-1 not observed:\n  js: %q\norch: %q", js.files[file], orch.files[file])
	}
	js.files[file] = strings.NewReplacer(`\ud83d`, "\ufffd", `\ude00`, "\ufffd").Replace(js.files[file])
	compareOutcomes(t, js, orch)
}

// shortIncomparableAt is the PE-5 regression case: three groups whose `at` values ("t0", true,
// "2") do not compare, so the comparator is inconsistent and the order is V8's. Node 26 (the
// reference) and orch give fp 1,0,2; Node 22 and 24 give 2,1,0.
var shortIncomparableAt = lines(
	`{"fp":"0","at":"t0","component":"c","kind":"note","summary":"s"}`,
	`{"fp":"1","at":true,"component":"c","kind":"note","summary":"s"}`,
	`{"fp":"2","at":"2","component":"c","kind":"note","summary":"s"}`,
)

// testIncidentShortIncomparableAt compares share on shortIncomparableAt byte for byte under the
// reference Node. Under an older Node only the bundle's summary order is allowed to differ
// (PE-5), so both summaries are put in fp order first; every other byte stays exact.
func (h *harness) testIncidentShortIncomparableAt(t *testing.T) {
	inv := h.incidentInvocation(incidentCase{args: []string{"share"}, seed: seedFile(incidentsFile, shortIncomparableAt), extraFiles: []string{"incidents-share.json"}})
	if h.nodeMajor < referenceNodeMajor {
		inv.normalizeWrites = func(o outcome, _ map[string]string) outcome {
			o.files["extra/incidents-share.json"] = summaryInFPOrder(t, o.files["extra/incidents-share.json"])
			return o
		}
	}
	h.comparePair(t, inv)
}

// summaryInFPOrder rewrites a share bundle with its summary array sorted by fp.
func summaryInFPOrder(t *testing.T, bundle string) string {
	t.Helper()
	doc, err := jsjson.Unmarshal([]byte(bundle))
	if err != nil {
		t.Fatalf("share bundle: %v", err)
	}
	summary, _ := jsjson.Path(doc, "summary").([]any)
	slices.SortStableFunc(summary, func(a, b any) int {
		return strings.Compare(jsjson.TemplateString(jsjson.Path(a, "fp")), jsjson.TemplateString(jsjson.Path(b, "fp")))
	})
	data, err := jsjson.MarshalIndent(doc, "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// testJSSortReference regenerates the jssort goldens with the installed Node and requires the
// committed testdata/jssort/orders.json under the reference Node or a later one, so a change to
// V8's sort fails loudly. An older Node sorts short arrays differently under inconsistent
// comparators by design (PE-5), so the comparison is skipped there.
func (h *harness) testJSSortReference(t *testing.T) {
	if h.nodeMajor < referenceNodeMajor {
		t.Skipf("jssort reference is V8 14.6 / Node %d (PE-5); Node %d differs on short inconsistent-comparator arrays by design", referenceNodeMajor, h.nodeMajor)
	}
	out, err := exec.Command(h.node, filepath.Join("..", "testdata", "jssort", "gen.mjs")).Output()
	if err != nil {
		t.Fatal(err)
	}
	type cases struct {
		Cases []map[string]any
	}
	var live, committed cases
	if err := json.Unmarshal(out, &live); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(readTestdata(t, "jssort/orders.json")), &committed); err != nil {
		t.Fatal(err)
	}
	if len(live.Cases) != len(committed.Cases) {
		t.Fatalf("Node wrote %d cases, orders.json has %d", len(live.Cases), len(committed.Cases))
	}
	differing := 0
	for i := range live.Cases {
		if !reflect.DeepEqual(live.Cases[i], committed.Cases[i]) {
			differing++
			if differing <= 5 {
				t.Errorf("case %d: Node %v, orders.json %v", i, live.Cases[i], committed.Cases[i])
			}
		}
	}
	if differing > 0 {
		t.Errorf("%d of %d cases differ from orders.json", differing, len(live.Cases))
	}
}

// testIncidentSplitSurrogate asserts PE-1: the 300-unit summary cut splits an emoji. stdout and
// the fingerprint are identical; the file holds JSON's "\ud83d" escape from the JS and U+FFFD
// from orch, and is otherwise identical.
func (h *harness) testIncidentSplitSurrogate(t *testing.T) {
	summary := strings.Repeat("a", 299) + "😀"
	js, orch := h.runPair(t, h.incidentInvocation(incidentCase{args: []string{"record", "--summary", summary}}))
	file := "state/incidents.jsonl"
	if !strings.Contains(js.files[file], `a\ud83d"`) || !strings.Contains(orch.files[file], "a�\"") {
		t.Fatalf("PE-1 not observed:\n  js: %q\norch: %q", js.files[file], orch.files[file])
	}
	js.files[file] = strings.Replace(js.files[file], `\ud83d`, "�", 1)
	compareOutcomes(t, js, orch)
}
