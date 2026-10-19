package procs

import (
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
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil) // still our child: reap it
	})
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

func TestStartDetachedReportsAStartFailure(t *testing.T) {
	if err := StartDetached(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("StartDetached of a missing program succeeded")
	}
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
