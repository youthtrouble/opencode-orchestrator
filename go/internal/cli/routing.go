package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

const routingUsage = "usage: orch routing [--refresh | --cached] [--json]"

// Routing implements the plugin-facing `orch routing [--refresh | --cached] [--json]`:
// getRouting()'s {usage, registry, channels, resolved, prompt} with --json (indent 2), else
// the prompt text. --cached never collects: it routes from the cache as it is, with "stale":
// true when it is old, and exits 3 when there is no cache.
func Routing(args []string, stdio Stdio) int {
	opts, positional := cliargs.ParseArgs(args, nil, []string{"refresh", "cached", "json"})
	if len(positional) > 0 || hasOtherKeys(opts, "refresh", "cached", "json") || (opts.IsFlag("refresh") && opts.IsFlag("cached")) {
		fmt.Fprintln(stdio.Err, routingUsage)
		return 2
	}
	cfg, err := usageConfig()
	if err != nil {
		return fail(stdio, "routing", err)
	}
	var r *usage.Routing
	if opts.IsFlag("cached") {
		r, err = usage.CachedRouting(cfg)
	} else {
		r, err = usage.GetRouting(cfg, opts.IsFlag("refresh"))
	}
	if errors.Is(err, usage.ErrNoCache) {
		fail(stdio, "routing", err)
		return 3
	}
	if err != nil {
		return fail(stdio, "routing", err)
	}
	if !opts.IsFlag("json") {
		fmt.Fprintln(stdio.Out, r.Prompt)
		return 0
	}
	return printJSON(stdio, "routing", r.JSON(), "  ")
}

// hasOtherKeys reports an option outside allowed: the new plugin-facing commands reject
// what they do not understand instead of ignoring it.
func hasOtherKeys(opts cliargs.Opts, allowed ...string) bool {
	for key := range opts {
		if !slices.Contains(allowed, key) {
			return true
		}
	}
	return false
}
