package bridge

import (
	"fmt"
	"regexp"
	"strings"
	"time"

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

// claudeArgv is the argv claude-exec.mjs passes to claude (l.38-49). Node's spawn coerces the
// value true to "true". --json-schema passes the schema file's contents; --add-dir an absolute
// path. A bare --json-schema or --add-dir is the path.resolve TypeError, and an unreadable
// schema file Node's fs error, both uncaught in the JS.
func claudeArgv(opts options, model any, prompt string) ([]string, error) {
	argv := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", text(opts.Or("permission-mode", "auto")), "--permission-prompts", "none"}
	if resume := opts.JS("resume"); jsjson.Truthy(resume) {
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
		if v := opts.JS(flag.key); jsjson.Truthy(v) {
			argv = append(argv, flag.arg, text(v))
		}
	}
	if schema := opts.JS("json-schema"); jsjson.Truthy(schema) {
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
	if dir := opts.JS("add-dir"); jsjson.Truthy(dir) {
		abs, err := resolvePath(dir)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--add-dir", abs)
	}
	return append(argv, "--", prompt), nil
}

// startLine is the "start" log line (l.52). Its mode falls back to "acceptEdits" although
// claude is passed "auto": a latent JS defect, mirrored.
func startLine(opts options, model any, cwd string) string {
	shown := model
	if jsjson.Truthy(opts.JS("resume")) {
		shown = "(resumed)"
	}
	effort := ""
	if v := opts.JS("effort"); jsjson.Truthy(v) {
		effort = " effort=" + text(v)
	}
	return fmt.Sprintf("start model=%s%s mode=%s cwd=%s", text(shown), effort, text(opts.Or("permission-mode", "acceptEdits")), cwd)
}

// newClaudeResult is the result object with its JS key order (l.57); exit_code, duration_ms
// and then record (or reason and resets_at) are appended when the run ends.
func newClaudeResult(opts options, model any, cwd, startedAt string) *jsjson.Object {
	result := jsjson.NewObject()
	result.Set("ok", false)
	result.Set("harness", "claude")
	result.Set("session_id", opts.Or("resume", nil))
	result.Set("model", model)
	result.Set("effort", opts.Or("effort", nil))
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
// as the JS line handler does (l.59-73), on the jsThread that serializes them.
type claudeEvents struct {
	*jsThread
	result *jsjson.Object
}

func newClaudeEvents(log *logger, result *jsjson.Object) *claudeEvents {
	e := &claudeEvents{result: result}
	e.jsThread = newJSThread(log, e)
	return e
}

// handleClose is the child's "close" handler (l.74-78).
func (e *claudeEvents) handleClose(exitCode any, stderr []byte, duration time.Duration) {
	e.result.Set("exit_code", exitCode)
	e.result.Set("duration_ms", float64(duration.Milliseconds()))
	if exitCode == 0.0 {
		return
	}
	if len(jsjson.Path(e.result, "errors").([]any)) == 0 {
		e.pushError(fmt.Sprintf("claude exited %s: %s", text(exitCode), stderrTail(stderr)))
	}
	e.result.Set("ok", false)
}

var (
	errNullEvent = nullPropertyRead("session_id")
	errNullBlock = nullPropertyRead("type")
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

// limitText is LIMIT_TEXT (l.79), /i like limitOrRate, and fable the frontier test (l.35).
var (
	limitText = regexp.MustCompile(`hit your (session|usage|weekly) limit|usage limit reached|rate limit`)
	fable     = regexp.MustCompile(`fable`)
)
