// Package bridge runs a coding CLI on a bounded task and reports one structured result, as the
// JS bridges in orchestrator/bin do. RunClaude is `orch claude-exec` (bin/claude-exec.mjs).
//
// The JS reads its options as JS values: a value key given last on the command line, without
// its value, is the boolean true, not a string. Those values are carried here as any (a string
// or true) so that they print, serialize and coerce exactly as the JS ones do.
package bridge

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

var (
	claudeValueKeys = []string{"model", "effort", "permission-mode", "allowed-tools", "disallowed-tools", "max-turns", "max-budget-usd", "cwd", "resume", "agent", "append-system-prompt", "json-schema", "timeout-min", "prompt", "prompt-file", "add-dir"}
	claudeFlagKeys  = []string{"force", "quiet", "help"}
)

// defaultClaudeModel is the model when --model is not given.
const defaultClaudeModel = "claude-opus-5-5"

// claudeOptions are the parsed command-line options, read as the JS reads `opts`.
type claudeOptions cliargs.Opts

// value is opts[key]: nil when absent (JS undefined), true for a bare key, else the string.
func (o claudeOptions) value(key string) any {
	opt, present := o[key]
	switch {
	case !present:
		return nil
	case opt.Flag:
		return true
	}
	return opt.Value
}

// or is `opts[key] || fallback`.
func (o claudeOptions) or(key string, fallback any) any {
	if v := o.value(key); jsjson.Truthy(v) {
		return v
	}
	return fallback
}

// claudeArgv is the argv claude-exec.mjs passes to claude (l.38-49). Node's spawn coerces the
// value true to "true". --json-schema passes the schema file's contents; --add-dir an absolute
// path. A bare --json-schema or --add-dir is the path.resolve TypeError, and an unreadable
// schema file Node's fs error, both uncaught in the JS.
func claudeArgv(opts claudeOptions, model any, prompt string) ([]string, error) {
	argv := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", text(opts.or("permission-mode", "auto")), "--permission-prompts", "none"}
	if resume := opts.value("resume"); jsjson.Truthy(resume) {
		argv = append(argv, "--resume", text(resume))
	} else {
		argv = append(argv, "--model", text(model))
	}
	for _, flag := range []struct{ key, arg string }{
		{"effort", "--effort"},
		{"allowed-tools", "--allowedTools"},
		{"disallowed-tools", "--disallowedTools"},
		{"max-turns", "--max-turns"},
		{"max-budget-usd", "--max-budget-usd"},
		{"agent", "--agent"},
		{"append-system-prompt", "--append-system-prompt"},
	} {
		if v := opts.value(flag.key); jsjson.Truthy(v) {
			argv = append(argv, flag.arg, text(v))
		}
	}
	if schema := opts.value("json-schema"); jsjson.Truthy(schema) {
		file, err := resolvePath(schema)
		if err != nil {
			return nil, err
		}
		data, err := nodefs.ReadFile(file)
		if err != nil {
			return nil, &jsError{class: "Error", message: err.Error()}
		}
		argv = append(argv, "--json-schema", jsstr.DecodeUTF8(data))
	}
	if dir := opts.value("add-dir"); jsjson.Truthy(dir) {
		abs, err := resolvePath(dir)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--add-dir", abs)
	}
	return append(argv, "--", prompt), nil
}

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

// text is String(v) for an option value or a value read from an event.
func text(v any) string { return jsjson.TemplateString(v) }

// startLine is the "start" log line (l.52). Its mode falls back to "acceptEdits" although
// claude is passed "auto": a latent JS defect, mirrored.
func startLine(opts claudeOptions, model any, cwd string) string {
	shown := model
	if jsjson.Truthy(opts.value("resume")) {
		shown = "(resumed)"
	}
	effort := ""
	if v := opts.value("effort"); jsjson.Truthy(v) {
		effort = " effort=" + text(v)
	}
	return fmt.Sprintf("start model=%s%s mode=%s cwd=%s", text(shown), effort, text(opts.or("permission-mode", "acceptEdits")), cwd)
}

// newClaudeResult is the result object with its JS key order (l.57); exit_code, duration_ms
// and then record (or reason and resets_at) are appended when the run ends.
func newClaudeResult(opts claudeOptions, model any, cwd, startedAt string) *jsjson.Object {
	result := jsjson.NewObject()
	result.Set("ok", false)
	result.Set("harness", "claude")
	result.Set("session_id", opts.or("resume", nil))
	result.Set("model", model)
	result.Set("effort", opts.or("effort", nil))
	result.Set("cwd", cwd)
	result.Set("started_at", startedAt)
	result.Set("final_message", "")
	result.Set("usage", nil)
	result.Set("model_usage", nil)
	result.Set("cost_usd", nil)
	result.Set("num_turns", nil)
	result.Set("permission_denials", []any{})
	result.Set("tools_used", jsjson.NewObject())
	result.Set("errors", []any{})
	result.Set("structured_output", nil)
	return result
}

// claudeEvents applies claude's stream-json events to the result and writes the progress log,
// as the JS line handler does (l.59-73). Line events, the timeout and the end of the run come
// from different goroutines; mu serializes them, so the log order is the order of events, as
// in single-threaded JS.
type claudeEvents struct {
	mu     sync.Mutex
	log    *logger
	result *jsjson.Object
	// crash is the exception that would have killed the JS inside its line handler; once set,
	// later events and log lines are dropped, as the JS process is gone.
	crash   error
	crashed chan struct{} // closed when crash is set
	closed  bool          // claude has closed; the timeout no longer logs
}

func newClaudeEvents(log *logger, result *jsjson.Object) *claudeEvents {
	return &claudeEvents{log: log, result: result, crashed: make(chan struct{})}
}

// handleLine is the readline "line" listener.
func (e *claudeEvents) handleLine(line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.crash != nil {
		return
	}
	if err := e.handleEvent(line); err != nil {
		e.crash = err
		close(e.crashed)
	}
}

// logf writes one progress line unless the JS would already have died.
func (e *claudeEvents) logf(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.crash == nil {
		e.log.printf(format, args...)
	}
}

// logIfRunning writes a progress line unless claude has closed or the JS would have crashed,
// and reports whether it did; the JS clears the timer on close.
func (e *claudeEvents) logIfRunning(line string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.crash != nil {
		return false
	}
	e.log.printf("%s", line)
	return true
}

// close is the child's "close" handler (l.74-78).
func (e *claudeEvents) close(code *int, waitErr error, stderr []byte, duration time.Duration) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.crash != nil {
		return e.crash
	}
	if waitErr != nil {
		return waitErr
	}
	exitCode := any(nil)
	if code != nil {
		exitCode = float64(*code)
	}
	e.result.Set("exit_code", exitCode)
	e.result.Set("duration_ms", float64(duration.Milliseconds()))
	if exitCode == 0.0 {
		return nil
	}
	if len(jsjson.Path(e.result, "errors").([]any)) == 0 {
		e.pushError(fmt.Sprintf("claude exited %s: %s", text(exitCode), stderrTail(stderr)))
	}
	e.result.Set("ok", false)
	return nil
}

var (
	errNullEvent = &jsError{class: "TypeError", message: "Cannot read properties of null (reading 'session_id')"}
	errNullBlock = &jsError{class: "TypeError", message: "Cannot read properties of null (reading 'type')"}
	// errPathArgBoolean is path.resolve(true).
	errPathArgBoolean = &jsError{class: "TypeError [ERR_INVALID_ARG_TYPE]", message: cliargs.ErrPathArgBoolean.Error()}
)

// handleEvent applies one line. Unparsable lines are skipped; primitives and arrays have none
// of the properties read. The returned error is a TypeError the JS would die of.
func (e *claudeEvents) handleEvent(line string) error {
	ev, err := jsjson.Unmarshal([]byte(line))
	if err != nil {
		return nil
	}
	if ev == nil {
		return errNullEvent
	}
	event, isObject := ev.(*jsjson.Object)
	if !isObject {
		return nil
	}
	if id := jsjson.Path(event, "session_id"); jsjson.Truthy(id) && !jsjson.Truthy(jsjson.Path(e.result, "session_id")) {
		e.result.Set("session_id", id)
	}
	switch jsjson.Path(event, "type") {
	case "system":
		if jsjson.Path(event, "subtype") == "init" {
			e.log.printf("session %s model=%s", jsjson.TemplateStringOf(jsjson.Lookup(event, "session_id")), jsjson.TemplateStringOf(jsjson.Lookup(event, "model")))
		}
	case "assistant":
		return e.handleAssistant(event)
	case "result":
		return e.handleResult(event)
	}
	return nil
}

// handleAssistant iterates `ev.message?.content || []`. A string iterates by character and no
// character has a type; any other truthy non-array is not iterable.
func (e *claudeEvents) handleAssistant(event *jsjson.Object) error {
	switch content := jsjson.Path(event, "message", "content").(type) {
	case []any:
		for _, block := range content {
			if err := e.handleBlock(block); err != nil {
				return err
			}
		}
	case string:
	default:
		if jsjson.Truthy(content) {
			return &jsError{class: "TypeError", message: "ev.message?.content is not iterable"}
		}
	}
	return nil
}

func (e *claudeEvents) handleBlock(block any) error {
	if block == nil {
		return errNullBlock
	}
	blockType := jsjson.Path(block, "type")
	if blockText := jsjson.Path(block, "text"); blockType == "text" && jsjson.Truthy(blockText) {
		s, isString := blockText.(string)
		if !isString {
			return &jsError{class: "TypeError", message: "block.text.slice(...).replace is not a function"}
		}
		e.log.printf("message: %s", strings.ReplaceAll(jsstr.Slice(s, 0, 160), "\n", " "))
		return nil
	}
	if blockType == "tool_use" {
		name := jsjson.TemplateStringOf(jsjson.Lookup(block, "name"))
		e.countTool(name)
		e.log.printf("tool: %s%s", name, toolDetail(block))
	}
	return nil
}

// toolDetail is the text after the tool name: the command (first 100 UTF-16 units), else the
// file path, else nothing.
func toolDetail(block any) string {
	if command := jsjson.Path(block, "input", "command"); jsjson.Truthy(command) {
		return " " + jsstr.Slice(text(command), 0, 100)
	}
	if file := jsjson.Path(block, "input", "file_path"); jsjson.Truthy(file) {
		return " " + text(file)
	}
	return ""
}

// objectPrototypeMethods maps the names Object.prototype holds a method under to the name in
// that method's source text, which `tools_used[name] || 0` reads for a tool not yet counted.
var objectPrototypeMethods = map[string]string{
	"constructor": "Object", "__defineGetter__": "__defineGetter__", "__defineSetter__": "__defineSetter__",
	"hasOwnProperty": "hasOwnProperty", "__lookupGetter__": "__lookupGetter__", "__lookupSetter__": "__lookupSetter__",
	"isPrototypeOf": "isPrototypeOf", "propertyIsEnumerable": "propertyIsEnumerable", "toString": "toString",
	"valueOf": "valueOf", "toLocaleString": "toLocaleString",
}

// countTool is `tools_used[name] = (tools_used[name] || 0) + 1` on a plain object: "__proto__"
// reads Object.prototype and ignores the string assigned to it, and the other inherited names
// read a method, so its source text gets "1" appended, as does every later count of it.
func (e *claudeEvents) countTool(name string) {
	if name == "__proto__" {
		return
	}
	tools := jsjson.Path(e.result, "tools_used").(*jsjson.Object)
	current, own := tools.Get(name)
	if inherited, isMethod := objectPrototypeMethods[name]; !own && isMethod {
		current = "function " + inherited + "() { [native code] }"
	}
	switch count := current.(type) {
	case float64:
		tools.Set(name, count+1)
	case string:
		tools.Set(name, count+"1")
	default:
		tools.Set(name, 1.0)
	}
}

// handleResult copies the result event (l.69-71); several result events overwrite each other
// and accumulate errors.
func (e *claudeEvents) handleResult(event *jsjson.Object) error {
	isError := jsjson.Truthy(jsjson.Path(event, "is_error"))
	r := e.result
	r.Set("ok", !isError)
	r.Set("final_message", or(jsjson.Path(event, "result"), ""))
	r.Set("usage", or(jsjson.Path(event, "usage"), nil))
	r.Set("model_usage", or(jsjson.Path(event, "modelUsage"), nil))
	r.Set("cost_usd", jsjson.Path(event, "total_cost_usd")) // ?? null: absent and null are both nil
	r.Set("num_turns", jsjson.Path(event, "num_turns"))
	r.Set("permission_denials", or(jsjson.Path(event, "permission_denials"), []any{}))
	r.Set("structured_output", jsjson.Path(event, "structured_output"))
	if !isError {
		return nil
	}
	if subtype := jsjson.Path(event, "subtype"); jsjson.Truthy(subtype) && subtype != "success" {
		e.pushError(subtype)
		return nil
	}
	// (ev.result || "error").slice(0, 200): strings and arrays have a slice method. A cut inside
	// a surrogate pair keeps U+FFFD where the JS keeps a lone surrogate (PE-1).
	switch message := or(jsjson.Path(event, "result"), "error").(type) {
	case string:
		e.pushError(jsstr.Slice(message, 0, 200))
	case []any:
		e.pushError(append([]any{}, message[:min(len(message), 200)]...))
	default:
		return &jsError{class: "TypeError", message: `(ev.result || "error").slice is not a function`}
	}
	return nil
}

func (e *claudeEvents) pushError(message any) {
	errors := jsjson.Path(e.result, "errors").([]any)
	e.result.Set("errors", append(errors, message))
}

// or is `v || fallback`.
func or(v, fallback any) any {
	if jsjson.Truthy(v) {
		return v
	}
	return fallback
}

// stderrTail is `stderr.trim().split("\n").slice(-3).join(" | ")`: the last three lines of
// claude's stderr, trimmed of JS white space. The JS decodes each chunk on its own, so a
// character split across chunks becomes U+FFFD there; the whole buffer is decoded here.
func stderrTail(stderr []byte) string {
	lines := strings.Split(strings.Trim(jsstr.DecodeUTF8(stderr), jsstr.WhiteSpace), "\n")
	return strings.Join(lines[max(len(lines)-3, 0):], " | ")
}

var (
	// limitText is LIMIT_TEXT (l.79) and limitOrRate the incident kind test (l.89), both /i. A
	// non-unicode JS /i matches ASCII letters only against ASCII letters, so the input is
	// lowered in ASCII only (see asciiLower) and the patterns are lower case.
	limitText   = regexp.MustCompile(`hit your (session|usage|weekly) limit|usage limit reached|rate limit`)
	limitOrRate = regexp.MustCompile(`limit|rate`)
	fable       = regexp.MustCompile(`fable`)
)

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
