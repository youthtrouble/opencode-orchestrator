package usage

import (
	"errors"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// TestRunQuietFailsOnStdoutPastNodeMaxBuffer: execFileSync throws ENOBUFS once stdout passes
// 1 MiB, and the child is stopped rather than read forever (PE-20).
func TestRunQuietFailsOnStdoutPastNodeMaxBuffer(t *testing.T) {
	started := time.Now()
	out, err := runQuiet("/bin/sh", []string{"-c", "exec cat /dev/zero"}, time.Minute)
	if !errors.Is(err, errStdoutMaxBuffer) || out != "" {
		t.Errorf("runQuiet = %d bytes, %v; want the maxBuffer error", len(out), err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("runQuiet took %v", elapsed)
	}
}

func TestRunQuietReturnsExactlyNodeMaxBuffer(t *testing.T) {
	out, err := runQuiet("/bin/sh", []string{"-c", "head -c 1048576 /dev/zero"}, time.Minute)
	if err != nil || len(out) != procs.NodeMaxBuffer {
		t.Errorf("runQuiet = %d bytes, %v", len(out), err)
	}
}

// TestRunQuietCountsOnlyStdout: stderr is ignored, as with stdio ["ignore", "pipe", "ignore"].
func TestRunQuietCountsOnlyStdout(t *testing.T) {
	out, err := runQuiet("/bin/sh", []string{"-c", "head -c 2000000 /dev/zero >&2; echo ok"}, time.Minute)
	if err != nil || out != "ok\n" {
		t.Errorf("runQuiet = %q, %v", out, err)
	}
}
