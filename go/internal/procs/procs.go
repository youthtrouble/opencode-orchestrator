// Package procs starts and stops the child processes of the bridges (orch claude-exec runs
// claude through it) the way the JS bridges do with node:child_process and node:readline:
//
//   - Start resolves the command on PATH as Node's spawn (libuv's execvp) does, including its
//     EACCES rule and its "spawn <name> <CODE>" error message.
//   - Wait reports the exit code as the "close" event does: nil (JS null) for a child killed by
//     a signal, never Go's -1.
//   - Lines splits a stream into lines as readline does.
//   - RunSync runs a child to completion as spawnSync does, with its timeout option; a
//     SIGINT, SIGTERM or SIGHUP that orch receives meanwhile stops the child first and then
//     ends orch.
//   - StartDetached starts a fire-and-forget child in a session of its own (the bridge's
//     post-run usage refresh, PE-12); Detached.StartReaped does the same for a long-lived
//     caller, reaping the child when it exits.
//
// One difference is deliberate (PE-B3 in the S5a plan): the JS sends one signal and waits for
// "close" forever. Here a signalled child that has not exited after Spec.Grace is killed, and
// once the child exits, Wait gives a grandchild that still holds stdin, stdout or stderr at
// most Spec.Grace before it closes the pipes (PE-10). The child stays in the caller's process
// group unless Spec.NewSession or Spec.OwnGroup is set, so a signal to the caller's group
// still reaches it.
//
// A second difference is PE-22: the JS signals a child's pid only, so the tools a cancelled
// CLI started keep running. A Spec.OwnGroup child is cancelled with its whole process group,
// and once it has exited, what is left of the group is SIGKILLed.
//
// Wait watches for the exit without reaping (waitid with WNOWAIT on Linux, kqueue's NOTE_EXIT
// on darwin), so it does that last kill, the reap and the bookkeeping that stops every later
// signal in one step, while the exited child still holds its pid and so its group id; no
// signal from this package can then reach a pid or a group id that the kernel has handed to
// another process. Elsewhere, or should the watch fail, Wait reaps first and kills after, which
// leaves the short window that reapThenCleanUp describes. The pipes to a child are this
// package's own, not os/exec's, for the same reason: exec.Cmd.Wait reaps the child and then
// waits for its pipes, so a Child built on it would learn of the reap only after the drain and
// could signal a pid or process group that is no longer its own meanwhile.
package procs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Spec describes a child. Name is resolved on the PATH in Env as Node's spawn does; a nil Env
// means the parent's environment, as Node's env: undefined. Stdin, when set, is copied to the
// child's stdin, which is then closed (Node's child.stdin.end(data)); nil gives it /dev/null
// (Node's "ignore"). A child that exits without reading all of it is not an error: the EPIPE
// of that copy is dropped, as os/exec drops it. An *os.File (a terminal, say) is handed to
// the child as its descriptor, with no copying in this process; a typed-nil *os.File passes
// through the same way, so the child gets that descriptor closed, as with os/exec. Stdout and
// Stderr receive the child's output (nil discards it); one writer given as both shares one
// pipe and one copy, as os/exec shares it. Grace bounds both the wait before SIGKILL after
// Signal and the wait for the stdin copy and pipe EOF after the child exits; it must be
// positive. Closing a pipe cannot interrupt the caller's own Stdin Reader or Stdout/Stderr
// Writer, so one whose Read or Write blocks keeps Wait waiting past Grace, as with os/exec.
// NewSession starts the child in a session and process group of its own (setsid, Node's spawn
// option detached: true), so SignalGroup reaches its descendants and a signal to the caller's
// group does not reach it. OwnGroup starts it in a process group of its own in the caller's
// session (setpgid), so Signal and its escalation reach the child's descendants too (PE-22);
// a signal to the caller's group does not reach it either. The two are mutually exclusive.
type Spec struct {
	Name           string
	Args           []string
	Dir            string
	Env            []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Grace          time.Duration
	NewSession     bool
	OwnGroup       bool
}

// Child is a started child process. Wait must be called exactly once; Signal and SignalGroup
// may be called from any goroutine, before or after Wait.
type Child struct {
	cmd      *exec.Cmd
	grace    time.Duration
	ownGroup bool // the child leads a process group of its own (Spec.OwnGroup)

	stdio *pipes

	mu        sync.Mutex
	killTimer *time.Timer // armed by the first Signal; nil until then
	reaped    bool        // set by Wait as soon as the child has been reaped
	cancelled bool        // Signal was called before the reap
}

// Start starts the child. A failure to find or execute the command is a *StartError; a
// failure to create the pipes to it is returned as os.Pipe reports it.
func Start(s Spec) (*Child, error) {
	if s.Grace <= 0 {
		return nil, errors.New("procs: Spec.Grace must be positive")
	}
	if s.NewSession && s.OwnGroup {
		return nil, errors.New("procs: Spec.NewSession and Spec.OwnGroup are mutually exclusive")
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
		Path: path,
		Args: append([]string{s.Name}, s.Args...),
		Dir:  s.Dir,
		Env:  env,
	}
	switch {
	case s.NewSession:
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	case s.OwnGroup:
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	stdio := &pipes{}
	if err := stdio.attach(cmd, s); err != nil {
		stdio.closeAll()
		return nil, err
	}
	err = cmd.Start()
	closeFiles(stdio.childEnds) // the child holds its own copies now, or never started
	if err != nil {
		closeFiles(stdio.parentEnds)
		return nil, startError(s.Name, err)
	}
	stdio.startCopies()
	return &Child{cmd: cmd, grace: s.Grace, ownGroup: s.OwnGroup, stdio: stdio}, nil
}

// Signal sends sig to the child: to its pid only, or to its whole process group for an
// OwnGroup child (PE-22). If the child has not exited Grace after the first Signal, it (or its
// group) is sent SIGKILL. Later calls send their signal but do not re-arm or extend that single
// timer. After the child has been reaped, Signal does nothing. sent reports whether sig reached
// the child, which it cannot once the child is reaped.
func (c *Child) Signal(sig syscall.Signal) (sent bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reaped {
		return false
	}
	c.cancelled = true
	// The only failure for our own child is that it has already exited (os.ErrProcessDone, or
	// ESRCH for a group whose members are all gone), which is what the escalation is waiting
	// for anyway.
	err := c.send(sig)
	if c.killTimer == nil {
		c.killTimer = time.AfterFunc(c.grace, c.kill)
	}
	return err == nil
}

// Pid is the child's process id.
func (c *Child) Pid() int {
	return c.cmd.Process.Pid
}

// SignalGroup sends sig to the child's process group, as `process.kill(-child.pid, sig)` does,
// or, when that fails (the child leads no group), to the child alone, as the JS fallback
// `child.kill(sig)` does; that second failure is ignored, as child.kill returns false rather
// than throw. Unlike Signal it never escalates to SIGKILL. After the child has been reaped it
// does nothing, so a pid the kernel may have reused is never hit.
func (c *Child) SignalGroup(sig syscall.Signal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reaped {
		return
	}
	if err := syscall.Kill(-c.cmd.Process.Pid, sig); err != nil {
		_ = c.cmd.Process.Signal(sig)
	}
}

// send delivers sig to the child, or to its group for an OwnGroup child, falling back to the
// child alone if the group is gone (as SignalGroup does). It must be called with mu held and
// the child not yet reaped.
func (c *Child) send(sig syscall.Signal) error {
	if c.ownGroup {
		if err := syscall.Kill(-c.cmd.Process.Pid, sig); !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return c.cmd.Process.Signal(sig)
}

// kill is the escalation. It sends nothing once Wait has marked the child reaped, and
// os.Process refuses to signal a process it has reaped, so a pid the kernel may have reused is
// never hit; an already-exited child makes the kill a no-op.
func (c *Child) kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reaped {
		_ = c.send(syscall.SIGKILL)
	}
}

// Wait waits for the child to exit and returns its exit code: a normal exit n gives &n, a
// death by signal gives nil (the JS close code null). Once a cancelled OwnGroup child has
// exited, what is left of its process group is SIGKILLed, so descendants that ignored or
// outlived the cancellation are gone before the caller reports the run over (PE-22); that also
// frees pipes they held, so the drain does not wait out Grace for them. Then the child counts
// as reaped (Signal, SignalGroup and the escalation send nothing), and only then does Wait give
// the copies to and from its stdin, stdout and stderr at most Grace to finish (PE-10) before
// it closes the pipes.
//
// err is non-nil only when the child's status could not be collected, or when a child that
// exited 0 had copying Stdin or its output to Stdout or Stderr fail (EPIPE on stdin
// excepted), as os/exec reports it. Pipes closed after Grace are not an error: the exit
// status is complete and the bound is a resource policy (os/exec's ErrWaitDelay rule).
func (c *Child) Wait() (code *int, err error) {
	if waitExited(c.Pid()) == nil {
		err = c.cleanUpThenReap()
	} else {
		err = c.reapThenCleanUp()
	}
	if drainErr := c.stdio.drain(c.grace); err == nil {
		err = drainErr
	}
	return closeCode(c.cmd.ProcessState), waitError(err)
}

// cleanUpThenReap finishes a child that has exited but is not yet reaped. The exited child
// still holds its pid, and with it its process group id, so the leftover kill reaches only
// what is left of its own group; and since the kill, the reap and the reaped flag share one
// hold of mu, no signal can be sent between the reap and the flag.
func (c *Child) cleanUpThenReap() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ownGroup && c.cancelled {
		c.killLeftovers()
	}
	// Every descriptor the child got is an *os.File, so exec copies nothing and returns at the
	// reap, which an exited child allows at once.
	err := c.cmd.Wait()
	c.markReaped()
	return err
}

// reapThenCleanUp is the fallback where the exit cannot be watched without reaping (a system
// other than darwin and Linux, or a watch that failed). A signal sent between wait4 returning
// and the flag being set (short but not bounded, since exec.Cmd.Wait does its own bookkeeping
// after the reap) finds the pid just freed, which pids handed out in sequence make unlikely to
// be reused that soon, or the child's process group, whose id is not reused while the group has
// members. The leftover kill comes after the reap on the same terms: with members left the id
// is still the group's, and with none, reuse since the reap would need the pids to wrap around
// and a new process to lead a group with that id.
func (c *Child) reapThenCleanUp() error {
	err := c.cmd.Wait()
	c.mu.Lock()
	c.markReaped()
	cancelled := c.cancelled
	c.mu.Unlock()
	if c.ownGroup && cancelled {
		c.killLeftovers()
	}
	return err
}

// markReaped records the reap and disarms the escalation. It must be called with mu held.
func (c *Child) markReaped() {
	c.reaped = true
	if c.killTimer != nil {
		c.killTimer.Stop()
	}
}

// killLeftovers SIGKILLs the process group of a cancelled OwnGroup child (PE-22). ESRCH (no
// member left) is the expected result and there is nothing else a caller could do about a
// failure, so the error is dropped.
func (c *Child) killLeftovers() {
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
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
// signal).
func waitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

// pipes connects a child's stdin, stdout and stderr to Spec readers and writers that are not
// *os.File, with an os.Pipe each and a copy in this process. exec.Cmd sees only *os.File (or
// nil, /dev/null), so it starts no copy of its own and its Wait returns at the reap.
type pipes struct {
	childEnds  []*os.File     // handed to the child; closed here once it has started
	parentEnds []*os.File     // closed by their copy, or by drain once Grace has passed
	copies     []func() error // started once the child runs
	done       chan error     // one result per copy
}

// attach gives cmd the child's side of s.Stdin, s.Stdout and s.Stderr. A Stderr that is the
// Stdout writer shares its pipe, as os/exec shares it, so one copy writes to it.
func (p *pipes) attach(cmd *exec.Cmd, s Spec) error {
	var err error
	if cmd.Stdin, err = p.input(s.Stdin); err != nil {
		return err
	}
	if cmd.Stdout, err = p.output(s.Stdout); err != nil {
		return err
	}
	if s.Stderr != nil && sameWriter(s.Stderr, s.Stdout) {
		cmd.Stderr = cmd.Stdout
		return nil
	}
	cmd.Stderr, err = p.output(s.Stderr)
	return err
}

func (p *pipes) input(r io.Reader) (io.Reader, error) {
	if _, isFile := r.(*os.File); isFile || r == nil {
		return r, nil
	}
	childEnd, parentEnd, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.add(childEnd, parentEnd, func() error { return copyStdin(parentEnd, r) })
	return childEnd, nil
}

func (p *pipes) output(w io.Writer) (io.Writer, error) {
	if _, isFile := w.(*os.File); isFile || w == nil {
		return w, nil
	}
	parentEnd, childEnd, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.add(childEnd, parentEnd, func() error { return copyOutput(w, parentEnd) })
	return childEnd, nil
}

func (p *pipes) add(childEnd, parentEnd *os.File, copyFn func() error) {
	p.childEnds = append(p.childEnds, childEnd)
	p.parentEnds = append(p.parentEnds, parentEnd)
	p.copies = append(p.copies, copyFn)
}

func (p *pipes) startCopies() {
	p.done = make(chan error, len(p.copies))
	for _, copyFn := range p.copies {
		go func() { p.done <- copyFn() }()
	}
}

// drain waits at most grace for the copies and returns the first error among them. When grace
// passes first, it closes the parent's ends, which ends every copy not blocked in the caller's
// own writer, waits for them and reports nothing: errors after that forced close are its
// echoes (os.ErrClosed), and os/exec drops them the same way.
func (p *pipes) drain(grace time.Duration) error {
	if len(p.copies) == 0 {
		return nil
	}
	expired := time.NewTimer(grace)
	defer expired.Stop()
	var first error
	for pending := len(p.copies); pending > 0; pending-- {
		select {
		case err := <-p.done:
			if first == nil {
				first = err
			}
		case <-expired.C:
			closeFiles(p.parentEnds)
			for ; pending > 0; pending-- {
				<-p.done
			}
			return nil
		}
	}
	return first
}

func (p *pipes) closeAll() {
	closeFiles(p.childEnds)
	closeFiles(p.parentEnds)
}

// copyStdin writes r to the child's stdin and then closes it, as Node's stdin.end(data) does.
// A child that exits without reading all of r makes the write fail with EPIPE, which is not
// an error (PE-13).
func copyStdin(parentEnd *os.File, r io.Reader) error {
	_, err := io.Copy(parentEnd, r)
	if isBrokenPipeWrite(err, parentEnd) {
		err = nil
	}
	if closeErr := parentEnd.Close(); err == nil {
		err = closeErr
	}
	return err
}

func copyOutput(w io.Writer, parentEnd *os.File) error {
	_, err := io.Copy(w, parentEnd)
	// A read end is only ever read here; closing it cannot lose data, so its error is moot.
	_ = parentEnd.Close()
	return err
}

// isBrokenPipeWrite reports whether err is a write to pipe that failed because its reader is
// gone, and not, say, a failure of the reader io.Copy took the bytes from.
func isBrokenPipeWrite(err error, pipe *os.File) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr) && pathErr.Op == "write" && pathErr.Path == pipe.Name() &&
		errors.Is(pathErr.Err, syscall.EPIPE)
}

// sameWriter is os/exec's interfaceEqual: whether a and b are the same writer, false for
// writers whose type cannot be compared.
func sameWriter(a, b io.Writer) (same bool) {
	// == panics for two values of one uncomparable type; same is then left false.
	defer func() { _ = recover() }()
	return a == b
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		// Every file here is a pipe end of this package's own; a close of one that a copy has
		// closed already (os.ErrClosed) is expected, and no other close error loses data.
		_ = f.Close()
	}
}
