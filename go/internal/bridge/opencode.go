package bridge

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

// opencode-exec.mjs had no --help handling and no effort key, so `--effort X` made X the prompt.
// orch prints help and reads --effort as a value it ignores (PE-46): `opencode run` has
// no effort option (its --variant is model-specific), and `orch worker` passes --effort to
// every harness.
var (
	opencodeValueKeys = []string{"agent", "cwd", "artifact-root", "resume", "timeout-min", "prompt", "prompt-file", "model", "effort"}
	opencodeFlagKeys  = []string{"quiet", "force", "help"}
)

// opencodeExecHelp is what `orch opencode-exec --help` prints (PE-46).
const opencodeExecHelp = `Run one headless opencode turn (opencode run --format json) on the ChatGPT subscription, structured result back.

  orch opencode-exec [--agent conductor] [--model PROVIDER/MODEL] [--effort LEVEL (ignored)] [--cwd DIR]
                     [--artifact-root DIR] [--resume SESSION_ID] [--timeout-min 120] [--force] [--quiet]
                     [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]

Prints one JSON object on stdout: { ok, harness, session_id, agent, cwd, started_at, final_message,
errors, exit_code, duration_ms, record }. opencode is stopped with SIGINT at the --timeout-min cap and
on a forwarded SIGTERM. Exit codes: 0 ok, 1 failed, 2 no prompt, 75 budget refused.`

// Defaults of opencode-exec.mjs (l.19, l.25).
const (
	defaultOpencodeAgent       = "conductor"
	defaultOpencodeTimeoutMins = 120
)

// RunOpencode is `orch opencode-exec [args]`: it runs one headless `opencode run` turn on the
// prompt and prints one JSON result, exactly as bin/opencode-exec.mjs does, step for step, and
// returns the exit code: 0 ok, 1 failed or crashed, 2 no prompt, 75 budget red. The differences
// are the documented exceptions PE-1, PE-8 to PE-11, PE-22, PE-44 to PE-47 and PE-65 and the
// crash output (only the error line of an exception the JS leaves uncaught). Unlike the other
// bridges it logs neither a start, a timeout nor a done line, and starts no post-run usage
// refresh.
func RunOpencode(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	parsed, positional := cliargs.ParseArgs(args, opencodeValueKeys, opencodeFlagKeys)
	opts := options(parsed)
	if parsed.IsFlag("help") {
		fmt.Fprintln(stdout, opencodeExecHelp)
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
	log := &logger{w: stderr, prefix: "[opencode] ", quiet: parsed.IsFlag("quiet")}
	root, err := artifactRoot(opts, cwd)
	if err != nil {
		return crash(stderr, err)
	}

	cfg, err := usageConfig()
	if err != nil {
		return crash(stderr, err)
	}
	decision, err := budget.Gate(cfg, routing.OpenAI, parsed.IsFlag("force"), false, false)
	if err != nil {
		return crash(stderr, err)
	}
	if !decision.Allowed {
		return refuse(stdout, stderr, &decision, root, "opencode-exec")
	}

	started := time.Now()
	result := newOpencodeResult(opts, cwd, jsjson.ISOString(started))
	events := newOpencodeEvents(log, result)
	err = childRun{
		name:       "opencode",
		args:       opencodeArgv(opts, prompt),
		dir:        cwd,
		timeout:    timeoutDelay(opts.JS("timeout-min"), defaultOpencodeTimeoutMins),
		stopSignal: syscall.SIGINT, // SIGTERM leaves the session running in `opencode serve` (PE-65)
		started:    started,
	}.run(events.jsThread)
	if err != nil {
		return crash(stderr, err)
	}
	return finishedRun{
		log: log, cwd: cwd, artifactRoot: root, prompt: prompt,
		timedOut: events.stoppedAtTimeout(), timeoutMinutes: timeoutMinutes(opts.JS("timeout-min"), defaultOpencodeTimeoutMins),
		model: opts.Or("model", nil), // the result has no model key (PE-47)
	}.report(stdout, stderr, result)
}

// opencodeArgv is the argv opencode-exec.mjs passes to opencode (l.19-22): the prompt is the
// last element, with no "--" before it.
func opencodeArgv(opts options, prompt string) []string {
	argv := []string{"run", "--format", "json", "--auto", "--agent", text(opts.Or("agent", defaultOpencodeAgent))}
	if resume := opts.JS("resume"); jsjson.Truthy(resume) {
		argv = append(argv, "--session", text(resume))
	}
	if model := opts.JS("model"); jsjson.Truthy(model) {
		argv = append(argv, "--model", text(model))
	}
	return append(argv, prompt)
}

// newOpencodeResult is the result object with its JS key order (l.26); exit_code, duration_ms
// and record are appended when the run ends. It has no model key; the failure incident takes
// context.model from --model instead (PE-47).
func newOpencodeResult(opts options, cwd, startedAt string) *jsjson.Object {
	result := jsjson.NewObject()
	result.Set("ok", false)
	result.Set("harness", "opencode")
	result.Set("session_id", opts.Or("resume", nil))
	result.Set("agent", opts.Or("agent", defaultOpencodeAgent))
	result.Set("cwd", cwd)
	result.Set("started_at", startedAt)
	result.Set("final_message", "")
	result.Set("errors", []any{})
	return result
}

// opencodeEvents applies opencode's `run --format json` events to the result and writes the
// progress log, as the JS line handler does (l.29-35), on the jsThread that serializes them.
type opencodeEvents struct {
	*jsThread
	result *jsjson.Object
	text   string // the last text part, which becomes final_message on close
}

func newOpencodeEvents(log *logger, result *jsjson.Object) *opencodeEvents {
	e := &opencodeEvents{result: result}
	e.jsThread = newJSThread(log, e)
	return e
}

// handleEvent applies one line. Unparsable lines are skipped; primitives and arrays have none
// of the properties read, and a part that is not an object has no type. The returned error is
// a TypeError the JS would die of.
func (e *opencodeEvents) handleEvent(line string) error {
	ev, err := jsjson.Unmarshal([]byte(line))
	if err != nil {
		return nil
	}
	if ev == nil {
		return nullPropertyRead("sessionID")
	}
	if id := jsjson.Path(ev, "sessionID"); jsjson.Truthy(id) && !jsjson.Truthy(jsjson.Path(e.result, "session_id")) {
		e.result.Set("session_id", id)
	}
	part := or(jsjson.Path(ev, "part"), jsjson.NewObject())
	partType := jsjson.Path(part, "type")
	if partText := jsjson.Path(part, "text"); partType == "text" && jsjson.Truthy(partText) {
		message, err := logText(partText, "p.text", 160)
		if err != nil {
			return err
		}
		e.text = partText.(string)
		e.log.printf("message: %s", message)
	}
	if partType == "tool" {
		e.log.printf("tool: %s", jsjson.TemplateStringOf(jsjson.Lookup(part, "tool")))
	}
	if jsjson.Path(ev, "type") == "error" {
		return e.handleError(ev)
	}
	return nil
}

// handleError collects an error event's message into errors, so that a usage limit is filed as
// kind limit with its text (PE-44). opencode 2.0.32's `run --format json` reports a failed run as
// {"type":"error","sessionID":...,"error":{"type":"unknown","message":...}}; a session error
// object keeps its message under data.message.
func (e *opencodeEvents) handleError(ev any) error {
	message := or(jsjson.Path(ev, "error", "data", "message"), jsjson.Path(ev, "error", "message"))
	if !jsjson.Truthy(message) {
		data, err := jsjson.Marshal(or(jsjson.Path(ev, "error"), ev))
		if err != nil {
			return err
		}
		message = string(data)
	}
	errors := jsjson.Path(e.result, "errors").([]any)
	e.result.Set("errors", append(errors, message))
	e.log.printf("error: %s", text(message))
	return nil
}

// handleClose is the child's "close" handler (l.36-39): the bare stderr tail, "" included, is
// the error of a non-zero exit that reported none in its events (PE-44).
func (e *opencodeEvents) handleClose(exitCode any, stderr []byte, duration time.Duration) {
	e.result.Set("final_message", e.text)
	e.result.Set("ok", exitCode == 0.0)
	e.result.Set("exit_code", exitCode)
	e.result.Set("duration_ms", float64(duration.Milliseconds()))
	if errors := jsjson.Path(e.result, "errors").([]any); exitCode != 0.0 && len(errors) == 0 {
		e.result.Set("errors", append(errors, stderrTail(stderr)))
	}
}
