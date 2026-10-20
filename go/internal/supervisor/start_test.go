package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// starter records the detached launch instead of making it; no test starts a daemon.
type starter struct {
	live      map[float64]bool
	keepAwake bool
	pid       int
	err       error
	launched  []procs.Detached
	logOpen   bool // the log descriptor was open when Detach got it
}

func (s *starter) Starter() Starter {
	return Starter{
		Self:  "/bin/orch",
		Alive: func(pid any) bool { n, _ := pid.(float64); return s.live[n] },
		KeepAwake: func(cmd string, args []string) (string, []string) {
			if s.keepAwake {
				return "caffeinate", append([]string{"-i", "-s", cmd}, args...)
			}
			return cmd, args
		},
		Detach: func(d procs.Detached) (int, error) {
			s.launched = append(s.launched, d)
			_, statErr := d.Output.Stat()
			s.logOpen = statErr == nil
			return s.pid, s.err
		},
	}
}

func startIn(t *testing.T, s *starter, prepare func(p Project)) (Project, string, error) {
	t.Helper()
	p := NewProject(t.TempDir())
	if prepare != nil {
		if err := os.MkdirAll(filepath.Join(p.Dir, "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
		prepare(p)
	}
	out, err := p.Start(s.Starter())
	if err != nil {
		return p, "", err
	}
	return p, compact(t, out), nil
}

func TestStartLaunchesServeDetached(t *testing.T) {
	for _, keepAwake := range []bool{false, true} {
		s := &starter{pid: 321, keepAwake: keepAwake}
		p, out, err := startIn(t, s, func(p Project) {
			mustWrite(t, p.StopPath, "")
			mustWrite(t, p.LogPath, "earlier\n")
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"started":true,"supervisor_pid":321,"log":"` + p.LogPath + `"}`; out != want {
			t.Errorf("out %s, want %s", out, want)
		}
		args := []string{"supervisor", "serve", "--cwd", p.Root}
		name := "/bin/orch"
		if keepAwake {
			name, args = "caffeinate", append([]string{"-i", "-s", "/bin/orch"}, args...)
		}
		if len(s.launched) != 1 || s.launched[0].Name != name || !reflect.DeepEqual(s.launched[0].Args, args) || s.launched[0].Dir != p.Root || !s.logOpen {
			t.Errorf("launched %+v (log open %v)", s.launched, s.logOpen)
		}
		if _, err := os.Stat(p.StopPath); !os.IsNotExist(err) {
			t.Error("start must remove the stop file")
		}
		if data, _ := os.ReadFile(p.LogPath); string(data) != "earlier\n" {
			t.Errorf("log truncated: %q", data)
		}
	}
}

func TestStartAppendsToTheLog(t *testing.T) {
	p := NewProject(t.TempDir())
	st := (&starter{}).Starter()
	st.Detach = func(d procs.Detached) (int, error) {
		_, err := d.Output.WriteString("serve output\n")
		return 1, err
	}
	if err := os.MkdirAll(filepath.Dir(p.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, p.LogPath, "earlier\n")
	if _, err := p.Start(st); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p.LogPath); string(data) != "earlier\nserve output\n" {
		t.Errorf("log %q", data)
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	s := &starter{live: map[float64]bool{77: true}}
	_, out, err := startIn(t, s, func(p Project) {
		mustWrite(t, p.LockPath, " 77\n")
		mustWrite(t, p.StopPath, "")
	})
	if err != nil || out != `{"started":false,"reason":"already running","pid":77}` {
		t.Errorf("out %s, %v", out, err)
	}
	if len(s.launched) != 0 {
		t.Error("launched over a live lock")
	}
}

func TestStartOverAStaleLockLaunches(t *testing.T) {
	s := &starter{pid: 5}
	_, out, err := startIn(t, s, func(p Project) { mustWrite(t, p.LockPath, "77") })
	if err != nil || len(s.launched) != 1 {
		t.Errorf("out %s, %v, launched %d", out, err, len(s.launched))
	}
}

func TestStartThatCannotLaunchLeavesThePidOut(t *testing.T) {
	s := &starter{err: &procs.StartError{Code: "EACCES"}}
	p, out, err := startIn(t, s, nil)
	if err != nil || out != `{"started":true,"log":"`+p.LogPath+`"}` {
		t.Errorf("out %s, %v", out, err)
	}
	s = &starter{err: errors.New("bad descriptor")}
	if _, _, err := startIn(t, s, nil); err == nil {
		t.Error("a synchronous spawn failure must be the error")
	}
}

func TestStartLogDirectoryErrors(t *testing.T) {
	s := &starter{}
	_, _, err := startIn(t, s, func(p Project) {
		if err := os.Mkdir(p.LogPath, 0o755); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || len(s.launched) != 0 {
		t.Fatalf("a directory log: %v, launched %d", err, len(s.launched))
	}
	p := NewProject(t.TempDir())
	mustWrite(t, p.Dir, "a file")
	if _, err := p.Start(s.Starter()); err == nil {
		t.Error(".orchestrator a file must fail the mkdir")
	}
}

// The log holds what serve and the conductors print: owner-only under a permissive umask (PE-21).
func TestStartLogIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	p, _, err := startIn(t, &starter{pid: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{p.LogPath: 0o600, filepath.Dir(p.LogPath): 0o700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode %v, want %v", path, got, want)
		}
	}
}

// A symlink planted at the log is refused, not followed (PE-21: the JS appends to its target).
func TestStartRefusesASymlinkedLog(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere")
	mustWrite(t, target, "untouched\n")
	s := &starter{}
	_, _, err := startIn(t, s, func(p Project) {
		if err := os.Symlink(target, p.LogPath); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.HasPrefix(err.Error(), "ELOOP: ") || len(s.launched) != 0 {
		t.Fatalf("a symlinked log: %v, launched %d", err, len(s.launched))
	}
	if data, _ := os.ReadFile(target); string(data) != "untouched\n" {
		t.Errorf("symlink target %q", data)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
