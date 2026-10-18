package usage

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

// runQuiet mirrors execFileSync(name, args, {encoding: "utf8", timeout, stdio: ["ignore",
// "pipe", "ignore"]}): the program is looked up on PATH, stdin and stderr are discarded, and
// any failure (not found, non-zero exit, timeout) is an error. On timeout the child gets
// SIGTERM, as Node sends.
func runQuiet(name string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// A child that ignores SIGTERM, or a grandchild holding stdout open, must not outlive the
	// timeout by more than this.
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}
