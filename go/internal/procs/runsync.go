package procs

import (
	"bytes"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// SyncSpec is a spawnSync call: Name resolved on PATH as Start resolves it, run with Args in
// Dir (the working directory when "") with the parent's environment and /dev/null as stdin.
// A positive Timeout is spawnSync's timeout option. Grace (positive) bounds the wait before
// SIGKILL once the timeout's SIGTERM was sent and the wait for pipes still held by
// grandchildren once the child has exited; spawnSync waits forever for both (PE-10 ext.).
type SyncSpec struct {
	Name    string
	Args    []string
	Dir     string
	Timeout time.Duration
	Grace   time.Duration
}

// SyncResult is what spawnSync returns. Status is the exit status, nil when the child was
// killed by a signal or could not be started (JS null). Stdout and Stderr are decoded as
// UTF-8 ("" when the child could not be started, where spawnSync gives undefined). TimedOut
// is spawnSync's ETIMEDOUT error: the timeout fired, whatever the child then did (a child
// that handles SIGTERM keeps its exit status, as in Node). Err is the *StartError of a child
// that could not be started, or a failure to collect its status.
type SyncResult struct {
	Status         *int
	Stdout, Stderr string
	TimedOut       bool
	Err            error
}

// RunSync runs the child to completion as spawnSync does.
func RunSync(s SyncSpec) SyncResult {
	var stdout, stderr bytes.Buffer
	child, err := Start(Spec{Name: s.Name, Args: s.Args, Dir: s.Dir, Stdout: &stdout, Stderr: &stderr, Grace: s.Grace})
	if err != nil {
		return SyncResult{Err: err}
	}
	timeout := startTimeout(child, s.Timeout)
	status, err := child.Wait()
	return SyncResult{
		Status:   status,
		Stdout:   jsstr.DecodeUTF8(stdout.Bytes()),
		Stderr:   jsstr.DecodeUTF8(stderr.Bytes()),
		TimedOut: timeout.stop(),
		Err:      err,
	}
}

// timeout sends SIGTERM to the child once its deadline passes (Child.Signal then arms the
// SIGKILL escalation).
type timeout struct {
	timer *time.Timer
	done  chan struct{} // closed once the deadline's function has run
	fired bool          // the deadline's SIGTERM reached the child; read after done
}

func startTimeout(child *Child, after time.Duration) *timeout {
	t := &timeout{done: make(chan struct{})}
	if after > 0 {
		t.timer = time.AfterFunc(after, func() {
			defer close(t.done)
			t.fired = child.Signal(syscall.SIGTERM)
		})
	}
	return t
}

// stop disarms the deadline and reports whether it signalled the child. A deadline that
// passes after the child has been reaped sends nothing and is no timeout; one that is running
// when stop is called is waited for, so its outcome is known.
func (t *timeout) stop() bool {
	if t.timer == nil || t.timer.Stop() {
		return false
	}
	<-t.done
	return t.fired
}
