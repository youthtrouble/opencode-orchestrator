// Package cli holds one function per orch subcommand. Each parses its argv, writes to the
// given streams and returns the process exit code; the domain logic lives in other packages.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/manifest"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/policy"
)

// Stdio are the streams a subcommand reads and writes. In is a file because prompt reading
// must know whether stdin is a terminal, as Node's process.stdin.isTTY does.
type Stdio struct {
	In  *os.File
	Out io.Writer
	Err io.Writer
}

// Command is the signature every subcommand implements.
type Command func(args []string, stdio Stdio) int

// decodeArgv decodes each argument as Node decodes process.argv: malformed UTF-8 becomes U+FFFD.
func decodeArgv(args []string) []string {
	decoded := make([]string, len(args))
	for i, arg := range args {
		decoded[i] = jsstr.DecodeUTF8([]byte(arg))
	}
	return decoded
}

// crash reports what the JS leaves uncaught: Node prints the error class and message on a
// line of its own amid a stack trace and exits 1. orch prints that line alone.
func crash(stdio Stdio, err error) int {
	fmt.Fprintf(stdio.Err, "%s: %v\n", jsErrorClass(err), err)
	return 1
}

// jsTypeError is a TypeError thrown by the JS script itself or by a Node API it calls, with
// V8's or Node's message; code is Node's error code, if any.
type jsTypeError struct {
	code    string
	message string
}

func (e *jsTypeError) Error() string { return e.message }

// Node's ERR_INVALID_ARG_TYPE for a path argument that is not a string.
var (
	// errPathArgUndefined: path.resolve(undefined).
	errPathArgUndefined = &jsTypeError{"ERR_INVALID_ARG_TYPE", `The "paths[0]" argument must be of type string. Received undefined`}
	// errPathArgBoolean: path.basename(true).
	errPathArgBoolean = &jsTypeError{"ERR_INVALID_ARG_TYPE", `The "path" argument must be of type string. Received type boolean (true)`}
	// errFileArgBoolean: fs.writeFileSync(true, ...).
	errFileArgBoolean = &jsTypeError{"ERR_INVALID_ARG_TYPE", `The "path" argument must be of type string or an instance of Buffer or URL. Received type boolean (true)`}
)

// jsErrorClass names the class the JS error would have: a TypeError for a value of the wrong
// shape, the TypeError (with Node's code) the script raises, a SyntaxError for unparsable
// JSON, otherwise Error.
func jsErrorClass(err error) string {
	var mailboxShape *mailbox.ShapeError
	var manifestShape *manifest.ShapeError
	var incidentShape *incident.ShapeError
	var policyType *policy.TypeError
	var flipProperty *flipcmd.PropertyTypeError
	var typeError *jsTypeError
	var syntax *jsonSyntaxError
	switch {
	case errors.As(err, &mailboxShape), errors.As(err, &manifestShape), errors.As(err, &incidentShape), errors.As(err, &policyType),
		errors.As(err, &flipProperty):
		return "TypeError"
	case errors.As(err, &typeError):
		if typeError.code != "" {
			return "TypeError [" + typeError.code + "]"
		}
		return "TypeError"
	case errors.As(err, &syntax):
		return "SyntaxError"
	}
	return "Error"
}

func printLine(stdio Stdio, value *jsjson.Object) int {
	data, err := jsjson.Marshal(value)
	if err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}
