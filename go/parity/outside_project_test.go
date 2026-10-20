//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"testing"
)

// compareOutsideProject runs inv where the project root has no .orchestrator/ directory: both
// runtimes must print the one not-a-project line naming flag, exit 2 and create no
// .orchestrator/ directory; the rest of the outcomes is compared as usual.
func (h *harness) compareOutsideProject(t *testing.T, inv invocation, flag string) {
	t.Helper()
	var sandboxes []sandbox
	seed := orNothing(inv.seed)
	inv.seed = func(t *testing.T, sb sandbox) {
		seed(t, sb)
		sandboxes = append(sandboxes, sb)
	}
	inv.projectPaths = true
	js, orch := h.runPair(t, inv)
	want := "not an orchestrator project: $PROJECT has no .orchestrator/ directory; cd into the project or pass " + flag + " <project dir>\n"
	for name, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 2 || o.stdout != "" || o.stderr != want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 2 and %q", name, o.exitCode, o.stdout, o.stderr, want)
		}
	}
	for _, sb := range sandboxes {
		if info, err := os.Stat(filepath.Join(sb.project, ".orchestrator")); err == nil && info.IsDir() {
			t.Errorf("%s: a refused command created .orchestrator/", sb.project)
		}
	}
	compareOutcomes(t, js, orch)
}
