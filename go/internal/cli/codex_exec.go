package cli

import "github.com/youthtrouble/opencode-orchestrator/go/internal/bridge"

// CodexExec implements `orch codex-exec [args]` (bin/codex-exec.mjs): it delegates a bounded
// task to `codex exec` and prints one JSON result. Arguments are decoded as Node decodes argv.
func CodexExec(args []string, stdio Stdio) int {
	return bridge.RunCodex(decodeArgv(args), stdio.In, stdio.Out, stdio.Err)
}
