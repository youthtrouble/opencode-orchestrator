package cli

import "github.com/youthtrouble/opencode-orchestrator/go/internal/bridge"

// ClaudeExec implements `orch claude-exec [args]` (bin/claude-exec.mjs): it delegates a bounded
// task to `claude -p` and prints one JSON result. Arguments are decoded as Node decodes argv.
func ClaudeExec(args []string, stdio Stdio) int {
	return bridge.RunClaude(decodeArgv(args), stdio.In, stdio.Out, stdio.Err)
}
