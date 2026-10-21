package cli

import (
	"fmt"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/budget"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

const budgetUsage = "usage: orch budget gate --vendor anthropic|openai [--force] [--frontier]"

// Budget implements the plugin-facing `orch budget gate --vendor V [--force] [--frontier]`:
// the budgetGate decision without its routing, {allowed, level, resets_at, note} with unset
// fields omitted, compact. Exit 0 whether or not the work is allowed.
func Budget(args []string, stdio Stdio) int {
	if len(args) == 0 || args[0] != "gate" {
		fmt.Fprintln(stdio.Err, budgetUsage)
		return 2
	}
	opts, positional := cliargs.ParseArgs(args[1:], []string{"vendor"}, []string{"force", "frontier"})
	vendor, _ := opts.Value("vendor")
	if len(positional) > 0 || hasOtherKeys(opts, "vendor", "force", "frontier") || (vendor != routing.Anthropic && vendor != routing.OpenAI) {
		fmt.Fprintln(stdio.Err, budgetUsage)
		return 2
	}
	cfg, err := usageConfig()
	if err != nil {
		return fail(stdio, "budget", err)
	}
	decision, err := budget.Gate(cfg, vendor, opts.IsFlag("force"), opts.IsFlag("frontier"), false)
	if err != nil {
		return fail(stdio, "budget", err)
	}
	return printJSON(stdio, "budget", decision.JSON(), "")
}
