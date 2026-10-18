package cli

import (
	"fmt"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

const effortUsage = "usage: orch effort --role ROLE [--size SIZE]"

// Effort implements the plugin-facing `orch effort --role R [--size S]`: the registry's
// effort policy for the role and size (default size when omitted) as plain text, or "null"
// when the policy has no table for the role.
func Effort(args []string, stdio Stdio) int {
	opts, positional := cliargs.ParseArgs(args, []string{"role", "size"}, nil)
	role, hasRole := opts.Value("role")
	size, _ := opts.Value("size")
	if len(positional) > 0 || hasOtherKeys(opts, "role", "size") || !hasRole || opts.IsFlag("size") {
		fmt.Fprintln(stdio.Err, effortUsage)
		return 2
	}
	p, err := paths.Resolve(os.Getenv)
	if err != nil {
		return fail(stdio, "effort", err)
	}
	reg, err := registry.Load(p.RegistryFile)
	if err != nil {
		return fail(stdio, "effort", err)
	}
	effort, ok := routing.EffortFor(reg, role, size)
	if !ok {
		effort = "null"
	}
	fmt.Fprintln(stdio.Out, effort)
	return 0
}
