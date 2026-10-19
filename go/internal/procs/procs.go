// Package procs starts and stops the child processes of the bridges (orch claude-exec runs
// claude through it) the way the JS bridges do with node:child_process and node:readline:
//
//   - Start resolves the command on PATH as Node's spawn (libuv's execvp) does, including its
//     EACCES rule and its "spawn <name> <CODE>" error message.
//   - Wait reports the exit code as the "close" event does: nil (JS null) for a child killed by
//     a signal, never Go's -1.
//   - Lines splits a stream into lines as readline does.
//   - RunSync runs a child to completion as spawnSync does, with its timeout option.
//   - StartDetached starts a fire-and-forget child in a session of its own (the bridge's
//     post-run usage refresh, PE-12); Detached.StartReaped does the same for a long-lived
//     caller, reaping the child when it exits.
//
// One difference is deliberate (PE-B3 in the S5a plan): the JS sends one signal and waits for
// "close" forever. Here a signalled child that has not exited after Spec.Grace is killed, and
// once the child exits, Wait gives a grandchild that still holds stdin, stdout or stderr at
// most Spec.Grace before it closes the pipes (PE-10). The child stays in the caller's process
// group unless Spec.NewSession is set, so a signal to the caller's group still reaches it.
//
// The pipes to a child are this package's own, not os/exec's: exec.Cmd.Wait reaps the child
// and then waits for its pipes, so a Child built on it would learn of the reap only after the
// drain and could signal a pid or process group that is no longer its own meanwhile.
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
// group does not reach it.
type Spec struct {
	Name           string
	Args           []string
	Dir            string
	Env            []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Grace          time.Duration
	NewSession     bool
}

// Child is a started child process. Wait must be called exactly once; Signal and SignalGroup
// may be called from any goroutine, before or after Wait.
type Child struct {
	cmd   *exec.Cmd
	grace time.Duration

	stdio *pipes

	mu        sync.Mutex
	killTimer *time.Timer // armed by the first Signal; nil until then
	reaped    bool        // set by Wait as soon as the child has been reaped
}

// Start starts the child. A failure to find or execute the command is a *StartError; a
// failure to create the pipes to it is returned as os.Pipe reports it.
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
		Path: path,
		Args: append([]string{s.Name}, s.Args...),
		Dir:  s.Dir,
		Env:  env,
	}
	if s.NewSession {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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
	return &Child{cmd: cmd, grace: s.Grace, stdio: stdio}, nil
}

// Signal sends sig to the child (its pid only, not its group). If the child has not exited
// Grace after the first Signal, it is sent SIGKILL. Later calls send their signal but do not
// re-arm or extend that single timer. After the child has been reaped, Signal does nothing.
// sent reports whether sig reached the child, which it cannot once the child is reaped.
func (c *Child) Signal(sig syscall.Signal) (sent bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reaped {
		return false
	}
	// The only failure for our own child is that it has already exited (os.ErrProcessDone),
	// which is what the escalation is waiting for anyway.
	err := c.cmd.Process.Signal(sig)
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

// kill is the escalation. os.Process refuses to signal a process it has reaped, so a pid the
// kernel may have reused is never hit; an already-exited child makes Kill a no-op.
func (c *Child) kill() {
	_ = c.cmd.Process.Kill()
}

// Wait waits for the child to exit and returns its exit code: a normal exit n gives &n, a
// death by signal gives nil (the JS close code null). The child counts as reaped, so Signal
// and SignalGroup send nothing, from the moment its exit status is collected; only then does
// Wait give the copies to and from its stdin, stdout and stderr at most Grace to finish
// (PE-10) before it closes the pipes. What remains is the window between wait4 returning and
// the flag being set, short but not bounded, since exec.Cmd.Wait does its own bookkeeping
// after the reap: a signal sent in it finds the pid just freed (pids are handed out in
// sequence, so one is rarely reused that soon) or the child's process group, whose id is not
// reused while the group has members.
//
// err is non-nil only when the child's status could not be collected, or when a child that
// exited 0 had copying Stdin or its output to Stdout or Stderr fail (EPIPE on stdin
// excepted), as os/exec reports it. Pipes closed after Grace are not an error: the exit
// status is complete and the bound is a resource policy (os/exec's ErrWaitDelay rule).
func (c *Child) Wait() (code *int, err error) {
	// Every descriptor the child got is an *os.File, so exec copies nothing and returns at the
	// reap.
	err = c.cmd.Wait()
	c.mu.Lock()
	c.reaped = true
	if c.killTimer != nil {
		c.killTimer.Stop()
	}
	c.mu.Unlock()
	if drainErr := c.stdio.drain(c.grace); err == nil {
		err = drainErr
	}
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
