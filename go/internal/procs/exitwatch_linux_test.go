package procs

import "github.com/youthtrouble/opencode-orchestrator/go/internal/proctest"

// hasExited reports whether pid, a child that only the test reaps, has exited: /proc shows it
// as a zombie, which proctest.Gone counts as gone.
func hasExited(pid int) (bool, error) {
	return proctest.Gone(pid)
}
