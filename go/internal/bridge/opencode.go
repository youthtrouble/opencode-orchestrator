package bridge

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

// opencode-exec.mjs has no --help handling and no effort key: --help is parsed and ignored, and
// `--effort X` is a flag followed by the positional X, which becomes the prompt (latent JS
// defects, mirrored).
var (
	opencodeValueKeys = []string{"agent", "cwd", "artifact-root", "resume", "timeout-min", "prompt", "prompt-file", "model"}
	opencodeFlagKeys  = []string{"quiet", "force", "help"}
)

// Defaults of opencode-exec.mjs (l.19, l.25).
const (
	defaultOpencodeAgent       = "conductor"
	defaultOpencodeTimeoutMins = 120
)

// RunOpencode is `orch opencode-exec [args]`: it runs one headless `opencode run` turn on the
// prompt and prints one JSON result, exactly as bin/opencode-exec.mjs does, step for step, and
// returns the exit code: 0 ok, 1 failed or crashed, 2 no prompt, 75 budget red. The differences
// are the documented exceptions PE-1, PE-8 to PE-11 and PE-22 and the crash output (only the
// error line of an exception the JS leaves uncaught). Unlike the other bridges it logs neither a
// start, a timeout nor a done line, and starts no post-run usage refresh.
func RunOpencode(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	parsed, positional := cliargs.ParseArgs(args, opencodeValueKeys, opencodeFlagKeys)
	opts := options(parsed)
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
		name:    "opencode",
		args:    opencodeArgv(opts, prompt),
		dir:     cwd,
		timeout: timeoutDelay(opts.JS("timeout-min"), defaultOpencodeTimeoutMins),
		started: started,
	}.run(events.jsThread)
	if err != nil {
		return crash(stderr, err)
	}
	return finishedRun{log: log, cwd: cwd, artifactRoot: root, prompt: prompt}.report(stdout, stderr, result)
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
// and record are appended when the run ends. It has no model key, so the failure incident's
// context.model is always null.
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
	return nil
}

// handleClose is the child's "close" handler (l.36-39): the bare stderr tail is the error of
// a non-zero exit, "" included.
func (e *opencodeEvents) handleClose(exitCode any, stderr []byte, duration time.Duration) {
	e.result.Set("final_message", e.text)
	e.result.Set("ok", exitCode == 0.0)
	e.result.Set("exit_code", exitCode)
	e.result.Set("duration_ms", float64(duration.Milliseconds()))
	if exitCode != 0.0 {
		errors := jsjson.Path(e.result, "errors").([]any)
		e.result.Set("errors", append(errors, stderrTail(stderr)))
	}
}
