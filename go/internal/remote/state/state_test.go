package state

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestEnsureCreatesAMachineIDOnceAndKeepsIt(t *testing.T) {
	file := File(t.TempDir())
	first, err := Ensure(file)
	if err != nil {
		t.Fatal(err)
	}
	if !uuidV4.MatchString(first.MachineID) {
		t.Errorf("machine id %q is not a lowercase UUID v4", first.MachineID)
	}
	for path, want := range map[string]os.FileMode{filepath.Dir(file): 0o700, file: 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want %04o", path, info.Mode().Perm(), err, want)
		}
	}
	again, err := Ensure(file)
	if err != nil || again.MachineID != first.MachineID {
		t.Errorf("Ensure again = %q, %v; want the kept id %q", again.MachineID, err, first.MachineID)
	}
}

func TestEnsureReportsACorruptFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(file, []byte(`{"machine_id":"x","extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var corrupt *CorruptError
	if _, err := Ensure(file); !errors.As(err, &corrupt) {
		t.Errorf("Ensure = %v, want a CorruptError", err)
	}
}

func TestDirSitsBesideTheUsageCache(t *testing.T) {
	if got := Dir("/home/u"); got != "/home/u/.local/state/opencode/orchestrator/remote" {
		t.Errorf("Dir = %s", got)
	}
}
