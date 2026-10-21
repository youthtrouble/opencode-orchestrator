// Package doctor is `orch doctor`: the health check of bin/doctor.mjs (binaries, auth, usage
// feeds, opencode config, plugin, tests), row for row with the JS texts, plus the orch rows of
// PE-18. Every process, file and routing read goes through Deps so each branch is testable
// without running anything.
package doctor

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// Status is a row's first column, padded to four characters as the JS prints it.
type Status string

const (
	OK   Status = "OK  "
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// Row is one printed line: `${status} ${name}` plus `  —  ${detail}` when the JS detail is
// truthy (HasDetail).
type Row struct {
	Status    Status
	Name      string
	Detail    string
	HasDetail bool
}

// String is the row as doctor.mjs prints it.
func (r Row) String() string {
	if !r.HasDetail {
		return string(r.Status) + " " + r.Name
	}
	return string(r.Status) + " " + r.Name + "  —  " + r.Detail
}

// Failed reports whether any row is a FAIL (doctor then exits 1).
func Failed(rows []Row) bool {
	for _, r := range rows {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

// Output is the JS `sh` result: the exit status (nil for a signal or a start failure) and
// stdout followed by stderr. TimedOut is spawnSync's ETIMEDOUT.
type Output struct {
	Code     *int
	Out      string
	TimedOut bool
}

// Runner runs name with args in dir ("" for the current directory) under timeout.
type Runner func(name string, args []string, dir string, timeout time.Duration) Output

// Deps are doctor's view of the machine.
type Deps struct {
	Run         Runner
	Home        string // os.homedir()
	OrchHome    string // the JS ROOT; its ../go holds the Go module the unit tests run
	Routing     func() (*usage.Routing, error)
	ReadFile    func(string) ([]byte, error)
	Exists      func(string) bool
	OnPath      func(name string) bool
	OrchVersion string
	Executable  func() (string, error)
	SelfTest    func() error // PE-18's in-process checks
}

// shTimeout is the JS sh's spawnSync timeout; goTestTimeout bounds PE-18's `go test ./...`.
const (
	shTimeout     = 20 * time.Second
	goTestTimeout = 120 * time.Second
)

// Check runs every check in the JS order and returns the rows.
func Check(d Deps) []Row {
	c := &checks{deps: d}
	c.binaries()
	c.logins()
	c.usageAndRouting()
	c.opencodeConfig()
	c.agents()
	c.selfTest()
	c.unitTests()
	c.pluginLog()
	return c.rows
}

type checks struct {
	deps Deps
	rows []Row
}

func (c *checks) push(s Status, name string) { c.rows = append(c.rows, Row{Status: s, Name: name}) }

// pushDetail adds a row whose detail is a JS value: shown only when truthy, as String(value).
func (c *checks) pushDetail(s Status, name string, detail any) {
	c.rows = append(c.rows, Row{Status: s, Name: name, Detail: jsjson.TemplateString(detail), HasDetail: jsjson.Truthy(detail)})
}

func (c *checks) sh(name string, args ...string) Output {
	return c.deps.Run(name, args, "", shTimeout)
}

func (c *checks) binaries() {
	for _, bin := range []string{"opencode", "codex", "claude", "node"} {
		r := c.sh(bin, "--version")
		if exitedZero(r) {
			c.pushDetail(OK, bin+" present", strings.SplitN(jsTrim(r.Out), "\n", 2)[0])
		} else {
			c.pushDetail(Fail, bin+" missing", "install it")
		}
		if bin == "node" {
			c.orchBinary()
		}
	}
}

// orchBinary is PE-18's row after `node present`.
func (c *checks) orchBinary() {
	self, err := c.deps.Executable()
	if err != nil {
		c.pushDetail(Fail, "orch binary", err.Error())
		return
	}
	c.pushDetail(OK, "orch binary", c.deps.OrchVersion+" at "+self)
}

var (
	codexLoggedIn  = regexp.MustCompile(`Logged in`)
	claudeLoggedIn = regexp.MustCompile(`(?i)loggedIn"?:[` + jsstr.WhiteSpaceClass + `]*true|logged in`)
	mentionsOpenAI = regexp.MustCompile(`(?i)openai`)
	mentionsClaude = regexp.MustCompile(`(?i)anthropic`)
)

func (c *checks) logins() {
	codex := c.sh("codex", "login", "status")
	if codexLoggedIn.MatchString(codex.Out) {
		c.pushDetail(OK, "codex login", jsTrim(codex.Out))
	} else {
		c.pushDetail(Warn, "codex not logged in", "run: codex login")
	}
	claude := c.sh("claude", "auth", "status")
	if claudeLoggedIn.MatchString(claude.Out) {
		c.push(OK, "claude login")
	} else {
		c.pushDetail(Warn, "claude auth status unclear", jsstr.Slice(jsTrim(claude.Out), 0, 120))
	}
	auth := c.sh("opencode", "auth", "list")
	if mentionsOpenAI.MatchString(auth.Out) {
		c.push(OK, "opencode: openai connected")
	} else {
		c.pushDetail(Warn, "opencode: OpenAI not connected", "run: opencode auth login openai --method chatgpt-token-sharing")
	}
	if mentionsClaude.MatchString(auth.Out) {
		c.push(OK, "opencode: anthropic connected (API key)")
	} else {
		c.push(OK, "opencode: anthropic not native (expected; Claude runs via claude-exec bridge)")
	}
}

// usageAndRouting is the JS try block: rows pushed before a throw are kept and the throw adds
// `FAIL usage/routing`.
func (c *checks) usageAndRouting() {
	if err := c.feedsAndRouting(); err != nil {
		c.pushDetail(Fail, "usage/routing", err.Error())
	}
}

func (c *checks) feedsAndRouting() error {
	r, err := c.deps.Routing()
	if err != nil {
		return err
	}
	claude := jsjson.Path(r.Usage, "claude")
	if jsjson.Truthy(jsjson.Path(claude, "ok")) {
		session, err := readPct(claude, "session")
		if err != nil {
			return err
		}
		fable := jsjson.Path(claude, "weekly_by_model", "fable", "pct")
		if fable == nil {
			fable = "n/a"
		}
		c.pushDetail(OK, "Claude usage feed", "session "+session+"% fable "+jsjson.TemplateString(fable)+"% via "+field(claude, "source"))
	} else {
		c.pushDetail(Fail, "Claude usage feed", jsjson.Path(claude, "error"))
	}
	codex := jsjson.Path(r.Usage, "codex")
	if jsjson.Truthy(jsjson.Path(codex, "ok")) {
		fiveHour, err := readPct(codex, "five_hour")
		if err != nil {
			return err
		}
		weekly, err := readPct(codex, "weekly")
		if err != nil {
			return err
		}
		c.pushDetail(OK, "Codex usage feed", "5h "+fiveHour+"% weekly "+weekly+"% plan "+field(codex, "plan"))
	} else {
		c.pushDetail(Fail, "Codex usage feed", jsjson.Path(codex, "error"))
	}
	if credits := routing.OpenAICredits(codex); credits != nil {
		c.pushDetail(OK, "OpenAI credits", credits.Text()+"; policy "+r.CreditsPolicy.Text())
	}
	summary, err := routingSummary(r)
	if err != nil {
		return err
	}
	c.pushDetail(OK, "routing", summary)
	return nil
}

// readPct is `${feed[window].pct}`: a missing or null window throws the JS TypeError.
func readPct(feed any, window string) (string, error) {
	w, present := jsjson.Lookup(feed, window)
	if w == nil {
		if present {
			return "", jsjson.CannotReadOfNull("pct")
		}
		return "", &jsjson.TypeError{Message: "Cannot read properties of undefined (reading 'pct')"}
	}
	return field(w, "pct"), nil
}

// field is `${v[key]}` on an object value.
func field(v any, key string) string {
	value, present := jsjson.Lookup(v, key)
	return jsjson.TemplateStringOf(value, present)
}

// routingSummary is `role=<model after "/">@<via>` or `role=PAUSED`, joined by one space.
func routingSummary(r *usage.Routing) (string, error) {
	parts := make([]string, 0, len(r.Resolved.Roles))
	for _, a := range r.Resolved.Roles {
		if a.Entry == nil {
			parts = append(parts, a.Role+"=PAUSED")
			continue
		}
		model, err := routing.ModelName(r.Registry, a.Entry)
		if err != nil {
			return "", err
		}
		parts = append(parts, a.Role+"="+model+"@"+a.Entry.Via)
	}
	return strings.Join(parts, " "), nil
}

var mentionsOrchestrator = regexp.MustCompile(`orchestrator`)

func (c *checks) opencodeConfig() {
	if c.deps.Exists(filepath.Join(c.deps.Home, ".config", "opencode", "opencode.jsonc")) {
		c.push(OK, "opencode.jsonc present")
	} else {
		c.push(Fail, "opencode.jsonc missing")
	}
	plugins := c.sh("opencode", "plugin", "list")
	if mentionsOrchestrator.MatchString(plugins.Out) {
		c.push(OK, "plugin registered")
	} else {
		c.pushDetail(Fail, "plugin not listed", "check ~/.config/opencode/plugins/orchestrator.ts and the log")
	}
}

// requiredAgents must all be among `opencode debug agents`' ids.
var requiredAgents = []string{"conductor", "architect-oai", "coder-oai", "reviewer-oai"}

func (c *checks) agents() {
	out := c.sh("opencode", "debug", "agents").Out
	ids, ok := agentIDs(out)
	if !ok {
		c.pushDetail(Fail, "opencode debug agents", jsstr.Slice(out, 0, 200))
		return
	}
	if includesAll(ids, requiredAgents) {
		c.pushDetail(OK, "agents loaded", jsjson.Join(ids, ","))
	} else {
		c.pushDetail(Fail, "agents missing", jsjson.Join(ids, ","))
	}
}

// agentIDs is `JSON.parse(out).map((a) => a.id)`; false where the JS throws (not JSON, not an
// array, a null element).
func agentIDs(out string) ([]any, bool) {
	parsed, err := jsjson.Unmarshal([]byte(out))
	if err != nil {
		return nil, false
	}
	agents, isArray := parsed.([]any)
	if !isArray {
		return nil, false
	}
	ids := make([]any, len(agents))
	for i, a := range agents {
		if a == nil {
			return nil, false
		}
		ids[i] = jsjson.Path(a, "id")
	}
	return ids, true
}

// includesAll is `want.every((id) => ids.includes(id))` for string ids.
func includesAll(ids []any, want []string) bool {
	for _, w := range want {
		found := false
		for _, id := range ids {
			if s, isString := id.(string); isString && s == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// selfTest is PE-18's row before `unit tests`.
func (c *checks) selfTest() {
	if err := c.deps.SelfTest(); err != nil {
		c.pushDetail(Fail, "orch self-test", err.Error())
		return
	}
	c.pushDetail(OK, "orch self-test", "json, routing, executable")
}

var firstFailLine = regexp.MustCompile(`(?m)^(?:--- )?FAIL.*$`)

// unitTests is PE-18's `unit tests`: the Go module's tests instead of `node --test`.
func (c *checks) unitTests() {
	module := filepath.Join(filepath.Dir(c.deps.OrchHome), "go")
	if !c.deps.OnPath("go") || !c.deps.Exists(filepath.Join(module, "go.mod")) {
		c.pushDetail(Warn, "unit tests", "not run (no go toolchain or module at "+module+")")
		return
	}
	r := c.deps.Run("go", []string{"test", "./..."}, module, goTestTimeout)
	switch {
	case r.TimedOut:
		c.pushDetail(Warn, "unit tests", "go test timed out after 120 s")
	case exitedZero(r):
		c.pushDetail(OK, "unit tests", "go test ./... passed")
	case firstFailLine.MatchString(r.Out):
		c.pushDetail(Fail, "unit tests", firstFailLine.FindString(r.Out))
	case r.Code != nil:
		c.pushDetail(Fail, "unit tests", "exit "+jsjson.TemplateString(float64(*r.Code)))
	default:
		c.pushDetail(Fail, "unit tests", "exit null")
	}
}

var (
	mentionsError = regexp.MustCompile(`ERROR`)
	pluginLogTail = 400
)

// pluginLog scans the last 400 lines of opencode's log and shows the last three plugin errors,
// redacted; an unreadable log adds no row.
func (c *checks) pluginLog() {
	data, err := c.deps.ReadFile(filepath.Join(c.deps.Home, ".local", "share", "opencode", "log", "opencode.log"))
	if err != nil {
		return
	}
	lines := strings.Split(jsstr.DecodeUTF8(data), "\n")
	if len(lines) > pluginLogTail {
		lines = lines[len(lines)-pluginLogTail:]
	}
	var errors []string
	for _, l := range lines {
		if mentionsOrchestrator.MatchString(l) && mentionsError.MatchString(l) {
			errors = append(errors, l)
		}
	}
	if len(errors) == 0 {
		c.push(OK, "no plugin errors in recent log")
		return
	}
	if len(errors) > 3 {
		errors = errors[len(errors)-3:]
	}
	// A log line may quote a path, an email or a credential; the row is redacted as an incident
	// is, with the home Node's os.homedir() gives, before it is cut.
	c.pushDetail(Warn, "plugin errors in log", jsstr.Slice(incident.Redact(c.deps.Home, strings.Join(errors, " | ")), 0, 300))
}

func exitedZero(r Output) bool { return r.Code != nil && *r.Code == 0 }

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.Trim(s, jsstr.WhiteSpace) }
