package bridge

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

// RunCodex is `orch codex-exec [args]`: it runs `codex exec` on the prompt, given on codex's
// stdin, and prints one JSON result, exactly as bin/codex-exec.mjs does, step for step, and
// returns the exit code: 0 ok, 1 failed or crashed, 2 no prompt, 75 budget red. The
// differences are the documented exceptions PE-1, PE-8 to PE-13, PE-44 and PE-45 and the crash
// output (only the error line of an exception the JS leaves uncaught).
func RunCodex(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	parsed, positional := cliargs.ParseArgs(args, codexValueKeys, codexFlagKeys)
	opts := options(parsed)
	if parsed.IsFlag("help") {
		fmt.Fprintln(stdout, cmdref.CodexExecHelp)
		return exitOK
	}
	prompt, err := cliargs.ReadPrompt(parsed, positional, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return exitPrompt
	}
	request, err := newCodexRequest(opts)
	if err != nil {
		return crash(stderr, err)
	}
	timeout := timeoutDelay(opts.JS("timeout-min"), defaultCodexTimeoutMins)
	root, err := artifactRoot(opts, request.cwd)
	if err != nil {
		return crash(stderr, err)
	}
	log := &logger{w: stderr, prefix: "[codex] ", quiet: parsed.IsFlag("quiet")}

	cfg, err := usageConfig()
	if err != nil {
		return crash(stderr, err)
	}
	cfg.ProjectRoot = request.cwd // its policy decides whether the run may draw ChatGPT credits
	decision, err := budget.Gate(cfg, routing.OpenAI, parsed.IsFlag("force"), isFrontierCodexModel(request.model), true)
	if err != nil {
		return crash(stderr, err)
	}
	if !decision.Allowed {
		return refuse(stdout, stderr, &decision, root, "codex-exec")
	}

	argv, err := request.argv()
	if err != nil {
		return crash(stderr, err)
	}
	started := time.Now()
	log.printf("%s", request.startLine())
	result := newCodexResult(request, jsjson.ISOString(started))
	events := newCodexEvents(log, result)
	err = childRun{
		name:       "codex",
		args:       argv,
		dir:        request.cwd,
		stdin:      strings.NewReader(prompt), // an unread prompt is no error (PE-13)
		timeout:    timeout,
		timeoutLog: "timeout; killing",
		started:    started,
	}.run(events.jsThread)
	if err != nil {
		return crash(stderr, err)
	}
	result.flagToolHostOutage(root)
	out := result.JSON()
	if decision.CreditFunded {
		out.Set("credits", creditsSpent(decision.CreditsBefore, budget.FreshCreditsBalance(cfg)))
	}
	return finishedRun{
		log: log, cwd: request.cwd, artifactRoot: root, prompt: prompt, afterRecord: startUsageRefresh, doneLine: codexDoneLine,
		timedOut: events.stoppedAtTimeout(), timeoutMinutes: timeoutMinutes(opts.JS("timeout-min"), defaultCodexTimeoutMins),
	}.report(stdout, stderr, out)
}

// creditsSpent is the {before, after} balance a credit-funded run records.
func creditsSpent(before, after any) *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("before", before)
	out.Set("after", after)
	return out
}
