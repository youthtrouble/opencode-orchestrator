// Package worker is `orch worker` (bin/worker.mjs): one bounded brief run on one harness as a
// tracked, lockable lane whose state lives in <project>/.orchestrator/workers/<lane>.json and
// whose lock is <lane>.lock beside it.
//
// The JS reads its options as JS values, so a lane is any: a string, true for a bare --lane, or
// nil for an absent one (JS undefined). It is interpolated into file names as the JS template
// literal does ("true.json", "undefined.json"); CheckLane refuses one that contains "/".
package worker

import (
	"math"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// Project is the worker area of one project.
type Project struct {
	Root       string
	WorkersDir string
	LogsDir    string
	// kill sends Stop's SIGINT, as process.kill does; tests replace it so no real process is
	// interrupted. Alive calls syscall.Kill itself: signal 0 only checks that a process
	// exists, so it is safe on any pid and needs no stand-in.
	kill func(pid int, sig syscall.Signal) error
}

// NewProject returns the worker area of the project at root (an absolute path).
func NewProject(root string) Project {
	return Project{
		Root:       root,
		WorkersDir: filepath.Join(root, ".orchestrator", "workers"),
		LogsDir:    filepath.Join(root, ".orchestrator", "logs"),
		kill:       syscall.Kill,
	}
}

// StatePath is `${workersDir}/${lane}.json`.
func (p Project) StatePath(lane any) string {
	return filepath.Join(p.WorkersDir, laneText(lane)+".json")
}

// LockPath is `${workersDir}/${lane}.lock`.
func (p Project) LockPath(lane any) string {
	return filepath.Join(p.WorkersDir, laneText(lane)+".lock")
}

// CheckLane refuses a lane whose text contains "/", which would let its state, lock or log
// name a file outside workers/ or logs/ (`--lane ../flip` overwrote flip.json; Sol S2/R8). A
// bare, absent or empty lane, "." and ".." stay allowed: their names ("true.json",
// "...json") stay inside.
func CheckLane(lane any) error {
	if strings.Contains(laneText(lane), "/") {
		return &UsageError{`lane must not contain "/"`}
	}
	return nil
}

// laneText is the lane as a template literal prints it; nil is undefined.
func laneText(lane any) string {
	return jsjson.TemplateStringOf(lane, lane != nil)
}

// ReadState is JSON.parse of the lane's state file, which may hold any JSON value, or nil when
// the file cannot be read or parsed (the JS returns null for both).
func (p Project) ReadState(lane any) any {
	return nodefs.ReadJSONOrNull(p.StatePath(lane))
}

// Alive is the JS alive(pid): false for a falsy pid, else whether process.kill(pid, 0)
// succeeds or fails with EPERM (the process exists but belongs to someone else). A pid Node
// refuses to coerce (not an integer in the int32 range) throws ERR_INVALID_ARG_TYPE there,
// which is not EPERM, so it is not alive. Signal 0 checks existence and sends nothing.
func Alive(pid any) bool {
	return jsjson.Truthy(pid) && Signalable(pid)
}

// Signalable is process.kill(pid, 0) succeeding or failing with EPERM, without Alive's falsy
// guard: the alive() of watch.mjs. Node's pid check is `pid == (pid | 0)`, so null and
// undefined fail it while 0, false and "" pass and signal the caller's process group (alive).
func Signalable(pid any) bool {
	if pid == nil {
		return false
	}
	n, ok := coercePid(pid)
	if !ok {
		return false
	}
	err := syscall.Kill(n, 0)
	return err == nil || err == syscall.EPERM
}

// coercePid is the pid process.kill uses: Node rejects pid unless `pid == (pid | 0)`, that is
// unless ToNumber(pid) (an array or object through its string form) is an integer in the int32
// range, and then signals that integer.
func coercePid(pid any) (int, bool) {
	n := toNumber(pid)
	if n != math.Trunc(n) || n < math.MinInt32 || n > math.MaxInt32 { // NaN fails the first test
		return 0, false
	}
	return int(n), true
}

// toNumber is JS Number(v) for any parsed JSON value.
func toNumber(v any) float64 {
	switch v.(type) {
	case []any, *jsjson.Object:
		return jsjson.ToNumber(jsjson.TemplateString(v))
	}
	return jsjson.ToNumber(v)
}
