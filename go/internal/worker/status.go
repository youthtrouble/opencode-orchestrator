package worker

import (
	"os"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// Status is `worker.mjs status [--lane L]`: for the given lane, or else every `<lane>.json` in
// the workers directory, the parsed state (or {lane, status: "unknown"} when it is falsy or
// unreadable) spread into a new object with process_alive and lock_present set. The error is
// Node's, for a workers directory that exists but cannot be listed.
func (p Project) Status(lane any) ([]any, error) {
	lanes, err := p.statusLanes(lane)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(lanes))
	for _, lane := range lanes {
		state := p.ReadState(lane)
		if !jsjson.Truthy(state) {
			unknown := jsjson.NewObject()
			unknown.Set("lane", lane)
			unknown.Set("status", "unknown")
			state = unknown
		}
		entry := jsjson.Spread(state)
		entry.Set("process_alive", Alive(jsjson.Path(state, "pid")))
		entry.Set("lock_present", nodefs.Exists(p.LockPath(lane)))
		out = append(out, entry)
	}
	return out, nil
}

// statusLanes is the given lane when truthy, else every lane of Lanes.
func (p Project) statusLanes(lane any) ([]any, error) {
	if jsjson.Truthy(lane) {
		return []any{lane}, nil
	}
	names, err := p.Lanes()
	if err != nil {
		return nil, err
	}
	lanes := make([]any, len(names))
	for i, name := range names {
		lanes[i] = name
	}
	return lanes, nil
}

// Lanes is the JS lane listing: the names ending in ".json" in the workers directory with
// that suffix removed, in byte order (Node's readdirSync sorts with strcmp, as os.ReadDir
// does), or none when the directory does not exist. The error is Node's, for a workers
// directory that exists but cannot be listed.
func (p Project) Lanes() ([]string, error) {
	if !nodefs.Exists(p.WorkersDir) {
		return []string{}, nil
	}
	entries, err := os.ReadDir(p.WorkersDir)
	if err != nil {
		return nil, nodefs.FileError(err, "scandir", p.WorkersDir)
	}
	lanes := []string{}
	for _, entry := range entries {
		if name, isState := strings.CutSuffix(jsstr.DecodeUTF8([]byte(entry.Name())), ".json"); isState {
			lanes = append(lanes, name)
		}
	}
	return lanes, nil
}
