package keepawake

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	args := []string{"worker", "start", "--lane", "w"}
	caffeinate := append([]string{"-i", "-s", "/bin/orch"}, args...)
	inhibit := append([]string{"--what=idle:sleep", "--why=opencode-orchestrator", "/bin/orch"}, args...)
	for _, c := range []struct {
		goos     string
		present  bool
		enabled  bool
		wantCmd  string
		wantArgs []string
		wantSeen []string // the names looked up
	}{
		{"darwin", true, true, "caffeinate", caffeinate, []string{"caffeinate"}},
		{"darwin", false, true, "/bin/orch", args, []string{"caffeinate"}},
		{"darwin", true, false, "/bin/orch", args, nil},
		{"darwin", false, false, "/bin/orch", args, nil},
		{"linux", true, true, "systemd-inhibit", inhibit, []string{"systemd-inhibit"}},
		{"linux", false, true, "/bin/orch", args, []string{"systemd-inhibit"}},
		{"linux", true, false, "/bin/orch", args, nil},
		{"linux", false, false, "/bin/orch", args, nil},
		{"freebsd", true, true, "/bin/orch", args, nil},
		{"freebsd", false, true, "/bin/orch", args, nil},
		{"freebsd", true, false, "/bin/orch", args, nil},
		{"freebsd", false, false, "/bin/orch", args, nil},
	} {
		var seen []string
		lookup := func(name string) bool { seen = append(seen, name); return c.present }
		cmd, got := wrap(c.goos, lookup, "/bin/orch", args, c.enabled)
		if cmd != c.wantCmd || !slices.Equal(got, c.wantArgs) || !slices.Equal(seen, c.wantSeen) {
			t.Errorf("wrap(%s, found=%v, enabled=%v) = %s %q after looking up %q; want %s %q after %q",
				c.goos, c.present, c.enabled, cmd, got, seen, c.wantCmd, c.wantArgs, c.wantSeen)
		}
	}
}

// TestFound runs a fake which from a temp PATH, as Node's spawnSync would find it.
func TestFound(t *testing.T) {
	for _, c := range []struct {
		name  string
		which string // the fake's body; "" for no which on PATH
		mode  os.FileMode
		want  bool
	}{
		{"exit-0", `[ "$1" = caffeinate ] && exit 0; exit 2`, 0o755, true},
		{"exit-1", "exit 1", 0o755, false},
		{"killed", "kill -TERM $$", 0o755, false},
		{"missing", "", 0, false},
		{"not-executable", "exit 0", 0o644, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.which != "" {
				body := "#!/bin/sh\n" + c.which + "\n"
				if err := os.WriteFile(filepath.Join(dir, "which"), []byte(body), c.mode); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir)
			if got := found("caffeinate"); got != c.want {
				t.Errorf("found(caffeinate) = %v, want %v", got, c.want)
			}
		})
	}
}

// TestWrapUsesWhich: Wrap looks the wrapper up with which on PATH; without one it is off.
func TestWrapUsesWhich(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cmd, args := Wrap("/bin/orch", []string{"worker"}, true)
	if cmd != "/bin/orch" || strings.Join(args, " ") != "worker" {
		t.Errorf("Wrap without which = %s %q, want the command unchanged", cmd, args)
	}
}
