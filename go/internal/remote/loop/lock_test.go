package loop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestASecondLockIsRefusedWithTheHoldersPid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.lock")
	held, err := AcquireLock(path, 4242)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireLock(path, 5353)
	var busy *LockHeldError
	if !errors.As(err, &busy) || busy.Pid != 4242 || err.Error() != "another orch remote serve is running (pid 4242)" {
		t.Fatalf("second lock = %v, want held by 4242", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireLock(path, 5353)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	defer again.Release()
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != "5353" {
		t.Errorf("lock file = %q, want the new holder's pid", data)
	}
}

func TestALockFileLeftByADeadServeIsTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.lock")
	if err := os.WriteFile(path, []byte("99999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(path, 4242)
	if err != nil {
		t.Fatalf("stale lock: %v", err)
	}
	defer lock.Release()
	if data, _ := os.ReadFile(path); string(data) != "4242\n" {
		t.Errorf("lock file = %q, want only the new pid", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("lock mode = %v, want 0600", info.Mode().Perm())
	}
}
