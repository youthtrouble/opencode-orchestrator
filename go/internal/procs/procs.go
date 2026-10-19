// Package procs starts and stops the child processes of the bridges (orch claude-exec runs
// claude through it) the way the JS bridges do with node:child_process and node:readline:
//
//   - Start resolves the command on PATH as Node's spawn (libuv's execvp) does, including its
//     EACCES rule and its "spawn <name> <CODE>" error message.
//   - Wait reports the exit code as the "close" event does: nil (JS null) for a child killed by
//     a signal, never Go's -1.
//   - Lines splits a stream into lines as readline does.
//   - StartDetached starts a fire-and-forget child in a session of its own (the bridge's
//     post-run usage refresh, PE-12).
//
// One difference is deliberate (PE-B3 in the S5a plan): the JS sends one signal and waits for
// "close" forever. Here a signalled child that has not exited after Spec.Grace is killed, and
// once the child exits, Wait gives a grandchild that still holds stdin, stdout or stderr at
// most Spec.Grace before it closes the pipes. The child stays in the caller's process group (no
// Setpgid, no Setsid), so a signal to the caller's group still reaches it.
package procs

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Spec describes a child. Name is resolved on the PATH in Env as Node's spawn does; a nil Env
// means the parent's environment, as Node's env: undefined. Stdin, when set, is copied to the
// child's stdin, which is then closed (Node's child.stdin.end(data)); nil gives it /dev/null
// (Node's "ignore"). A child that exits without reading all of it is not an error: os/exec
// drops the EPIPE of that copy. Stdout and Stderr receive the child's output (nil discards
// it). Grace bounds both the wait before SIGKILL after Signal and the wait for the stdin copy
// and pipe EOF after the child exits; it must be positive.
type Spec struct {
	Name           string
	Args           []string
	Dir            string
	Env            []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Grace          time.Duration
}

// Child is a started child process. Wait must be called exactly once; Signal may be called
// from any goroutine, before or after Wait.
type Child struct {
	cmd   *exec.Cmd
	grace time.Duration

	mu        sync.Mutex
	killTimer *time.Timer // armed by the first Signal; nil until then
	reaped    bool        // set once Wait has returned
}

// Start starts the child. A failure to find or execute the command is a *StartError.
func Start(s Spec) (*Child, error) {
	if s.Grace <= 0 {
		return nil, errors.New("procs: Spec.Grace must be positive")
	}
	env := s.Env
	if env == nil {
		// Set explicitly: exec.Cmd rewrites PWD for a nil Env with a Dir, and Node does not.
		env = os.Environ()
	}
	path, err := resolve(s.Name, s.Dir, env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{
		Path:      path,
		Args:      append([]string{s.Name}, s.Args...),
		Dir:       s.Dir,
		Env:       env,
		Stdin:     s.Stdin,
		Stdout:    s.Stdout,
		Stderr:    s.Stderr,
		WaitDelay: s.Grace,
	}
	if err := cmd.Start(); err != nil {
		return nil, startError(s.Name, err)
	}
	return &Child{cmd: cmd, grace: s.Grace}, nil
}

// Signal sends sig to the child (its pid only, not its group). If the child has not exited
// Grace after the first Signal, it is sent SIGKILL. Later calls send their signal but do not
// re-arm or extend that single timer. After the child has been reaped, Signal does nothing.
func (c *Child) Signal(sig syscall.Signal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reaped {
		return
	}
	// The only failure for our own child is that it has already exited (os.ErrProcessDone),
	// which is what the escalation is waiting for anyway.
	_ = c.cmd.Process.Signal(sig)
	if c.killTimer == nil {
		c.killTimer = time.AfterFunc(c.grace, c.kill)
	}
}

// kill is the escalation. os.Process refuses to signal a process it has reaped, so a pid the
// kernel may have reused is never hit; an already-exited child makes Kill a no-op.
func (c *Child) kill() {
	_ = c.cmd.Process.Kill()
}

// Wait waits for the child to exit and returns its exit code: a normal exit n gives &n, a
// death by signal gives nil (the JS close code null). Once the child has exited, Wait waits
// at most Grace for the copies to and from its stdin, stdout and stderr to finish, then closes
// the pipes.
//
// err is non-nil only when the child's status could not be collected, or when copying Stdin
// or its output to Stdout or Stderr failed (EPIPE on stdin excepted). exec.ErrWaitDelay (a
// grandchild held a pipe past Grace) is not an error here: the exit status is complete, the
// bound is a resource policy, and exec reports it only for a zero exit anyway.
func (c *Child) Wait() (code *int, err error) {
	err = c.cmd.Wait()
	c.mu.Lock()
	c.reaped = true
	if c.killTimer != nil {
		c.killTimer.Stop()
	}
	c.mu.Unlock()
	return closeCode(c.cmd.ProcessState), waitError(err)
}

// closeCode maps a wait status to Node's close code: the exit status, or nil when the child
// was terminated by a signal (or never reaped).
func closeCode(state *os.ProcessState) *int {
	if state == nil {
		return nil
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() {
		return nil
	}
	code := status.ExitStatus()
	return &code
}

// waitError drops the errors that closeCode already accounts for (a non-zero exit or a
// signal) and the bounded pipe wait.
func waitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) || errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}
