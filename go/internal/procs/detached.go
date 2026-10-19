package procs

import (
	"os"
	"os/exec"
	"syscall"
)

// Detached is a child started and left to run on its own, as Node's spawn with detached: true,
// stdio ["ignore", fd, fd] and unref() leaves it: Name is resolved on PATH as Start resolves
// it, the child runs in Dir (the working directory when "") in a session and process group of
// its own, its stdin is /dev/null and its stdout and stderr are Output (/dev/null when nil).
// Output is handed to the child as its descriptor, with no copying in this process, so the
// child keeps writing to it after the caller exits.
type Detached struct {
	Name   string
	Args   []string
	Dir    string
	Output *os.File
}

// Start starts the child and returns its pid without waiting for it. A failure to find or
// execute Name is a *StartError. Nothing here reaps the child: it is meant for a caller that
// exits soon after, leaving it to init.
func (d Detached) Start() (int, error) {
	cmd, err := d.start()
	if err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid // read first: Release invalidates it
	// Release only frees this process's handle on the child; whatever it reports, the child
	// is running, which is what the caller must learn.
	_ = cmd.Process.Release()
	return pid, nil
}

// StartReaped is Start for a long-lived caller: a goroutine waits for the child, so it does
// not stay a zombie once it exits (Node's event loop reaps an unref'd child the same way).
// A failure to find or execute Name is returned at once, as a *StartError.
func (d Detached) StartReaped() (int, error) {
	cmd, err := d.start()
	if err != nil {
		return 0, err
	}
	// The exit status of a fire-and-forget child is not wanted; only the reaping is.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

func (d Detached) start() (*exec.Cmd, error) {
	// Set explicitly: exec.Cmd rewrites PWD for a nil Env with a Dir, and Node does not.
	env := os.Environ()
	path, err := resolve(d.Name, d.Dir, env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{
		Path:        path,
		Args:        append([]string{d.Name}, d.Args...),
		Dir:         d.Dir,
		Env:         env,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if d.Output != nil { // a nil *os.File in the io.Writer fields would not mean /dev/null
		cmd.Stdout, cmd.Stderr = d.Output, d.Output
	}
	if err := cmd.Start(); err != nil {
		return nil, startError(d.Name, err)
	}
	return cmd, nil
}

// StartDetached starts the program at path with args as a Detached child with its output on
// /dev/null (the bridges' post-run usage refresh, PE-12). Unlike a Start child it is outside
// the caller's process group, so a signal to that group does not reach it, and it keeps
// running when the caller exits.
func StartDetached(path string, args ...string) error {
	_, err := Detached{Name: path, Args: args}.Start()
	return err
}
