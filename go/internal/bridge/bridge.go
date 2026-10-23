// Package bridge runs a coding CLI on a bounded task and reports one structured result, as the
// JS bridges in orchestrator/bin do: RunClaude is `orch claude-exec` (bin/claude-exec.mjs),
// RunCodex is `orch codex-exec` (bin/codex-exec.mjs) and RunOpencode is `orch opencode-exec`
// (bin/opencode-exec.mjs).
//
// The JS reads its options as JS values: a value key given last on the command line, without
// its value, is the boolean true, not a string. Those values are carried here as any (a string
// or true) so that they print, serialize and coerce exactly as the JS ones do.
//
// This file holds what the three bridges share besides the child run loop (child.go): option
// reading, the progress logger, the budget refusal, the end-of-run report and the crash output.
package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/runrecord"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// Exit codes of the bridges.
const (
	exitOK        = 0
	exitFailed    = 1 // a failed run, or a crash
	exitPrompt    = 2 // no usable prompt
	exitBudgetRed = 75
)

// options are the parsed command-line options, read as the JS reads `opts` (see Opts.JS).
type options = cliargs.Opts

// or is `v || fallback`.
func or(v, fallback any) any {
	if jsjson.Truthy(v) {
		return v
	}
	return fallback
}

// text is String(v) for an option value or a value read from an event.
func text(v any) string { return jsjson.TemplateString(v) }

// resolvePath is path.resolve(v) for an option value: true is Node's ERR_INVALID_ARG_TYPE.
func resolvePath(v any) (string, error) {
	s, isString := v.(string)
	if !isString {
		return "", errPathArgBoolean
	}
	abs, err := nodefs.Resolve(s)
	if err != nil {
		return "", &jsError{class: "Error", message: err.Error()}
	}
	return abs, nil
}

// resolveProjectRoot keeps option type errors identical to resolvePath, while preventing
// bridge runs from treating .orchestrator itself as a project (PE-41).
func resolveProjectRoot(v any) (string, error) {
	abs, err := resolvePath(v)
	if err != nil {
		return "", err
	}
	return paths.ProjectRoot(abs)
}

// artifactRoot keeps orchestration output separate from the child working directory (PE-42).
// An explicit flag wins over the environment; the default remains the resolved child cwd.
func artifactRoot(opts options, cwd string) (string, error) {
	return resolveProjectRoot(opts.Or("artifact-root", or(os.Getenv("ORCH_ARTIFACT_ROOT"), cwd)))
}

// timeoutMinutes is `Number(opts["timeout-min"] || defaultMinutes)`.
func timeoutMinutes(timeoutMin any, defaultMinutes float64) float64 {
	if jsjson.Truthy(timeoutMin) {
		return jsjson.ToNumber(timeoutMin)
	}
	return defaultMinutes
}

// timeoutDelay is the delay Node gives setTimeout for
// `Number(opts["timeout-min"] || defaultMinutes) * 60000` milliseconds (procs.TimerDelay).
func timeoutDelay(timeoutMin any, defaultMinutes float64) time.Duration {
	return procs.TimerDelay(timeoutMinutes(timeoutMin, defaultMinutes) * 60000)
}

// refuse is the budget-red refusal every bridge makes the same way: an incident naming the
// script, then {ok:false, reason, ...gate}, then exit 75.
func refuse(stdout, stderr io.Writer, decision *budget.Decision, cwd, script string) int {
	context := jsjson.NewObject()
	context.Set("resets_at", or(decision.ResetsAt, nil))
	recordIncident(incident.Draft{Component: "bridge", Kind: "limit", Summary: cmdref.Command(script) + " refused: budget red", Context: context, Project: cwd})
	refusal := jsjson.NewObject()
	refusal.Set("ok", false)
	refusal.Set("reason", "budget_red")
	gate := decision.JSON()
	for _, key := range gate.Keys() {
		value, _ := gate.Get(key)
		refusal.Set(key, value)
	}
	if decision.Routing != nil {
		refusal.Set("routing", decision.Routing.JSON())
	}
	return printResult(stdout, stderr, refusal, exitBudgetRed)
}

// finishedRun is a run whose child has closed, reported as the three scripts end: the run
// record ({...result, prompt_sha256, prompt_head}), the post-run hook, the "done" progress
// line, an incident for a failed run, then the result on stdout.
type finishedRun struct {
	log          *logger
	cwd          string
	artifactRoot string
	prompt       string
	// afterRecord runs once the run record is written, where claude-exec and codex-exec start
	// their usage refresh, or is nil. A run record the JS throws on ends the run before it.
	afterRecord func()
	// doneLine is the "done" progress line given the result and its record file, or nil for a
	// bridge that logs none.
	doneLine func(result *jsjson.Object, record string) string
	// timedOut is set when the bridge's own cap of timeoutMinutes stopped the child; a failed
	// run is then filed as kind timeout (PE-45).
	timedOut       bool
	timeoutMinutes float64
	// model is the incident's context.model for a result without a model key (opencode,
	// PE-47), or nil.
	model any
}

// report writes the run record, runs afterRecord, writes the "done" line, an incident for a
// failed run and result with its record path appended, and returns the exit code.
func (r finishedRun) report(stdout, stderr io.Writer, result *jsjson.Object) int {
	hash := sha256.Sum256([]byte(r.prompt))
	record := jsjson.Spread(result)
	record.Set("prompt_sha256", hex.EncodeToString(hash[:]))
	record.Set("prompt_head", jsstr.Slice(r.prompt, 0, 400)) // U+FFFD for a split surrogate pair (PE-1)
	root := r.artifactRoot
	if root == "" {
		root = r.cwd
	}
	if root != r.cwd {
		record.Set("artifact_root", root)
	}
	file, err := runrecord.Write(root, record, time.Now())
	if err != nil {
		return crash(stderr, err)
	}
	result.Set("record", file)
	if r.afterRecord != nil {
		r.afterRecord()
	}
	if r.doneLine != nil {
		r.log.printf("%s", r.doneLine(result, file))
	}
	if !jsjson.Truthy(jsjson.Path(result, "ok")) {
		r.recordFailure(result, root)
		return printResult(stdout, stderr, result, exitFailed)
	}
	return printResult(stdout, stderr, result, exitOK)
}

// recordFailure is the incident of a failed run: kind timeout when the bridge's own cap stopped
// the child (PE-45), else limit when the errors mention a limit or a rate, else crash.
func (r finishedRun) recordFailure(result *jsjson.Object, cwd string) {
	errs := jsjson.Path(result, "errors").([]any)
	harness := text(jsjson.Path(result, "harness"))
	kind := "crash"
	summary := harness + " bridge failed: " + jsstr.Slice(jsjson.Join(errs, "; "), 0, 160) // PE-1
	switch {
	case r.timedOut:
		kind = "timeout"
		summary = harness + " bridge timed out after " + text(r.timeoutMinutes) + " min"
	case matchesIgnoringCase(limitOrRate, jsjson.Join(errs, " ")):
		kind = "limit"
	}
	context := jsjson.NewObject()
	context.Set("model", or(jsjson.Path(result, "model"), r.model))
	context.Set("exit_code", jsjson.Path(result, "exit_code"))
	recordIncident(incident.Draft{
		Component: "bridge",
		Kind:      kind,
		Summary:   summary,
		Detail:    jsjson.Join(errs, "\n"),
		Context:   context,
		Project:   cwd,
	})
}

// durationSeconds is `Math.round(result.duration_ms / 1000)` as the "done" line prints it.
func durationSeconds(result *jsjson.Object) string {
	durationMs, _ := jsjson.Path(result, "duration_ms").(float64)
	return text(jsjson.Round(durationMs / 1000))
}

// noPostRunRefreshEnv set to "1" turns the post-run usage refresh off. Only the parity harness
// sets it: a refresh outliving orch would write usage.json into a case's sandbox after its
// files are compared, and the JS refresh never writes it (PE-12).
const noPostRunRefreshEnv = "ORCH_NO_POST_RUN_REFRESH"

// startUsageRefresh is `getUsage({ refresh: true }).catch(() => {})` after a run (claude-exec
// l.87, codex-exec l.91), which the JS starts and then exits before it can complete (plan 3.10
// item 3). orch runs it as `orch usage --refresh` in a session of its own with its stdio on
// /dev/null, and does not wait for it, so the run's exit code and timing are unchanged. The
// refresh is bounded by the timeouts of the feeds it reads (security 30 s, the Claude endpoint
// 15 s, codex app-server 20 s, sqlite3 5 s, opencode auth list 15 s). As in the JS, a refresh
// that cannot start is ignored.
func startUsageRefresh() {
	if os.Getenv(noPostRunRefreshEnv) == "1" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	_ = procs.StartDetached(self, "usage", "--refresh")
}

// recordIncident appends an incident to the user's log, ignoring failures, as the JS's
// `try { incident(...) } catch { /* ignore */ }` does.
var recordIncident = incident.TryRecord

// usageConfig is the usage configuration for this process's environment.
func usageConfig() (usage.Config, error) {
	p, err := paths.Resolve(os.Getenv)
	if err != nil {
		return usage.Config{}, err
	}
	return usage.NewConfig(p), nil
}

// printResult writes value as one JSON line (console.log(JSON.stringify(value))) and returns
// code, or crashes if value cannot be serialized.
func printResult(stdout, stderr io.Writer, value *jsjson.Object, code int) int {
	data, err := jsjson.Marshal(value)
	if err != nil {
		return crash(stderr, err)
	}
	fmt.Fprintf(stdout, "%s\n", data)
	return code
}

// logger writes the "[<harness>] " progress lines on stderr, unless --quiet.
type logger struct {
	w      io.Writer
	prefix string
	quiet  bool
}

func (l *logger) printf(format string, args ...any) {
	if !l.quiet {
		fmt.Fprintf(l.w, l.prefix+format+"\n", args...)
	}
}

// lockedBuffer collects a child's stderr; exec copies into it on its own goroutine.
type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data
}

// stderrTail is `stderr.trim().split("\n").slice(-3).join(" | ")`: the last three lines of
// the child's stderr, trimmed of JS white space. The JS decodes stderr as a stream
// (setEncoding("utf8")), so decoding the whole buffer here gives the same text.
func stderrTail(stderr []byte) string {
	lines := strings.Split(strings.Trim(jsstr.DecodeUTF8(stderr), jsstr.WhiteSpace), "\n")
	return strings.Join(lines[max(len(lines)-3, 0):], " | ")
}

// limitOrRate is the incident kind test of every bridge, /limit|rate/i. A non-unicode JS /i
// matches ASCII letters only against ASCII letters, so the input is lowered in ASCII only
// (see asciiLower) and the bridges' /i patterns are written in lower case.
var limitOrRate = regexp.MustCompile(`limit|rate`)

// matchesIgnoringCase is re.test(s) for a /i regexp of lower-case ASCII literals.
func matchesIgnoringCase(re *regexp.Regexp, s string) bool {
	return re.MatchString(asciiLower(s))
}

// asciiLower lowers A-Z only. Under a non-unicode /i, JS canonicalizes by toUpperCase but
// never maps a non-ASCII character onto an ASCII one, so the Kelvin sign does not match "k"
// (Go's (?i) folding would match it).
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// jsError is an exception the JS leaves uncaught. Node prints "<class>: <message>" amid a
// source excerpt and a stack and exits 1; orch prints that line alone.
type jsError struct {
	class   string
	message string
}

func (e *jsError) Error() string { return e.class + ": " + e.message }

// errPathArgBoolean is path.resolve(true).
var errPathArgBoolean = &jsError{class: "TypeError [ERR_INVALID_ARG_TYPE]", message: cliargs.ErrPathArgBoolean.Error()}

// nullPropertyRead is the TypeError of reading key from null.
func nullPropertyRead(key string) *jsError {
	return &jsError{class: "TypeError", message: "Cannot read properties of null (reading '" + key + "')"}
}

// crash prints the error line of an exception the JS leaves uncaught and returns exit 1.
func crash(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, crashLine(err))
	return exitFailed
}

// crashLine is the "<class>: <message>" line Node prints for err: a TypeError for a run record
// field without the string method the JS calls, Node's message for a codex that cannot be
// started, otherwise an Error.
func crashLine(err error) string {
	var thrown *jsError
	var spawnErr *usage.SpawnError
	switch {
	case errors.As(err, &thrown):
		return thrown.Error()
	case errors.Is(err, runrecord.ErrNoStringMethod):
		return "TypeError: " + err.Error()
	case errors.As(err, &spawnErr) && errors.Is(err, exec.ErrNotFound):
		return "Error: spawn " + spawnErr.Bin + " ENOENT"
	}
	return "Error: " + err.Error()
}
