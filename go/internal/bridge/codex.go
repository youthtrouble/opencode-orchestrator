package bridge

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

var (
	codexValueKeys = []string{"model", "effort", "sandbox", "cwd", "artifact-root", "resume", "schema", "timeout-min", "prompt", "prompt-file", "add-dir"}
	codexFlagKeys  = []string{"force", "quiet", "ephemeral", "help"}
)

// Defaults of codex-exec.mjs (l.30-33).
const (
	defaultCodexModel       = "gpt-6.1-sol"
	defaultCodexEffort      = "high"
	defaultCodexSandbox     = "workspace-write"
	defaultCodexTimeoutMins = 60
)

// errModelIncludes is `model.includes("astra")` (l.36) for a bare --model, the JS value true.
var errModelIncludes = &jsError{class: "TypeError", message: "model.includes is not a function"}

// codexRequest is what codex-exec.mjs derives from its options before the budget gate
// (l.29-33). effort and sandbox are strings, or true for a bare --effort or --sandbox.
type codexRequest struct {
	opts    options
	cwd     string
	model   string
	effort  any
	sandbox any
}

// newCodexRequest resolves the working directory and reads the model, failing as the JS does:
// a bare --cwd is the path.resolve TypeError, and a bare --model the TypeError of the gate's
// `model.includes`, both before the gate runs.
func newCodexRequest(opts options) (codexRequest, error) {
	cwd, err := resolveProjectRoot(opts.Or("cwd", "."))
	if err != nil {
		return codexRequest{}, err
	}
	model, isString := opts.Or("model", defaultCodexModel).(string)
	if !isString {
		return codexRequest{}, errModelIncludes
	}
	return codexRequest{
		opts:    opts,
		cwd:     cwd,
		model:   model,
		effort:  opts.Or("effort", defaultCodexEffort),
		sandbox: opts.Or("sandbox", defaultCodexSandbox),
	}, nil
}

// isFrontierCodexModel is the gate's frontier test, `model.includes("astra")`: a
// case-sensitive substring test.
func isFrontierCodexModel(model string) bool {
	return strings.Contains(model, "astra")
}

// argv is the argv codex-exec.mjs passes to codex (l.39-45), the prompt being read from stdin
// ("-"). A bare --schema or --add-dir is the path.resolve TypeError. Every exec-only option
// (the sandbox flags, --add-dir) precedes the resume token: codex-cli 0.162.1's `exec resume`
// rejects them, while --output-schema and --ephemeral are accepted by both.
func (q codexRequest) argv() ([]string, error) {
	argv := append([]string{"exec", "--json", "--skip-git-repo-check", "-C", q.cwd}, codexSandboxArgs(q.sandbox)...)
	argv = append(argv, "-c", `model_reasoning_effort="`+text(q.effort)+`"`)
	addDir, err := q.resolvedPathArg("add-dir", "--add-dir")
	if err != nil {
		return nil, err
	}
	argv = append(argv, addDir...)
	if resume := q.opts.JS("resume"); jsjson.Truthy(resume) {
		argv = append(argv, "resume", text(resume))
	} else {
		argv = append(argv, "-m", q.model)
	}
	schema, err := q.resolvedPathArg("schema", "--output-schema")
	if err != nil {
		return nil, err
	}
	argv = append(argv, schema...)
	if jsjson.Truthy(q.opts.JS("ephemeral")) {
		argv = append(argv, "--ephemeral")
	}
	return append(argv, "-"), nil
}

// codexSandboxArgs selects the sandbox. codex-cli 0.162.1's --approve-for-me means the
// workspace-write sandbox with automatic approval review, and is rejected next to -s, so
// workspace-write (compared strictly) is that flag alone; anything else, a bare --sandbox's
// true included, is "-s <value>".
func codexSandboxArgs(sandbox any) []string {
	if sandbox == "workspace-write" {
		return []string{"--approve-for-me"}
	}
	return []string{"-s", text(sandbox)}
}

// resolvedPathArg is `flag <absolute path>` for a truthy path option, nothing otherwise.
func (q codexRequest) resolvedPathArg(key, flag string) ([]string, error) {
	v := q.opts.JS(key)
	if !jsjson.Truthy(v) {
		return nil, nil
	}
	abs, err := resolvePath(v)
	if err != nil {
		return nil, err
	}
	return []string{flag, abs}, nil
}

// startLine is the "start" log line (l.49).
func (q codexRequest) startLine() string {
	shown := q.model
	if jsjson.Truthy(q.opts.JS("resume")) {
		shown = "(resumed)"
	}
	return fmt.Sprintf("start model=%s effort=%s sandbox=%s cwd=%s", shown, text(q.effort), text(q.sandbox), q.cwd)
}

// codexResult is the result object of l.56, kept as fields because one of its values can be JS
// undefined: a thread.started event without a thread_id sets thread_id to undefined, which
// keeps the key in its third place but leaves it out of JSON.stringify, until a later
// thread.started gives it a value again in that same place. JSON builds the object in the JS
// key order.
type codexResult struct {
	ok              bool
	threadID        any
	threadIDDefined bool
	model           string
	effort, sandbox any
	cwd, startedAt  string
	finalMessage    string
	usage           any
	filesChanged    []any // {path, kind} objects, each key absent when undefined
	commands        []any // {command, exit_code} objects, likewise
	errors          []any // an undefined message is nil, as JSON.stringify writes it in an array
	// Appended when codex closes (l.79-80), then by the tool-host no-op path (l.87).
	closed     bool
	exitCode   any
	durationMs float64
	reason     string
}

func newCodexResult(q codexRequest, startedAt string) *codexResult {
	return &codexResult{
		threadID:        q.opts.Or("resume", nil),
		threadIDDefined: true,
		model:           q.model,
		effort:          q.effort,
		sandbox:         q.sandbox,
		cwd:             q.cwd,
		startedAt:       startedAt,
		filesChanged:    []any{},
		commands:        []any{},
		errors:          []any{},
	}
}

// JSON is the result as JSON.stringify sees it.
func (r *codexResult) JSON() *jsjson.Object {
	o := jsjson.NewObject()
	o.Set("ok", r.ok)
	o.Set("harness", "codex")
	r.setThreadID(o)
	o.Set("model", r.model)
	o.Set("effort", r.effort)
	o.Set("sandbox", r.sandbox)
	o.Set("cwd", r.cwd)
	o.Set("started_at", r.startedAt)
	o.Set("final_message", r.finalMessage)
	o.Set("usage", r.usage)
	o.Set("files_changed", r.filesChanged)
	o.Set("commands", r.commands)
	o.Set("errors", r.errors)
	if r.closed {
		o.Set("exit_code", r.exitCode)
		o.Set("duration_ms", r.durationMs)
	}
	if r.reason != "" {
		o.Set("reason", r.reason)
	}
	return o
}

// setThreadID sets thread_id on o unless it is undefined.
func (r *codexResult) setThreadID(o *jsjson.Object) {
	if r.threadIDDefined {
		o.Set("thread_id", r.threadID)
	}
}

// toolHostFailure is NOOP_RE (l.85), /i: see limitOrRate for the ASCII-only case rule.
var toolHostFailure = regexp.MustCompile(`timed out negotiating with the code-mode host|could not run|unable to (run|execute)|tool (host|calls?) (failed|unavailable)|no tool`)

// flagToolHostOutage is l.86-89: a completed turn that ran nothing and whose message reports
// a tool-host failure is a failed run, with an incident of its own.
func (r *codexResult) flagToolHostOutage(root string) {
	if !r.ok || len(r.commands) > 0 || len(r.filesChanged) > 0 || !matchesIgnoringCase(toolHostFailure, r.finalMessage) {
		return
	}
	r.ok = false
	r.reason = "tool_host_unavailable"
	r.errors = append(r.errors, "codex completed the turn without executing any tool; its message reports tool-host failure (retry after ~60s)")
	context := jsjson.NewObject()
	context.Set("model", r.model)
	r.setThreadID(context)
	recordIncident(incident.Draft{
		Component: "bridge",
		Kind:      "crash",
		Summary:   "codex turn completed with zero tool calls; code-mode host timeout reported in prose",
		Detail:    jsstr.Slice(r.finalMessage, 0, 400), // PE-1
		Context:   context,
		Project:   root,
	})
}

// codexDoneLine is the "done" line (l.92).
func codexDoneLine(result *jsjson.Object, record string) string {
	return fmt.Sprintf("done ok=%t in %ss; record %s", jsjson.Truthy(jsjson.Path(result, "ok")), durationSeconds(result), record)
}

// codexEvents applies codex's `exec --json` events to the result and writes the progress log,
// as the JS line handler does (l.58-75), on the jsThread that serializes them.
type codexEvents struct {
	*jsThread
	result *codexResult
}

func newCodexEvents(log *logger, result *codexResult) *codexEvents {
	e := &codexEvents{result: result}
	e.jsThread = newJSThread(log, e)
	return e
}

// handleEvent applies one line. Unparsable lines are skipped; primitives and arrays have no
// type. The returned error is a TypeError the JS would die of.
func (e *codexEvents) handleEvent(line string) error {
	ev, err := jsjson.Unmarshal([]byte(line))
	if err != nil {
		return nil
	}
	if ev == nil {
		return nullPropertyRead("type")
	}
	r := e.result
	switch jsjson.Path(ev, "type") {
	case "thread.started":
		r.threadID, r.threadIDDefined = jsjson.Lookup(ev, "thread_id")
		e.log.printf("thread %s", jsjson.TemplateStringOf(r.threadID, r.threadIDDefined))
	case "item.completed":
		return e.handleItem(or(jsjson.Path(ev, "item"), jsjson.NewObject()))
	case "turn.completed":
		r.usage = or(jsjson.Path(ev, "usage"), nil)
		r.ok = true
	case "turn.failed":
		failure, err := turnFailure(ev)
		if err != nil {
			return err
		}
		r.errors = append(r.errors, failure)
		r.ok = false
	}
	return nil
}

// turnFailure is `ev.error?.message || JSON.stringify(ev.error || ev)`.
func turnFailure(ev any) (any, error) {
	if message := jsjson.Path(ev, "error", "message"); jsjson.Truthy(message) {
		return message, nil
	}
	data, err := jsjson.Marshal(or(jsjson.Path(ev, "error"), ev))
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// handleItem applies an item.completed event's item (l.63-69).
func (e *codexEvents) handleItem(item any) error {
	r := e.result
	switch jsjson.Path(item, "type") {
	case "agent_message":
		itemText := jsjson.Path(item, "text")
		message, err := logText(itemText, `(it.text || "")`, 160)
		if err != nil {
			return err
		}
		if jsjson.Truthy(itemText) {
			r.finalMessage = itemText.(string)
		}
		e.log.printf("message: %s", message)
	case "command_execution":
		r.commands = append(r.commands, definedFields(item, "command", "exit_code"))
		command, err := commandText(jsjson.Path(item, "command"))
		if err != nil {
			return err
		}
		e.log.printf("ran: %s -> %s", command, jsjson.TemplateStringOf(jsjson.Lookup(item, "exit_code")))
	case "file_change":
		return e.handleChanges(or(jsjson.Path(item, "changes"), []any{}))
	case "error":
		message, present := jsjson.Lookup(item, "message")
		r.errors = append(r.errors, message)
		e.log.printf("error: %s", jsjson.TemplateStringOf(message, present))
	case "reasoning":
		thought, err := logText(jsjson.Path(item, "text"), `(it.text || "")`, 100)
		if err != nil {
			return err
		}
		e.log.printf("thinking: %s", thought)
	}
	return nil
}

// handleChanges is `for (const c of it.changes || [])` and its "files" log line. A string
// iterates its characters, each pushing {}, and then has no map method; anything else that is
// not an array is not iterable (see notIterable). A null change has no path.
func (e *codexEvents) handleChanges(changes any) error {
	switch list := changes.(type) {
	case []any:
		paths := make([]any, 0, len(list))
		for _, change := range list {
			if change == nil {
				return nullPropertyRead("path")
			}
			e.result.filesChanged = append(e.result.filesChanged, definedFields(change, "path", "kind"))
			paths = append(paths, jsjson.Path(change, "path"))
		}
		e.log.printf("files: %s", jsjson.Join(paths, ", "))
		return nil
	case string:
		return &jsError{class: "TypeError", message: "(it.changes || []).map is not a function"}
	}
	return notIterable(changes)
}

// notIterable is V8's TypeError for `for...of` over a truthy non-iterable JSON value it cannot
// name by its source text (as it does for `it.changes || []`): the type, then the value of a
// primitive, as Node 26 prints "number 5 is not iterable (...)" and "object is not iterable (...)".
func notIterable(value any) *jsError {
	described := "object"
	switch value.(type) {
	case float64:
		described = "number " + jsjson.TemplateString(value)
	case bool:
		described = "boolean " + jsjson.TemplateString(value)
	}
	return &jsError{class: "TypeError", message: described + " is not iterable (cannot read property Symbol(Symbol.iterator))"}
}

// commandText is `${(it.command || "").slice(0, 120)}`: a string keeps 120 UTF-16 units, an
// array 120 elements, joined; any other truthy value has no slice method.
func commandText(command any) (string, error) {
	switch c := or(command, "").(type) {
	case string:
		return jsstr.Slice(c, 0, 120), nil
	case []any:
		return text(c[:min(len(c), 120)]), nil
	}
	return "", &jsError{class: "TypeError", message: `(it.command || "").slice is not a function`}
}

// definedFields is the object literal {k1: v.k1, k2: v.k2, ...}: an undefined value leaves its
// key out of JSON, a null keeps it.
func definedFields(v any, keys ...string) *jsjson.Object {
	o := jsjson.NewObject()
	for _, key := range keys {
		if value, present := jsjson.Lookup(v, key); present {
			o.Set(key, value)
		}
	}
	return o
}

// logText is `<expr>.slice(0, n).replace(/\n/g, " ")` of an event's text, which the JS
// evaluates even under --quiet: "" for a falsy value; a TypeError for a truthy non-string (an
// array has a slice method, but its slice no replace). A cut inside a surrogate pair leaves
// U+FFFD where the JS prints a lone surrogate, which Node prints as U+FFFD too.
func logText(v any, expr string, n int) (string, error) {
	if !jsjson.Truthy(v) {
		return "", nil
	}
	switch s := v.(type) {
	case string:
		return strings.ReplaceAll(jsstr.Slice(s, 0, n), "\n", " "), nil
	case []any:
		return "", &jsError{class: "TypeError", message: expr + ".slice(...).replace is not a function"}
	}
	return "", &jsError{class: "TypeError", message: expr + ".slice is not a function"}
}

// handleClose is the child's "close" handler (l.77-82).
func (e *codexEvents) handleClose(exitCode any, stderr []byte, duration time.Duration) {
	r := e.result
	r.closed = true
	r.exitCode = exitCode
	r.durationMs = float64(duration.Milliseconds())
	if exitCode == 0.0 {
		return
	}
	if len(r.errors) == 0 {
		r.errors = append(r.errors, fmt.Sprintf("codex exited %s: %s", text(exitCode), stderrTail(stderr)))
	}
	r.ok = false
}
