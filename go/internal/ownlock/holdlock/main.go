// Command holdlock is a test fixture for test processes that cannot take POSIX record locks
// (Node has no fcntl): it holds the ownlock of its one argument with its own pid, prints
// "locked" and keeps the lock until its stdin closes, then releases it (CA2).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: holdlock <path>")
		os.Exit(2)
	}
	lock, err := ownlock.Acquire(os.Args[1], os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
