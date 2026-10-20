package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// incidentSandbox points HOME at a temp directory and works in another, so no test reads or
// writes the real incident log. It returns the log path.
func incidentSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	return filepath.Join(home, ".local", "state", "opencode", "orchestrator", "incidents.jsonl")
}

func TestIncidentRecordListReportShare(t *testing.T) {
	log := incidentSandbox(t)
	code, stdout, stderr := run(t, Incident, []string{"record", "--component", "bridge", "--kind", "crash", "--summary", "exit 2", "--cwd", "/w/repo", "--ctx", "k=v"}, "")
	receipt := regexp.MustCompile(`^\{"recorded":"[0-9a-f]{12}","at":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z"\}\n$`)
	if code != 0 || !receipt.MatchString(stdout) || stderr != "" {
		t.Fatalf("record: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), `"context":{"k":"v"},"project":"repo","version":`) {
		t.Fatalf("log %q, %v", data, err)
	}
	if code, stdout, _ := run(t, Incident, []string{"list"}, ""); code != 0 || !strings.HasSuffix(stdout, " bridge/crash repo: exit 2\n") {
		t.Errorf("list: code %d stdout %q", code, stdout)
	}
	if code, stdout, _ := run(t, Incident, []string{"report"}, ""); code != 0 || !strings.HasPrefix(stdout, "  1x ") || !strings.Contains(stdout, " bridge/crash [repo] exit 2  (first ") {
		t.Errorf("report: code %d stdout %q", code, stdout)
	}
	code, stdout, _ = run(t, Incident, []string{"share"}, "")
	if code != 0 || stdout != "wrote incidents-share.json (1 incidents). Review it, then attach to a GitHub issue by hand. Nothing was uploaded.\n" {
		t.Errorf("share: code %d stdout %q", code, stdout)
	}
	bundle, err := os.ReadFile("incidents-share.json")
	if err != nil || strings.Contains(string(bundle), `"project":`) || strings.HasSuffix(string(bundle), "\n") {
		t.Errorf("bundle %q, %v: want no project key and no trailing newline", bundle, err)
	}
}

func TestIncidentUsage(t *testing.T) {
	log := incidentSandbox(t)
	for _, args := range [][]string{nil, {"frob"}} {
		if code, _, stderr := run(t, Incident, args, ""); code != 2 || stderr != "commands: record | list | report | share   (log: "+log+")\n" {
			t.Errorf("%q: code %d stderr %q", args, code, stderr)
		}
	}
}

// The error lines Node prints for the uncaught exceptions (C1), captured from Node v26.5.1.
func TestIncidentCrashLines(t *testing.T) {
	log := incidentSandbox(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"record"}, "Error: incident needs component and summary"},
		{[]string{"record", "--summary", ""}, "Error: incident needs component and summary"},
		{[]string{"record", "--summary", "s", "--cwd"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "path" argument must be of type string. Received type boolean (true)`},
		{[]string{"record", "--summary", "s", "--ctx"}, "TypeError: opts.ctx.map is not a function"},
		{[]string{"record", "--ctx", "--ctx", "a=b", "--summary", "s"}, "TypeError: opts.ctx.push is not a function"},
		{[]string{"frob", "--ctx", "--ctx", "a=b"}, "TypeError: opts.ctx.push is not a function"},
		{[]string{"share", "--out"}, `TypeError [ERR_INVALID_ARG_TYPE]: The "path" argument must be of type string or an instance of Buffer or URL. Received type boolean (true)`},
		{[]string{"share", "--out", "missing/x.json"}, "Error: ENOENT: no such file or directory, open 'missing/x.json'"},
	}
	for _, c := range cases {
		if code, stdout, stderr := run(t, Incident, c.args, ""); code != 1 || stdout != "" || stderr != c.want+"\n" {
			t.Errorf("%q: code %d stdout %q stderr %q, want %q", c.args, code, stdout, stderr, c.want)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("a failed record wrote the log (stat error %v)", err)
	}
}

// The expectation is Node's output for the same pairs: the empty pair resets key "" to
// undefined, which JSON omits.
func TestContextOfMirrorsObjectFromEntriesOfSplit(t *testing.T) {
	got := contextOf([]string{"b=1", "a=b=c", "b", "=x", "2=two", "b=late", "solo", ""})
	data, err := jsjson.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"2":"two","b":"late","a":"b"}`; string(data) != want {
		t.Errorf("context = %s, want %s", data, want)
	}
}

// TestIncidentWithEmptyHomeUsesTheWorkingDirectory: Node keeps an explicitly empty HOME, so
// the log is relative to the working directory; orch must never fall back to the user's own
// log (which a caller emptying HOME is trying to avoid).
func TestIncidentWithEmptyHomeUsesTheWorkingDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)
	const relative = ".local/state/opencode/orchestrator/incidents.jsonl"
	if code, _, stderr := run(t, Incident, []string{"frob"}, ""); code != 2 || stderr != "commands: record | list | report | share   (log: "+relative+")\n" {
		t.Fatalf("usage: code %d stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, Incident, []string{"record", "--summary", "ab", "--cwd", "/w/p"}, ""); code != 0 || stderr != "" {
		t.Fatalf("record: code %d stderr %q", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, relative))
	if err != nil || !strings.Contains(string(data), `"summary":"~a~b~","detail":"~"`) {
		t.Errorf("log %q, %v: want it under the working directory, redacted as Node does for an empty home", data, err)
	}
}

// PE-25: record --request - takes the incident from stdin and writes the entry the flags write.
func TestIncidentRecordRequestMatchesTheFlags(t *testing.T) {
	log := incidentSandbox(t)
	apiKey := "sk-" + "ant-" + strings.Repeat("x", 24)
	request := `{"component":"plugin","kind":"crash","summary":"exit 2 ` + apiKey + `","detail":"d","context":{"k":"v"},"project":"/w/repo"}`
	if code, stdout, stderr := run(t, Incident, []string{"record", "--request", "-"}, request); code != 0 || !strings.HasPrefix(stdout, `{"recorded":"`) || stderr != "" {
		t.Fatalf("request: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := run(t, Incident, []string{"record", "--component", "plugin", "--kind", "crash", "--summary", "exit 2 " + apiKey, "--detail", "d", "--ctx", "k=v", "--cwd", "/w/repo"}, ""); code != 0 || stderr != "" {
		t.Fatalf("flags: code %d stderr %q", code, stderr)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	withoutAt := regexp.MustCompile(`(?m)^\{"at":"[^"]*",`).ReplaceAllString(string(data), "")
	entries := strings.Split(strings.TrimSuffix(withoutAt, "\n"), "\n")
	if len(entries) != 2 || entries[0] != entries[1] || strings.Contains(string(data), apiKey) {
		t.Errorf("want two equal redacted entries:\n%s", data)
	}
}

func TestIncidentRecordRequestDefaultsAndContextValues(t *testing.T) {
	log := incidentSandbox(t)
	request := `{"summary":"s","kind":null,"component":"","context":{"n":3,"b":true,"z":null,"p":"/Users/bob/x"}}`
	if code, _, stderr := run(t, Incident, []string{"record", "--request", "-"}, request); code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), `"component":"agent","kind":"note","summary":"s","detail":"","context":{"n":3,"b":true,"z":null,"p":"~/x"},"project":"`) {
		t.Errorf("log %q, %v", data, err)
	}
}

func TestIncidentRecordRequestRefusals(t *testing.T) {
	log := incidentSandbox(t)
	usage := func(message string) string { return "orch incident record: " + message + "\n" }
	cases := []struct {
		args   []string
		stdin  string
		code   int
		stderr string
	}{
		{[]string{"record", "--request", "-"}, `{"component":"c"}`, 1, "Error: incident needs component and summary\n"},
		{[]string{"record", "--request", "-"}, `{"summary":`, 2, ""},
		{[]string{"record", "--request", "-"}, `["s"]`, 2, usage("--request: stdin must be a JSON object")},
		{[]string{"record", "--request", "-"}, `{"summary":"s","ctx":{}}`, 2, usage(`--request: unknown key "ctx"`)},
		{[]string{"record", "--request", "-"}, `{"summary":5}`, 2, usage("--request: summary must be a string")},
		{[]string{"record", "--request", "-"}, `{"summary":"s","context":{"a":{}}}`, 2, usage("--request: context.a must be a string, number, boolean or null")},
		{[]string{"record", "--request", "-"}, `{"summary":"s","context":[]}`, 2, usage("--request: context must be an object")},
		{[]string{"record", "--request", "r.json"}, `{"summary":"s"}`, 2, usage("--request takes - (the incident as one JSON object on stdin)")},
		{[]string{"record", "--request"}, `{"summary":"s"}`, 2, usage("--request takes - (the incident as one JSON object on stdin)")},
		{[]string{"record", "--request", "-", "--summary", "s"}, `{"summary":"s"}`, 2, usage("--request cannot be combined with --summary")},
		{[]string{"record", "--ctx", "a=b", "--request", "-"}, `{"summary":"s"}`, 2, usage("--request cannot be combined with --ctx")},
		{[]string{"record", "--request", "-", "--cwd"}, `{"summary":"s"}`, 2, usage("--request cannot be combined with --cwd")},
	}
	for _, c := range cases {
		code, stdout, stderr := run(t, Incident, c.args, c.stdin)
		if code != c.code || stdout != "" || (c.stderr != "" && stderr != c.stderr) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%q < %s: code %d stdout %q stderr %q, want %d %q", c.args, c.stdin, code, stdout, stderr, c.code, c.stderr)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("a refused request wrote the log (stat error %v)", err)
	}
}
