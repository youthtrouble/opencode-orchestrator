package supervisor

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// serve runs Serve with an injected Sleep that records each wait and creates the stop file at
// the stopAfter-th call; between, at runs before each wait returns.
type serveRun struct {
	sleeps []time.Duration
	code   int
	err    error
	stderr string
}

func (f *fixture) serve(stopAfter int, between func(call int)) serveRun {
	f.t.Helper()
	var run serveRun
	f.sup.Sleep = func(d time.Duration) {
		run.sleeps = append(run.sleeps, d)
		if between != nil {
			between(len(run.sleeps))
		}
		if len(run.sleeps) == stopAfter {
			f.write("supervisor.stop", "")
		}
		if len(run.sleeps) > stopAfter {
			f.t.Fatalf("serve did not stop after %d waits", stopAfter)
		}
	}
	var stderr bytes.Buffer
	run.code, run.err = f.sup.Serve(&stderr)
	run.stderr = stderr.String()
	return run
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Stat(f.path(rel))
	return err == nil
}

func TestServeTicksOnceThenStops(t *testing.T) {
	f := newFixture(t)
	got := f.serve(1, nil)
	if got.code != 0 || got.err != nil || got.stderr != "" {
		t.Fatalf("serve = %d, %v, %q", got.code, got.err, got.stderr)
	}
	if len(got.sleeps) != 1 || got.sleeps[0] != 5*time.Second {
		t.Errorf("waits %v, want one of 5s", got.sleeps)
	}
	want := `{
  "events": {},
  "lanes": {},
  "checked_at": "2027-01-15T08:00:00.000Z",
  "pid": 4242,
  "status": "stopped",
  "stopped_at": "2027-01-15T08:00:00.000Z"
}
`
	if state := f.read("supervisor-state.json"); state != want {
		t.Errorf("state\n%s\nwant\n%s", state, want)
	}
	if f.exists("supervisor.lock") || !f.exists("supervisor.stop") {
		t.Error("serve must release its lock and leave the stop file")
	}
}

func TestServeStopFileAtTheFirstLoopMeansNoTick(t *testing.T) {
	f := newFixture(t)
	f.write("supervisor.stop", "")
	f.at(0.5)
	got := f.serve(0, nil)
	if got.code != 0 || got.err != nil || len(got.sleeps) != 0 {
		t.Fatalf("serve = %d, %v, waits %v", got.code, got.err, got.sleeps)
	}
	want := "{\n  \"status\": \"stopped\",\n  \"stopped_at\": \"2027-01-15T08:00:00.500Z\"\n}\n"
	if state := f.read("supervisor-state.json"); state != want {
		t.Errorf("state %s", state)
	}
}

// A failed tick writes nothing; the next successful write persists its storage_error once,
// then it is deleted in memory, while storage_failures keeps counting across restarts and a
// stale stopped_at keeps its position.
func TestServeStorageErrorLifecycle(t *testing.T) {
	f := newFixture(t)
	f.write("supervisor-state.json", `{"status": "stopped", "stopped_at": "old", "storage_failures": 2}`)
	f.write("workers", "not a directory") // Lanes fails: ENOTDIR
	var persisted string
	got := f.serve(2, func(call int) {
		switch call {
		case 1:
			if err := os.Remove(f.path("workers")); err != nil {
				t.Fatal(err)
			}
		case 2:
			persisted = f.read("supervisor-state.json")
		}
	})
	if got.code != 0 || got.err != nil {
		t.Fatalf("serve = %d, %v", got.code, got.err)
	}
	if !strings.Contains(persisted, `"storage_error": "ENOTDIR"`) || !strings.Contains(persisted, `"storage_failures": 3`) {
		t.Errorf("second tick wrote\n%s", persisted)
	}
	want := `{
  "status": "stopped",
  "stopped_at": "2027-01-15T08:00:00.000Z",
  "storage_failures": 3,
  "events": {},
  "lanes": {},
  "checked_at": "2027-01-15T08:00:00.000Z",
  "pid": 4242
}
`
	if state := f.read("supervisor-state.json"); state != want {
		t.Errorf("state\n%s\nwant\n%s", state, want)
	}
}

func TestServeLockClaim(t *testing.T) {
	t.Run("a live holder refuses, writing nothing", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.lock", " 77\n")
		f.live[77] = true
		got := f.serve(0, nil)
		if got.code != 1 || got.err != nil || got.stderr != "a supervisor already owns this project\n" {
			t.Fatalf("serve = %d, %v, %q", got.code, got.err, got.stderr)
		}
		if f.read("supervisor.lock") != " 77\n" || f.exists("supervisor-state.json") {
			t.Error("a refused serve wrote")
		}
	})
	t.Run("a stale holder is overwritten", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor.lock", "77")
		f.sup.Sleep = func(time.Duration) { t.Fatal("ticked") }
		f.write("supervisor.stop", "")
		lockDuringServe := ""
		f.sup.Now = func() time.Time { lockDuringServe = f.read("supervisor.lock"); return f.now }
		if code, err := f.sup.Serve(&bytes.Buffer{}); code != 0 || err != nil {
			t.Fatalf("serve = %d, %v", code, err)
		}
		if lockDuringServe != "4242" {
			t.Errorf("lock while serving %q, want this pid", lockDuringServe)
		}
	})
	t.Run("a directory lock is the error", func(t *testing.T) {
		f := newFixture(t)
		f.mkdir("supervisor.lock")
		got := f.serve(0, nil)
		if got.code != 1 {
			t.Errorf("code %d", got.code)
		}
		// PE-21: a stale lock is replaced through a private temp, so the rename fails.
		wantRenameOntoDirError(t, got.err, f.path("supervisor.lock"))
	})
}

// The lock is owner-only under a permissive umask, whether created or taken over (PE-21).
func TestServeLockIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	for name, stale := range map[string]bool{"created": false, "taken over": true} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if stale {
				f.write("supervisor.lock", "77") // 0644 under this umask
			}
			f.write("supervisor.stop", "")
			// Serve removes the lock when it stops, so the mode is sampled inside the injected
			// Now, which stopServing calls (for stopped_at) before it unlinks the lock.
			mode := os.FileMode(0)
			f.sup.Now = func() time.Time {
				if info, err := os.Stat(f.path("supervisor.lock")); err == nil {
					mode = info.Mode().Perm()
				}
				return f.now
			}
			if code, err := f.sup.Serve(&bytes.Buffer{}); code != 0 || err != nil {
				t.Fatalf("serve = %d, %v", code, err)
			}
			if mode != 0o600 {
				t.Errorf("lock mode while serving %v, want 0600", mode)
			}
		})
	}
}

// A state that is no object fails where the JS assigns to it, leaving the lock; an array is
// read as {} (PE-16).
func TestServeCrashesOnAStateThatIsNoObject(t *testing.T) {
	for _, tc := range []struct {
		state string
		stop  bool
		want  string
	}{
		{"null", false, "Cannot set properties of null (setting 'storage_error')"},
		{"null", true, "Cannot set properties of null (setting 'status')"},
		{"5", false, "Cannot create property 'storage_error' on number '5'"},
		{`"x"`, true, "Cannot create property 'status' on string 'x'"},
		{"true", false, "Cannot create property 'storage_error' on boolean 'true'"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			f := newFixture(t)
			f.write("supervisor-state.json", tc.state)
			if tc.stop {
				f.write("supervisor.stop", "")
			}
			got := f.serve(1, nil)
			if got.code != 1 || len(got.sleeps) != 0 {
				t.Errorf("code %d, waits %v", got.code, got.sleeps)
			}
			wantError(t, got.err, tc.want)
			if f.read("supervisor-state.json") != tc.state || f.read("supervisor.lock") != "4242" {
				t.Error("a crashed serve must write nothing and keep its lock")
			}
		})
	}
	t.Run("an array is {}", func(t *testing.T) {
		f := newFixture(t)
		f.write("supervisor-state.json", "[]")
		f.write("supervisor.stop", "")
		if got := f.serve(0, nil); got.code != 0 || got.err != nil {
			t.Fatalf("serve = %d, %v", got.code, got.err)
		}
		if state := f.read("supervisor-state.json"); !strings.HasPrefix(state, "{\n  \"status\": \"stopped\"") {
			t.Errorf("state %s", state)
		}
	})
}

func TestPlusOneIsJSAddition(t *testing.T) {
	for _, tc := range []struct {
		in, want any
	}{
		{nil, 1.0}, {0.0, 1.0}, {"", 1.0}, {false, 1.0}, {2.0, 3.0}, {true, 2.0}, {"2", "21"}, {[]any{}, "1"},
	} {
		if got := plusOne(tc.in); got != tc.want {
			t.Errorf("plusOne(%#v) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
