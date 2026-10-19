package procs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStartDetached: the child runs in a session (so a process group) of its own with its
// stdio on /dev/null, and StartDetached returns while it is still running.
func TestStartDetached(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report")
	child := script(t, `stdio=other
if [ /dev/fd/0 -ef /dev/null ] && [ /dev/fd/1 -ef /dev/null ] && [ /dev/fd/2 -ef /dev/null ]; then stdio=devnull; fi
echo "$$ $stdio" > "$1.tmp" && mv "$1.tmp" "$1"
exec sleep 30`)
	started := time.Now()
	if err := StartDetached(child, report); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > slack {
		t.Errorf("StartDetached took %v; it must not wait for the child", elapsed)
	}
	fields := strings.Fields(waitForFile(t, report))
	if len(fields) != 2 {
		t.Fatalf("child reported %q", fields)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killAndReap(pid) })
	// Setsid makes the child a session and process group leader; Getsid is not in Linux's syscall.
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid || pgid == syscall.Getpgrp() {
		t.Errorf("child process group %d (%v), want its own group %d", pgid, err, pid)
	}
	if fields[1] != "devnull" {
		t.Error("child stdio is not /dev/null")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("child is not running after StartDetached returned: %v", err)
	}
}

// TestDetachedWritesBothStreamsToOutput: the child leads a session of its own, its stdin is
// /dev/null, its stdout and stderr are the Output descriptor itself (it keeps writing there
// with nothing in this process copying), and Start returns its pid while it runs.
func TestDetachedWritesBothStreamsToOutput(t *testing.T) {
	dir := t.TempDir()
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	report := filepath.Join(dir, "report")
	child := script(t, `stdin=other
if [ /dev/fd/0 -ef /dev/null ]; then stdin=devnull; fi
echo "out $PWD"
echo "err $stdin" >&2
echo "$$" > "$1.tmp" && mv "$1.tmp" "$1"
exec sleep 30`)
	started := time.Now()
	pid, err := Detached{Name: child, Args: []string{report}, Dir: dir, Output: output}.Start()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > slack {
		t.Errorf("Start took %v; it must not wait for the child", elapsed)
	}
	t.Cleanup(func() { killAndReap(pid) })
	if reported := strings.TrimSpace(waitForFile(t, report)); reported != strconv.Itoa(pid) {
		t.Fatalf("Start returned pid %d, the child is %s", pid, reported)
	}
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		t.Errorf("child process group %d (%v), want its own group %d", pgid, err, pid)
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "out "+physicalDir+"\nerr devnull\n"; got != want && got != "out "+dir+"\nerr devnull\n" {
		t.Errorf("output = %q, want %q", got, want)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("child is not running after Start returned: %v", err)
	}
}

// TestDetachedStartErrors: a name found nowhere on PATH and a file that may not be executed
// fail as Node's spawn reports them.
func TestDetachedStartErrors(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, c := range []struct{ name, want string }{
		{"absent-command", "spawn absent-command ENOENT"},
		{"plain", "spawn plain EACCES"},
		{plain, "spawn " + plain + " EACCES"},
	} {
		_, err := Detached{Name: c.name}.Start()
		var startErr *StartError
		if !errors.As(err, &startErr) || err.Error() != c.want {
			t.Errorf("Start(%s) = %v, want *StartError %q", c.name, err, c.want)
		}
	}
}

func TestStartDetachedReportsAStartFailure(t *testing.T) {
	if err := StartDetached(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("StartDetached of a missing program succeeded")
	}
}

// killAndReap kills a detached test child, still a child of this process, and reaps it.
func killAndReap(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
	var status syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &status, 0, nil)
}

// waitForFile returns the content of path once it exists, failing after slack.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(slack)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within %v: %v", path, slack, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
