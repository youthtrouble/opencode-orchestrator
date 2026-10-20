package usage

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// errStdoutMaxBuffer is execFileSync's ENOBUFS error, with Node's message.
var errStdoutMaxBuffer = errors.New("stdout maxBuffer length exceeded")

// runQuiet mirrors execFileSync(name, args, {encoding: "utf8", timeout, stdio: ["ignore",
// "pipe", "ignore"]}): the program is looked up on PATH, stdin and stderr are discarded, and
// any failure (not found, non-zero exit, timeout, more than Node's 1 MiB maxBuffer of stdout)
// is an error. On timeout or overflow the child gets SIGTERM, as Node sends (PE-20).
func runQuiet(name string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// A child that ignores SIGTERM, or a grandchild holding stdout open, must not outlive the
	// timeout or the overflow by more than this.
	cmd.WaitDelay = time.Second
	limit := procs.NewOutputLimit(procs.NodeMaxBuffer)
	stdout := limit.Stream()
	cmd.Stdout = stdout
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() {
		select {
		case <-limit.Exceeded():
			cancel()
		case <-ctx.Done():
		}
	}()
	err := cmd.Wait()
	if limit.Overflowed() {
		return "", errStdoutMaxBuffer
	}
	if err != nil {
		return "", err
	}
	return string(stdout.Bytes()), nil
}
