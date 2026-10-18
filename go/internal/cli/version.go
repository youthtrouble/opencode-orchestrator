package cli

import (
	"fmt"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/version"
)

// Version implements `orch version`: prints e.g. "0.5.0 (abc1234)" and exits 0.
func Version(_ []string, stdio Stdio) int {
	fmt.Fprintln(stdio.Out, version.String())
	return 0
}
