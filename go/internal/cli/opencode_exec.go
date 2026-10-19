package cli

import "github.com/youthtrouble/opencode-orchestrator/go/internal/bridge"

// OpencodeExec implements `orch opencode-exec [args]` (bin/opencode-exec.mjs): it runs one
// headless `opencode run` turn and prints one JSON result. Arguments are decoded as Node
// decodes argv.
func OpencodeExec(args []string, stdio Stdio) int {
	return bridge.RunOpencode(decodeArgv(args), stdio.In, stdio.Out, stdio.Err)
}
