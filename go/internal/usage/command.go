package usage

import (
	"errors"
	"fmt"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

var (
	// errStdoutMaxBuffer is execFileSync's ENOBUFS error, with Node's message.
	errStdoutMaxBuffer = errors.New("stdout maxBuffer length exceeded")
	// errTimedOut is execFileSync's ETIMEDOUT error code.
	errTimedOut = errors.New("ETIMEDOUT")
)

// runQuiet mirrors execFileSync(name, args, {encoding: "utf8", timeout, stdio: ["ignore",
// "pipe", "ignore"]}): the program is looked up on PATH as Node looks it up, stdin and stderr
// are discarded, stdout is decoded as UTF-8 (U+FFFD for invalid bytes), and any failure (not
// found, non-zero exit, death by a signal, timeout, more than Node's 1 MiB maxBuffer of stdout)
// is an error. It runs through procs.RunSync, so a timeout, an overflow or a SIGINT, SIGTERM or
// SIGHUP sent to orch stops the child's whole process group: SIGTERM (or the signal), SIGKILL a
// second later should the child still run, and once it has exited, SIGKILL for what is left of
// the group (PE-20, PE-22).
func runQuiet(name string, args []string, timeout time.Duration) (string, error) {
	r := procs.RunSync(procs.SyncSpec{Name: name, Args: args, Timeout: timeout, Grace: time.Second, DiscardStderr: true})
	switch {
	case r.Err != nil:
		return "", r.Err
	case r.Overflowed:
		return "", errStdoutMaxBuffer
	case r.TimedOut:
		return "", fmt.Errorf("spawnSync %s %w", name, errTimedOut)
	case r.Status == nil:
		return "", fmt.Errorf("%s was killed by a signal", name)
	case *r.Status != 0:
		return "", fmt.Errorf("%s exited with status %d", name, *r.Status)
	}
	return r.Stdout, nil
}
