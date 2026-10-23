package bridge

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// RunClaude is `orch claude-exec [args]`: it runs `claude -p` on the prompt and prints one JSON
// result, exactly as bin/claude-exec.mjs does, step for step, and returns the exit code: 0 ok,
// 1 failed or crashed, 2 no prompt, 75 budget red (refused, or the run hit a usage limit).
// The differences are the documented exceptions PE-8 to PE-12 and the crash output (only the
// error line of an exception the JS leaves uncaught).
func RunClaude(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	parsed, positional := cliargs.ParseArgs(args, claudeValueKeys, claudeFlagKeys)
	opts := options(parsed)
	if parsed.IsFlag("help") {
		fmt.Fprintln(stdout, cmdref.ClaudeExecHelp)
		return exitOK
	}
	prompt, err := cliargs.ReadPrompt(parsed, positional, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return exitPrompt
	}
	cwd, err := resolveProjectRoot(opts.Or("cwd", "."))
	if err != nil {
		return crash(stderr, err)
	}
	model := opts.Or("model", defaultClaudeModel)
	root, err := artifactRoot(opts, cwd)
	if err != nil {
		return crash(stderr, err)
	}
	timeout := timeoutDelay(opts.JS("timeout-min"), 60)
	log := &logger{w: stderr, prefix: "[claude] ", quiet: parsed.IsFlag("quiet")}

	cfg, err := usageConfig()
	if err != nil {
		return crash(stderr, err)
	}
	decision, err := budget.Gate(cfg, routing.Anthropic, parsed.IsFlag("force"), matchesIgnoringCase(fable, text(model)), false)
	if err != nil {
		return crash(stderr, err)
	}
	if !decision.Allowed {
		return refuse(stdout, stderr, &decision, root, "claude-exec")
	}

	argv, err := claudeArgv(opts, model, prompt)
	if err != nil {
		return crash(stderr, err)
	}
	started := time.Now()
	log.printf("%s", startLine(opts, model, cwd))
	result := newClaudeResult(opts, model, cwd, jsjson.ISOString(started))
	run := &claudeRun{events: newClaudeEvents(log, result), cwd: cwd, artifactRoot: root, prompt: prompt, started: started}
	if err := run.execute(argv, timeout); err != nil {
		return crash(stderr, err)
	}
	if run.hitUsageLimit() {
		return run.finishOnUsageLimit(cfg, stdout, stderr)
	}
	return run.finish(stdout, stderr)
}

// claudeRun is one claude child and what is known about it.
type claudeRun struct {
	events       *claudeEvents
	cwd          string
	artifactRoot string
	prompt       string
	started      time.Time
}

// execute runs claude to completion (l.53-78), with CLAUDECODE removed from its environment
// and the prompt after "--" in its argv; see childRun.run.
func (r *claudeRun) execute(argv []string, timeout time.Duration) error {
	return childRun{
		name:       "claude",
		args:       argv,
		dir:        r.cwd,
		env:        procs.WithoutEnv(os.Environ(), "CLAUDECODE"),
		timeout:    timeout,
		timeoutLog: "timeout; killing",
		started:    r.started,
	}.run(r.events.jsThread)
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
	recordIncident(incident.Draft{Component: "bridge", Kind: "limit", Summary: "claude run ended on a session-limit message", Context: context, Project: text(or(r.artifactRoot, r.cwd))})
	return printResult(stdout, stderr, result, exitBudgetRed)
}

// finish is l.86-91: the run record, the post-run usage refresh, which orch does not wait for
// (PE-12), the "done" line, an incident for a failed run, then the result.
func (r *claudeRun) finish(stdout, stderr io.Writer) int {
	return finishedRun{log: r.events.log, cwd: r.cwd, artifactRoot: r.artifactRoot, prompt: r.prompt, afterRecord: startUsageRefresh, doneLine: claudeDoneLine}.report(stdout, stderr, r.events.result)
}

// claudeDoneLine is the "done" line (l.88).
func claudeDoneLine(result *jsjson.Object, record string) string {
	return fmt.Sprintf("done ok=%t turns=%s in %ss; record %s", jsjson.Truthy(jsjson.Path(result, "ok")), text(jsjson.Path(result, "num_turns")), durationSeconds(result), record)
}
