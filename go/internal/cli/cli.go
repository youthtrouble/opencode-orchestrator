// Package cli holds one function per orch subcommand. Each parses its argv, writes to the
// given streams and returns the process exit code; the domain logic lives in other packages.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/manifest"
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

// jsErrorClass names the class the JS error would have: a TypeError for a value of the wrong
// shape, Node's coded TypeError for a missing path argument, a SyntaxError for unparsable
// JSON, otherwise Error.
func jsErrorClass(err error) string {
	var mailboxShape *mailbox.ShapeError
	var manifestShape *manifest.ShapeError
	var syntax *jsonSyntaxError
	switch {
	case errors.As(err, &mailboxShape), errors.As(err, &manifestShape):
		return "TypeError"
	case errors.Is(err, errPathArgUndefined):
		return "TypeError [ERR_INVALID_ARG_TYPE]"
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
