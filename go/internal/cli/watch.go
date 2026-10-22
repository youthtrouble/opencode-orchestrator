package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/watch"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

// Watch implements `orch watch [--cwd DIR] [--follow] [--interval 20]`, bin/watch.mjs: one
// dashboard render, or with --follow a redraw every interval until the process is killed. A
// routing failure prints `orch watch: <error>` and exits 1 (PE-2); the JS TypeErrors are
// crashes with the JS message.
func Watch(args []string, stdio Stdio) int {
	opts, _ := cliargs.ParseArgs(decodeArgv(args), []string{"cwd", "interval"}, []string{"follow"})
	root, err := projectRoot(opts)
	if err != nil {
		return crash(stdio, err)
	}
	if code, ok := requireProject(stdio, root, "--cwd"); !ok {
		return code
	}
	home, err := paths.NodeHome(os.LookupEnv)
	if err != nil {
		return fail(stdio, "watch", err)
	}
	deps := watch.Deps{
		Root:  root,
		Home:  home,
		Now:   time.Now,
		Alive: worker.Signalable, // bin/watch.mjs at go/parity/js-ref: alive() has no falsy guard
		Routing: func() (*usage.Routing, error) {
			cfg, err := usageConfig()
			if err != nil {
				return nil, err
			}
			return usage.GetRouting(cfg, false)
		},
	}
	render := func() (string, error) { return watch.Render(deps) }
	if opts.IsFlag("follow") {
		watch.Follow(context.Background(), watch.FollowDeps{Render: render, Interval: opts.JS("interval"), Out: stdio.Out, Err: stdio.Err, Sleep: sleep})
		return 0
	}
	frame, err := render()
	var routingErr *watch.RoutingError
	switch {
	case errors.As(err, &routingErr):
		return fail(stdio, "watch", err)
	case err != nil:
		return crash(stdio, err)
	}
	fmt.Fprintln(stdio.Out, frame)
	return 0
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
