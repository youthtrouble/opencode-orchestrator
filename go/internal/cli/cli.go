// Package cli holds one function per orch subcommand. Each parses its argv, writes to the
// given streams and returns the process exit code; the domain logic lives in other packages.
package cli

import (
	"io"
	"os"
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
