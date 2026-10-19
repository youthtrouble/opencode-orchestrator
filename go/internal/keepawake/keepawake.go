// Package keepawake keeps the machine from idle-sleeping while a detached worker (or, later,
// the supervisor) runs, as lib/keepawake.mjs does: the command is wrapped in caffeinate on
// macOS or systemd-inhibit on Linux, when `which` finds it. The assertion lasts only as long
// as the wrapped process; no system setting changes.
package keepawake

import (
	"runtime"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// whichGrace bounds the wait for which's descriptors after it exits; it has none to copy
// (its stdio is /dev/null) and is never signalled, so the value is only Spec's required bound.
const whichGrace = 5 * time.Second

// Wrap is keepAwakeWrap(cmd, args, {enabled}): when enabled and the platform's wrapper is on
// PATH, the command to run instead of cmd and its arguments; otherwise cmd and args unchanged.
func Wrap(cmd string, args []string, enabled bool) (string, []string) {
	return wrap(runtime.GOOS, found, cmd, args, enabled)
}

// wrap is Wrap for the platform goos with the lookup found.
func wrap(goos string, found func(name string) bool, cmd string, args []string, enabled bool) (string, []string) {
	if !enabled {
		return cmd, args
	}
	switch {
	case goos == "darwin" && found("caffeinate"):
		return "caffeinate", append([]string{"-i", "-s", cmd}, args...)
	case goos == "linux" && found("systemd-inhibit"):
		return "systemd-inhibit", append([]string{"--what=idle:sleep", "--why=opencode-orchestrator", cmd}, args...)
	}
	return cmd, args
}

// found is `spawnSync("which", [name], {stdio: "ignore"}).status === 0`: which is resolved on
// PATH as Node resolves it, and a which that is missing, cannot start or is killed by a
// signal means not found.
func found(name string) bool {
	child, err := procs.Start(procs.Spec{Name: "which", Args: []string{name}, Grace: whichGrace})
	if err != nil {
		return false
	}
	code, err := child.Wait()
	return err == nil && code != nil && *code == 0
}
