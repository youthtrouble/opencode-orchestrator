package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// machine is a scripted doctor environment: answers maps "name arg..." to an output; an
// unscripted command did not start (status null, no output), as a missing binary does.
type machine struct {
	answers  map[string]Output
	files    map[string]string
	exists   map[string]bool
	goOnPath bool
	routing  *usage.Routing
	routeErr error
	selfErr  error
	ran      []string
}

func code(n int) *int { return &n }

func ok(out string) Output { return Output{Code: code(0), Out: out} }

func (m *machine) deps() Deps {
	return Deps{
		Run: func(name string, args []string, dir string, timeout time.Duration) Output {
			key := strings.Join(append([]string{name}, args...), " ")
			m.ran = append(m.ran, key+" @"+dir+" "+timeout.String())
			return m.answers[key]
		},
		Home:     "/h",
		OrchHome: "/repo/orchestrator",
		Routing:  func() (*usage.Routing, error) { return m.routing, m.routeErr },
		ReadFile: func(path string) ([]byte, error) {
			if content, found := m.files[path]; found {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		},
		Exists:      func(path string) bool { return m.exists[path] },
		OnPath:      func(string) bool { return m.goOnPath },
		OrchVersion: "0.5.0 (abc1234)",
		Executable:  func() (string, error) { return "/bin/orch", nil },
		SelfTest:    func() error { return m.selfErr },
	}
}

const pluginLogPath = "/h/.local/share/opencode/log/opencode.log"

func healthy(t *testing.T) *machine {
	return &machine{
		answers: map[string]Output{
			"opencode --version":    ok("1.2.3\n"),
			"codex --version":       ok("codex-cli 0.9\nextra"),
			"claude --version":      ok("  2.0 (Claude Code)\n"),
			"node --version":        ok("v26.5.1\n"),
			"codex login status":    ok("Logged in using ChatGPT\n"),
			"claude auth status":    ok(`{"loggedIn": true}`),
			"opencode auth list":    ok("OpenAI oauth\nAnthropic api\n"),
			"opencode plugin list":  ok("orchestrator.ts\n"),
			"opencode debug agents": ok(`[{"id":"conductor"},{"id":"architect-oai"},{"id":"coder-oai"},{"id":"reviewer-oai"}]`),
			"go test ./...":         ok("ok  pkg\n"),
		},
		files:    map[string]string{pluginLogPath: "INFO fine\n"},
		exists:   map[string]bool{"/h/.config/opencode/opencode.jsonc": true, "/repo/go/go.mod": true},
		goOnPath: true,
		routing:  greenRouting(t),
	}
}

func greenRouting(t *testing.T) *usage.Routing {
	t.Helper()
	value, err := jsjson.Unmarshal(greenUsage)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := jsjson.Unmarshal([]byte(`{"vendors":{
		"anthropic":{"tiers":{"frontier":{"opencode":"anthropic/fable-5.1"}}},
		"openai":{"tiers":{"mini":{"opencode":"noslash"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return &usage.Routing{
		Usage:    value.(*jsjson.Object),
		Registry: registry.(*jsjson.Object),
		Resolved: routing.Resolved{Roles: []routing.Assignment{
			{Role: "planner", Entry: &routing.RoleEntry{Vendor: "anthropic", Tier: "frontier", Model: "anthropic/fable-5.1", Via: "claude-code"}},
			{Role: "coder", Entry: nil},
			{Role: "utility", Entry: &routing.RoleEntry{Vendor: "openai", Tier: "mini", Model: "noslash", Via: "opencode"}},
		}},
	}
}

func lines(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.String() + "\n")
	}
	return b.String()
}

func TestHealthyMachinePrintsEveryRowInTheJSOrder(t *testing.T) {
	m := healthy(t)
	rows := Check(m.deps())
	want := `OK   opencode present  —  1.2.3
OK   codex present  —  codex-cli 0.9
OK   claude present  —  2.0 (Claude Code)
OK   node present  —  v26.5.1
OK   orch binary  —  0.5.0 (abc1234) at /bin/orch
OK   codex login  —  Logged in using ChatGPT
OK   claude login
OK   opencode: openai connected
OK   opencode: anthropic connected (API key)
OK   Claude usage feed  —  session 12% fable 20% via claude-code-keychain
OK   Codex usage feed  —  5h 5% weekly 10% plan plus
OK   routing  —  planner=fable-5.1@claude-code coder=PAUSED utility=undefined@opencode
OK   opencode.jsonc present
OK   plugin registered
OK   agents loaded  —  conductor,architect-oai,coder-oai,reviewer-oai
OK   orch self-test  —  json, routing, executable
OK   unit tests  —  go test ./... passed
OK   no plugin errors in recent log
`
	if got := lines(rows); got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	if Failed(rows) {
		t.Error("a healthy machine failed")
	}
	if last := m.ran[len(m.ran)-1]; last != "go test ./... @/repo/go 2m0s" {
		t.Errorf("unit tests ran %q", last)
	}
	if first := m.ran[0]; first != "opencode --version @ 20s" {
		t.Errorf("sh ran %q", first)
	}
}

func TestNothingInstalledFailsEveryProbe(t *testing.T) {
	m := &machine{routeErr: errors.New("ENOENT: no such file or directory, open '/h/models.json'")}
	rows := Check(m.deps())
	want := `FAIL opencode missing  —  install it
FAIL codex missing  —  install it
FAIL claude missing  —  install it
FAIL node missing  —  install it
OK   orch binary  —  0.5.0 (abc1234) at /bin/orch
WARN codex not logged in  —  run: codex login
WARN claude auth status unclear
WARN opencode: OpenAI not connected  —  run: opencode auth login openai --method chatgpt-token-sharing
OK   opencode: anthropic not native (expected; Claude runs via claude-exec bridge)
FAIL usage/routing  —  ENOENT: no such file or directory, open '/h/models.json'
FAIL opencode.jsonc missing
FAIL plugin not listed  —  check ~/.config/opencode/plugins/orchestrator.ts and the log
FAIL opencode debug agents
OK   orch self-test  —  json, routing, executable
WARN unit tests  —  not run (no go toolchain or module at /repo/go)
`
	if got := lines(rows); got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	if !Failed(rows) {
		t.Error("no FAIL reported")
	}
}

func TestClaudeAuthForms(t *testing.T) {
	for out, loggedIn := range map[string]bool{
		`{"loggedIn": true}`:     true,
		"loggedIn: true":         true,
		"Logged In as x":         true,
		`{"loggedIn": false}`:    false,
		strings.Repeat("é", 200): false,
	} {
		m := healthy(t)
		m.answers["claude auth status"] = ok(out)
		row := Check(m.deps())[6]
		if (row.Name == "claude login") != loggedIn {
			t.Errorf("%q: row %q", out, row)
		}
		if !loggedIn && row.Detail != strings.TrimSpace(firstUnits(out, 120)) {
			t.Errorf("%q: detail %q", out, row.Detail)
		}
	}
}

// firstUnits cuts s to n UTF-16 units for the BMP-only inputs above.
func firstUnits(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func TestUsageRowsBeforeAThrowAreKept(t *testing.T) {
	m := healthy(t)
	codex, _ := m.routing.Usage.Get("codex")
	codex.(*jsjson.Object).Delete("weekly")
	rows := Check(m.deps())
	got := lines(rows[9:11])
	want := "OK   Claude usage feed  —  session 12% fable 20% via claude-code-keychain\n" +
		"FAIL usage/routing  —  Cannot read properties of undefined (reading 'pct')\n"
	if got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
}

// A role whose tier declares no string model makes the JS `v.model.split` throw.
func TestRoutingRowThrowsForAModelThatIsNotAString(t *testing.T) {
	for _, c := range []struct {
		name, tier, want string
	}{
		{"missing", `{}`, "Cannot read properties of undefined (reading 'split')"},
		{"null", `{"opencode":null}`, "Cannot read properties of null (reading 'split')"},
		{"number", `{"opencode":5}`, "v.model.split is not a function"},
		{"empty", `{"opencode":""}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := healthy(t)
			tier, err := jsjson.Unmarshal([]byte(c.tier))
			if err != nil {
				t.Fatal(err)
			}
			jsjson.Path(m.routing.Registry, "vendors", "openai", "tiers").(*jsjson.Object).Set("mini", tier)
			got := lines(Check(m.deps())[11:12])
			want := "FAIL usage/routing  —  " + c.want + "\n"
			if c.want == "" {
				want = "OK   routing  —  planner=fable-5.1@claude-code coder=PAUSED utility=undefined@opencode\n"
			}
			if got != want {
				t.Errorf("row %q, want %q", got, want)
			}
		})
	}
}

func TestFeedErrorsAndMissingFable(t *testing.T) {
	m := healthy(t)
	claude, _ := m.routing.Usage.Get("claude")
	claude.(*jsjson.Object).Set("weekly_by_model", jsjson.NewObject())
	codex, _ := m.routing.Usage.Get("codex")
	codex.(*jsjson.Object).Set("ok", false)
	codex.(*jsjson.Object).Set("error", "codex app-server exited")
	rows := Check(m.deps())
	got := lines(rows[9:11])
	want := "OK   Claude usage feed  —  session 12% fable n/a% via claude-code-keychain\n" +
		"FAIL Codex usage feed  —  codex app-server exited\n"
	if got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	claude.(*jsjson.Object).Set("ok", false)
	if row := Check(m.deps())[9]; row.String() != "FAIL Claude usage feed" {
		t.Errorf("feed without error: %q", row)
	}
}

func TestAgentsAnyJSONValue(t *testing.T) {
	cases := map[string]string{
		`[{"id":"conductor"},{"id":"coder-oai"},7,{"id":["a","b"]},{}]`: "FAIL agents missing  —  conductor,coder-oai,,a,b,",
		`[{"id":"conductor"},null]`:                                     "FAIL opencode debug agents  —  [{\"id\":\"conductor\"},null]",
		`{"map": 1}`:                                                    "FAIL opencode debug agents  —  {\"map\": 1}",
		"not json " + strings.Repeat("x", 300):                          "FAIL opencode debug agents  —  not json " + strings.Repeat("x", 191),
	}
	for out, want := range cases {
		m := healthy(t)
		m.answers["opencode debug agents"] = ok(out)
		if row := Check(m.deps())[14]; row.String() != want {
			t.Errorf("%q: row %q, want %q", out, row, want)
		}
	}
}

func TestUnitTestOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		answer Output
		onPath bool
		want   string
	}{
		{"pass", ok("ok\n"), true, "OK   unit tests  —  go test ./... passed"},
		{"fail", Output{Code: code(1), Out: "ok a\n--- FAIL: TestX (0.00s)\nFAIL\tpkg\n"}, true, "FAIL unit tests  —  --- FAIL: TestX (0.00s)"},
		{"fail without a FAIL line", Output{Code: code(2), Out: "build failed\n"}, true, "FAIL unit tests  —  exit 2"},
		{"timeout", Output{TimedOut: true}, true, "WARN unit tests  —  go test timed out after 120 s"},
		{"no go", ok(""), false, "WARN unit tests  —  not run (no go toolchain or module at /repo/go)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := healthy(t)
			m.answers["go test ./..."] = c.answer
			m.goOnPath = c.onPath
			if row := Check(m.deps())[16]; row.String() != c.want {
				t.Errorf("row %q, want %q", row, c.want)
			}
		})
	}
}

func TestSelfTestFailureIsAFail(t *testing.T) {
	m := healthy(t)
	m.selfErr = errors.New("json: re-serialised x")
	rows := Check(m.deps())
	if row := rows[15]; row.String() != "FAIL orch self-test  —  json: re-serialised x" || !Failed(rows) {
		t.Errorf("row %q", row)
	}
}

func TestPluginLogScan(t *testing.T) {
	var log []string
	for i := 0; i < 500; i++ {
		log = append(log, "INFO line")
	}
	long := "ERROR orchestrator " + strings.Repeat("a", 100)            // 119 units
	straddling := "ERROR orchestrator " + strings.Repeat("a", 36) + "😀" // 😀 at units 299-300 of the join
	log[50] = "ERROR orchestrator too old"                              // outside the last 400 lines
	log[200] = "ERROR orchestrator one"
	log[300] = long
	log[400] = long
	log[499] = straddling
	m := healthy(t)
	m.files[pluginLogPath] = strings.Join(log, "\n")
	row := Check(m.deps())[17]
	want := long + " | " + long + " | " + strings.TrimSuffix(straddling, "😀") + "\ufffd" // the cut keeps a lone high surrogate
	if row.Status != Warn || row.Name != "plugin errors in log" || row.Detail != want {
		t.Errorf("row %q, want detail %q", row, want)
	}
}

func TestUnreadablePluginLogAddsNoRow(t *testing.T) {
	m := healthy(t)
	delete(m.files, pluginLogPath)
	rows := Check(m.deps())
	if last := rows[len(rows)-1]; last.Name != "unit tests" {
		t.Errorf("last row %q", last)
	}
}

func TestSelfTestPassesOnTheRepoRegistry(t *testing.T) {
	registry := filepath.Join("..", "..", "..", "orchestrator", "models.json")
	if err := SelfTest(registry, os.Executable)(); err != nil {
		t.Fatal(err)
	}
	if err := SelfTest(filepath.Join(t.TempDir(), "missing.json"), os.Executable)(); err == nil || !strings.HasPrefix(err.Error(), "routing: ") {
		t.Errorf("missing registry: %v", err)
	}
	broken := func() (string, error) { return "", errors.New("no path") }
	if err := SelfTest(registry, broken)(); err == nil || err.Error() != "executable: no path" {
		t.Errorf("broken executable: %v", err)
	}
}
