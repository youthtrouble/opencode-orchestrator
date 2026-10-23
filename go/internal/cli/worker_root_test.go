package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerProjectSelector(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--root", "parent/.orchestrator/briefs", "--cwd", "child"}, "--root"},
		{[]string{"stop", "--lane", "x", "--root", "parent", "--cwd", "child"}, "--root"},
		{[]string{"status", "--root", "", "--cwd", "child"}, "--cwd"},
	} {
		code, stdout, stderr := run(t, Worker, tc.args, "")
		if code != 2 || stdout != "" || !strings.Contains(stderr, "pass "+tc.want+" <project dir>") {
			t.Fatalf("%q: exit %d, out %q, err %q", tc.args, code, stdout, stderr)
		}
		root := "parent"
		if tc.want == "--cwd" {
			root = "child"
		}
		absolute, err := filepath.Abs(root)
		if err != nil || !strings.Contains(stderr, absolute+" has no .orchestrator/ directory") {
			t.Fatalf("wrong selected project: %q, %v", stderr, err)
		}
	}
	_, _, want := run(t, Worker, []string{"status", "--cwd"}, "")
	for _, command := range []string{"start", "status", "stop"} {
		code, stdout, stderr := run(t, Worker, []string{command, "--root"}, "")
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("bare --root %s: exit %d, out %q, err %q; want %q", command, code, stdout, stderr, want)
		}
	}
}
