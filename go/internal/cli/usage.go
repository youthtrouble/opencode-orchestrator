package cli

import (
	"fmt"
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// Usage implements `orch usage [--refresh] [--json]` (bin/usage.mjs): the routing summary
// as text, or with --json the usage, channels, resolved routing and the OpenAI credits fact
// and setting (indent 2). Exit 1 when
// routing cannot be computed (the JS crashes there).
func Usage(args []string, stdio Stdio) int {
	opts := cliargs.ParseUsage(args)
	cfg, err := usageConfig()
	if err != nil {
		return fail(stdio, "usage", err)
	}
	r, err := usage.GetRouting(cfg, opts.Refresh)
	if err != nil {
		return fail(stdio, "usage", err)
	}
	// stale is always false and the stale hint never prints: GetRouting re-collects a stale
	// cache (a latent JS defect, mirrored; plan 3.10).
	stale := jsjson.Truthy(jsjson.Path(r.Usage, "stale"))
	if !opts.JSON {
		fmt.Fprintln(stdio.Out, r.Prompt)
		if stale {
			fmt.Fprintln(stdio.Out, "(cached usage is stale; pass --refresh)")
		}
		return 0
	}
	resolved := r.Resolved.JSON()
	out := jsjson.NewObject()
	if generatedAt, present := r.Usage.Get("generated_at"); present {
		out.Set("generated_at", generatedAt)
	}
	out.Set("stale", stale)
	out.Set("channels", r.Channels.JSON())
	out.Set("usage", r.Usage)
	for _, key := range []string{"postures", "roles", "reasons", "paused"} {
		value, _ := resolved.Get(key)
		out.Set(key, value)
	}
	out.Set("openai_credits", nil)
	if credits := routing.OpenAICredits(jsjson.Path(r.Usage, "codex")); credits != nil {
		out.Set("openai_credits", credits.JSON())
	}
	out.Set("openai_credits_policy", r.CreditsPolicy.JSON())
	return printJSON(stdio, "usage", out, "  ")
}

// usageConfig is the usage configuration for this process's environment.
func usageConfig() (usage.Config, error) {
	p, err := paths.Resolve(os.Getenv)
	if err != nil {
		return usage.Config{}, err
	}
	return usage.NewConfig(p), nil
}

// printJSON writes value as JSON plus a newline (console.log(JSON.stringify(value, null,
// gap))) and returns the exit code.
func printJSON(stdio Stdio, command string, value any, gap string) int {
	data, err := jsjson.MarshalIndent(value, gap)
	if err != nil {
		return fail(stdio, command, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}

// fail reports err on stderr as "orch <command>: <err>" and returns exit code 1.
func fail(stdio Stdio, command string, err error) int {
	fmt.Fprintf(stdio.Err, "orch %s: %v\n", command, err)
	return 1
}
