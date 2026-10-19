package supervisor

import (
	"errors"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// channelTimeout is spawnSync's timeout for the codex-queue and command channels.
const channelTimeout = 30 * time.Second

// errArgvNotIterable is V8's TypeError for `[...channel.argv.slice(1)]` on an argv that is
// neither an array nor a string (probed with Node v26.5.1).
var errArgvNotIterable = &jsjson.TypeError{Message: "channel.argv.slice is not a function or its return value is not iterable"}

// deliverChannel is the JS deliver(channel, text): wake the coordinator through one channel
// and return {status} or {status, error}. Everything it does sits in one try, so any throw
// is a delivery_failed whose error is `err.code || String(err)`.
func (s *Supervisor) deliverChannel(channel any, text string) *jsjson.Object {
	result, err := s.sendTo(channel, text)
	if err != nil {
		return delivery("delivery_failed", errorCode(err))
	}
	return result
}

func (s *Supervisor) sendTo(channel any, text string) (*jsjson.Object, error) {
	switch jsjson.Path(channel, "type") {
	case "codex-queue":
		return s.codexQueue(channel, text)
	case "opencode-run":
		return s.opencodeRun(channel, text)
	case "claude-resume":
		return s.claudeResume(channel, text)
	case "command":
		return s.command(channel, text)
	}
	return unknownChannel(channel, text), nil
}

// codexQueue queues the text on a codex thread and waits up to channelTimeout. A timeout is
// uncertain (codex may have queued it); a start failure or a signal is `exit null`.
func (s *Supervisor) codexQueue(channel any, text string) (*jsjson.Object, error) {
	args, err := spawnArgs("queue", "--thread", property(channel, "thread"), "--message", text)
	if err != nil {
		return nil, err
	}
	r := s.RunSync(SyncSpec{Name: "codex", Args: args, Dir: s.Root, Timeout: channelTimeout})
	switch {
	case exitedZero(r):
		return delivery("queued", ""), nil
	case r.TimedOut:
		return delivery("delivery_uncertain", "timeout"), nil
	}
	return exitFailure(r), nil
}

// opencodeRun steers the coordinator's opencode session without waiting for its reply. A
// spawn failure is ignored (PE-17: the JS has no 'error' listener and dies after the tick).
func (s *Supervisor) opencodeRun(channel any, text string) (*jsjson.Object, error) {
	args, err := spawnArgs("run", "--session", property(channel, "session"), "--format", "json", text)
	if err != nil {
		return nil, err
	}
	_ = s.Spawn("opencode", args, s.Root)
	return delivery("queued", ""), nil
}

// claudeResume resumes the coordinator's Claude session through `orch claude-exec`, without
// waiting. The JS spawns its own node binary, which does not fail to start; a failure here
// is ignored the same way as opencodeRun's.
func (s *Supervisor) claudeResume(channel any, text string) (*jsjson.Object, error) {
	permissionMode := jsjson.Path(channel, "permission_mode")
	if !jsjson.Truthy(permissionMode) {
		permissionMode = "auto"
	}
	args, err := spawnArgs("claude-exec", "--cwd", s.Root, "--resume", property(channel, "session"),
		"--permission-mode", permissionMode, "--quiet", "--prompt", text)
	if err != nil {
		return nil, err
	}
	_ = s.Spawn(s.Self, args, s.Root)
	return delivery("queued", ""), nil
}

// command runs `argv[0]` with `[...argv.slice(1), text]` and waits up to channelTimeout; a
// timeout is a failure (`exit null`), unlike codexQueue's.
func (s *Supervisor) command(channel any, text string) (*jsjson.Object, error) {
	file, rest, err := splitArgv(jsjson.Lookup(channel, "argv"))
	if err != nil {
		return nil, err
	}
	name, err := spawnFile(file)
	if err != nil {
		return nil, err
	}
	args, err := spawnArgs(append(rest, text)...)
	if err != nil {
		return nil, err
	}
	r := s.RunSync(SyncSpec{Name: name, Args: args, Dir: s.Root, Timeout: channelTimeout})
	if exitedZero(r) {
		return delivery("queued", ""), nil
	}
	return exitFailure(r), nil
}

// splitArgv is `channel.argv[0]` and `[...channel.argv.slice(1)]` for any JSON value: an
// array by its elements, a string by its UTF-16 units (argv[0]) and code points (the rest).
func splitArgv(argv any, present bool) (first any, rest []any, err error) {
	switch v := argv.(type) {
	case nil:
		if present {
			return nil, nil, jsjson.CannotReadOfNull("0")
		}
		return nil, nil, &jsjson.TypeError{Message: "Cannot read properties of undefined (reading '0')"}
	case []any:
		if len(v) == 0 {
			return nil, nil, nil
		}
		return v[0], v[1:], nil
	case string:
		if v == "" {
			return nil, nil, nil
		}
		for _, r := range jsstr.Slice(v, 1, jsstr.Len(v)) {
			rest = append(rest, string(r))
		}
		return jsstr.Slice(v, 0, 1), rest, nil
	}
	return nil, nil, errArgvNotIterable
}

// argError is the TypeError Node's spawn and spawnSync throw for an invalid file or argument;
// only its code reaches the state (`err.code || String(err)`).
type argError struct {
	Code string
}

func (e *argError) Error() string { return "TypeError [" + e.Code + "]" }

var (
	errFileNotString = &argError{"ERR_INVALID_ARG_TYPE"}
	errArgValue      = &argError{"ERR_INVALID_ARG_VALUE"}
)

// spawnFile is Node's check of spawn's file argument: a string, not empty, without NUL.
func spawnFile(file any) (string, error) {
	name, isString := file.(string)
	switch {
	case !isString:
		return "", errFileNotString
	case name == "" || strings.Contains(name, "\x00"):
		return "", errArgValue
	}
	return name, nil
}

// spawnArgs is spawn's args as Node passes them: a string holding NUL is rejected, any other
// value is String(v), which the C argv then ends at its first NUL (probed: ["a\0"] as an
// argument passes "a"). Go's exec would reject the NUL instead, so it is cut here.
func spawnArgs(values ...any) ([]string, error) {
	args := make([]string, len(values))
	for i, value := range values {
		if text, isString := value.(string); isString && strings.Contains(text, "\x00") {
			return nil, errArgValue
		}
		args[i], _, _ = strings.Cut(argText(value), "\x00")
	}
	return args, nil
}

// undefinedValue is a JS undefined passed as a spawn argument, which String() prints as
// "undefined" (JSON values cannot hold it; nil is null).
type undefinedValue struct{}

// property is `v[key]` for a spawn argument: the value, or undefinedValue when absent.
func property(v any, key string) any {
	if value, present := jsjson.Lookup(v, key); present {
		return value
	}
	return undefinedValue{}
}

func argText(value any) string {
	if _, isUndefined := value.(undefinedValue); isUndefined {
		return "undefined"
	}
	return jsjson.TemplateString(value)
}

// errorCode is `err.code || String(err)` for every error a tick or a delivery throws.
func errorCode(err error) string {
	var arg *argError
	if errors.As(err, &arg) {
		return arg.Code
	}
	return nodefs.ErrorCode(err)
}

func exitedZero(r SyncResult) bool {
	return r.Status != nil && *r.Status == 0
}

// exitFailure is `{status: "delivery_failed", error: `exit ${r.status}`}`.
func exitFailure(r SyncResult) *jsjson.Object {
	var status any // null
	if r.Status != nil {
		status = float64(*r.Status)
	}
	return delivery("delivery_failed", "exit "+jsjson.TemplateString(status))
}

// unknownChannel is deliver's answer for a channel type it does not know.
func unknownChannel(channel any, _ string) *jsjson.Object {
	return delivery("delivery_failed", "unknown channel "+jsjson.TemplateStringOf(jsjson.Lookup(channel, "type")))
}

// delivery is the result object: {status}, or {status, error} when errorText is not "".
func delivery(status, errorText string) *jsjson.Object {
	result := jsjson.NewObject()
	result.Set("status", status)
	if errorText != "" {
		result.Set("error", errorText)
	}
	return result
}
