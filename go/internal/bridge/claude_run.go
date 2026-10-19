package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/runrecord"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// Exit codes of claude-exec.
const (
	exitOK        = 0
	exitFailed    = 1 // a failed run, or a crash
	exitPrompt    = 2 // no usable prompt
	exitBudgetRed = 75
)

// claudeGrace bounds the wait before SIGKILL after orch signals claude, and the wait for
// claude's pipes to close after it exits (PE-10).
const claudeGrace = 10 * time.Second

// maxTimerMs is Node's TIMEOUT_MAX: setTimeout runs a delay above it, below 1 or NaN after 1 ms.
const maxTimerMs = math.MaxInt32

// RunClaude is `orch claude-exec [args]`: it runs `claude -p` on the prompt and prints one JSON
// result, exactly as bin/claude-exec.mjs does, step for step, and returns the exit code: 0 ok,
// 1 failed or crashed, 2 no prompt, 75 budget red (refused, or the run hit a usage limit).
// The differences are the documented exceptions PE-8 to PE-12 and the crash output (only the
// error line of an exception the JS leaves uncaught).
func RunClaude(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	parsed, positional := cliargs.ParseArgs(args, claudeValueKeys, claudeFlagKeys)
	opts := claudeOptions(parsed)
	if parsed.IsFlag("help") {
		fmt.Fprintln(stdout, cmdref.ClaudeExecHelp)
		return exitOK
	}
	prompt, err := cliargs.ReadPrompt(parsed, positional, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return exitPrompt
	}
	cwd, err := resolvePath(opts.or("cwd", "."))
	if err != nil {
		return crash(stderr, err)
	}
	model := opts.or("model", defaultClaudeModel)
	timeout := timeoutDelay(opts.value("timeout-min"))
	log := &logger{w: stderr, quiet: parsed.IsFlag("quiet")}

	cfg, err := usageConfig()
	if err != nil {
		return crash(stderr, err)
	}
	decision, err := budget.Gate(cfg, routing.Anthropic, parsed.IsFlag("force"), matchesIgnoringCase(fable, text(model)))
	if err != nil {
		return crash(stderr, err)
	}
	if !decision.Allowed {
		return refuse(stdout, stderr, &decision, cwd)
	}

	argv, err := claudeArgv(opts, model, prompt)
	if err != nil {
		return crash(stderr, err)
	}
	started := time.Now()
	log.printf("%s", startLine(opts, model, cwd))
	result := newClaudeResult(opts, model, cwd, jsjson.ISOString(started))
	run := &claudeRun{events: newClaudeEvents(log, result), cwd: cwd, prompt: prompt, started: started}
	if err := run.execute(argv, timeout); err != nil {
		return crash(stderr, err)
	}
	if run.hitUsageLimit() {
		return run.finishOnUsageLimit(cfg, stdout, stderr)
	}
	return run.finish(stdout, stderr)
}

// timeoutDelay is the delay Node gives setTimeout for `Number(opts["timeout-min"] || 60) * 60000`
// milliseconds: 1 ms when that is NaN, below 1 or above maxTimerMs. Node also prints a
// warning for the clamp; orch does not (PE-9).
func timeoutDelay(timeoutMin any) time.Duration {
	minutes := 60.0
	if jsjson.Truthy(timeoutMin) {
		minutes = jsjson.ToNumber(timeoutMin)
	}
	ms := minutes * 60000
	if !(ms >= 1 && ms <= maxTimerMs) {
		ms = 1
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// refuse is the budget-red refusal (l.36): an incident, then {ok:false, reason, ...gate}, then
// exit 75.
func refuse(stdout, stderr io.Writer, decision *budget.Decision, cwd string) int {
	context := jsjson.NewObject()
	context.Set("resets_at", or(decision.ResetsAt, nil))
	recordIncident(incident.Draft{Component: "bridge", Kind: "limit", Summary: cmdref.ScriptFile("claude-exec") + " refused: budget red", Context: context, Project: cwd})
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

// claudeRun is one claude child and what is known about it.
type claudeRun struct {
	events  *claudeEvents
	cwd     string
	prompt  string
	started time.Time
}

// execute runs claude to completion (l.53-78): it forwards SIGTERM and SIGINT to claude
// (PE-11), sends SIGTERM once the timeout passes, and on exit appends exit_code and
// duration_ms and the failure, if any. An error is a crash: a spawn failure, or a TypeError
// the JS would have died of in its line handler (claude is then sent SIGTERM).
func (r *claudeRun) execute(argv []string, timeout time.Duration) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	lines := &procs.Lines{OnLine: r.events.handleLine}
	stderr := &lockedBuffer{}
	child, err := procs.Start(procs.Spec{
		Name:   "claude",
		Args:   argv,
		Dir:    r.cwd,
		Env:    procs.WithoutEnv(os.Environ(), "CLAUDECODE"),
		Stdout: lines,
		Stderr: stderr,
		Grace:  claudeGrace,
	})
	if err != nil {
		return &jsError{class: "Error", message: err.Error()}
	}
	closed := make(chan struct{})
	go forwardSignals(child, signals, r.events.crashed, closed)
	timer := time.AfterFunc(timeout, func() {
		if r.events.logIfRunning("timeout; killing") {
			child.Signal(syscall.SIGTERM)
		}
	})
	code, waitErr := child.Wait()
	// claude has exited: no timeout may fire from here on, so none can log after the last line
	// (the JS clears the timer on close). One that fired before Wait returned may still be
	// logging; in the JS it would have fired before close too.
	timer.Stop()
	lines.Close()
	close(closed)
	return r.events.close(code, waitErr, stderr.Bytes(), time.Since(r.started))
}

// forwardSignals relays the signals orch receives to claude, and stops claude once the JS
// would have crashed, until claude has closed.
func forwardSignals(child *procs.Child, signals <-chan os.Signal, crashed <-chan struct{}, closed <-chan struct{}) {
	for {
		select {
		case sig := <-signals:
			child.Signal(sig.(syscall.Signal))
		case <-crashed:
			child.Signal(syscall.SIGTERM)
			crashed = nil
		case <-closed:
			return
		}
	}
}

// hitUsageLimit is l.80: a failed run whose final message reads like a usage limit.
func (r *claudeRun) hitUsageLimit() bool {
	result := r.events.result
	return !jsjson.Truthy(jsjson.Path(result, "ok")) && matchesIgnoringCase(limitText, jsjson.OrText(jsjson.Path(result, "final_message"), ""))
}

// finishOnUsageLimit is l.81-84: no run record; reason and errors say usage_limit, resets_at
// comes from a fresh usage reading (absent when it fails), an incident, then exit 75.
func (r *claudeRun) finishOnUsageLimit(cfg usage.Config, stdout, stderr io.Writer) int {
	result := r.events.result
	result.Set("reason", "budget_red")
	result.Set("errors", []any{"usage_limit"})
	current, err := usage.Get(cfg, true)
	var spawnErr *usage.SpawnError
	if errors.As(err, &spawnErr) {
		return crash(stderr, err) // an uncaught spawn error in the JS, not a rejection
	}
	if err == nil {
		result.Set("resets_at", or(jsjson.Path(current, "claude", "session", "resets_at"), nil))
	}
	context := jsjson.NewObject()
	context.Set("model", jsjson.Path(result, "model"))
	context.Set("resets_at", or(jsjson.Path(result, "resets_at"), nil))
	recordIncident(incident.Draft{Component: "bridge", Kind: "limit", Summary: "claude run ended on a session-limit message", Context: context, Project: r.cwd})
	return printResult(stdout, stderr, result, exitBudgetRed)
}

// finish is l.86-91: the run record, the "done" line, an incident for a failed run, the
// result, then the post-run usage refresh, which orch does not wait for (PE-12).
func (r *claudeRun) finish(stdout, stderr io.Writer) int {
	code := r.report(stdout, stderr)
	startUsageRefresh()
	return code
}

// report writes the run record, the "done" line, an incident for a failed run and the result,
// and returns the exit code.
func (r *claudeRun) report(stdout, stderr io.Writer) int {
	result := r.events.result
	hash := sha256.Sum256([]byte(r.prompt))
	record := jsjson.Spread(result)
	record.Set("prompt_sha256", hex.EncodeToString(hash[:]))
	record.Set("prompt_head", jsstr.Slice(r.prompt, 0, 400)) // U+FFFD for a split surrogate pair (PE-1)
	file, err := runrecord.Write(r.cwd, record, time.Now())
	if err != nil {
		return crash(stderr, err)
	}
	result.Set("record", file)
	ok := jsjson.Truthy(jsjson.Path(result, "ok"))
	durationMs, _ := jsjson.Path(result, "duration_ms").(float64)
	r.events.logf("done ok=%t turns=%s in %ss; record %s", ok, text(jsjson.Path(result, "num_turns")), text(jsjson.Round(durationMs/1000)), file)
	if !ok {
		errs := jsjson.Path(result, "errors").([]any)
		kind := "crash"
		if matchesIgnoringCase(limitOrRate, jsjson.Join(errs, " ")) {
			kind = "limit"
		}
		context := jsjson.NewObject()
		context.Set("model", or(jsjson.Path(result, "model"), nil))
		context.Set("exit_code", jsjson.Path(result, "exit_code"))
		recordIncident(incident.Draft{
			Component: "bridge",
			Kind:      kind,
			Summary:   "claude bridge failed: " + jsstr.Slice(jsjson.Join(errs, "; "), 0, 160), // PE-1
			Detail:    jsjson.Join(errs, "\n"),
			Context:   context,
			Project:   r.cwd,
		})
		return printResult(stdout, stderr, result, exitFailed)
	}
	return printResult(stdout, stderr, result, exitOK)
}

// noPostRunRefreshEnv set to "1" turns the post-run usage refresh off. Only the parity harness
// sets it: a refresh outliving orch would write usage.json into a case's sandbox after its
// files are compared, and the JS refresh never writes it (PE-12).
const noPostRunRefreshEnv = "ORCH_NO_POST_RUN_REFRESH"

// startUsageRefresh is l.87, `getUsage({ refresh: true }).catch(() => {})`, which the JS starts
// and then exits before it can complete (plan 3.10 item 3). orch runs it as `orch usage
// --refresh` in a session of its own with its stdio on /dev/null, and does not wait for it, so
// the run's exit code and timing are unchanged. The refresh is bounded by the timeouts of the
// feeds it reads (security 30 s, the Claude endpoint 15 s, codex app-server 20 s, sqlite3 5 s,
// opencode auth list 15 s). As in the JS, a refresh that cannot start is ignored.
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

// recordIncident appends an incident to the user's log. The JS wraps every such call in
// `try { ... } catch { /* ignore */ }`: a bridge never fails because its incident log does.
func recordIncident(draft incident.Draft) {
	home, err := paths.NodeHome(os.LookupEnv)
	if err != nil {
		return
	}
	_, _ = incident.New(home).Record(draft)
}

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

// logger writes the "[claude] " progress lines on stderr, unless --quiet.
type logger struct {
	w     io.Writer
	quiet bool
}

func (l *logger) printf(format string, args ...any) {
	if !l.quiet {
		fmt.Fprintf(l.w, "[claude] "+format+"\n", args...)
	}
}

// lockedBuffer collects claude's stderr; exec copies into it on its own goroutine.
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
