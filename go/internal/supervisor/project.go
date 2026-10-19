// Package supervisor is `orch supervisor` (bin/supervisor.mjs): it watches every worker lane
// of a project, records durable events in <project>/.orchestrator/supervisor-state.json,
// posts them to the mailbox and wakes the coordinator, and re-dispatches budget-deferred
// lanes once their window has reset.
//
// The state, config, acks and worker files are JSON documents shared with the JS supervisor
// and may be hand-edited, so they are read as jsjson values and mutated in place in JS key
// order, statement by statement as the JS does: a throw keeps every earlier mutation and
// write. Shapes the JS mutates invisibly (arrays) are read as {} (PE-16 in go/README.md).
package supervisor

import (
	"path/filepath"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

// Project is the supervisor's files in one project.
type Project struct {
	Root       string
	Dir        string // .orchestrator
	ConfigPath string // supervisor.json
	StatePath  string // supervisor-state.json
	AcksPath   string // supervisor-acks.json
	LockPath   string // supervisor.lock: the serving supervisor's pid as text
	StopPath   string // supervisor.stop
	LogPath    string // logs/supervisor.log
	workers    worker.Project
}

// NewProject returns the supervisor files of the project at root (an absolute path).
func NewProject(root string) Project {
	dir := filepath.Join(root, ".orchestrator")
	return Project{
		Root:       root,
		Dir:        dir,
		ConfigPath: filepath.Join(dir, "supervisor.json"),
		StatePath:  filepath.Join(dir, "supervisor-state.json"),
		AcksPath:   filepath.Join(dir, "supervisor-acks.json"),
		LockPath:   filepath.Join(dir, "supervisor.lock"),
		StopPath:   filepath.Join(dir, "supervisor.stop"),
		LogPath:    filepath.Join(dir, "logs", "supervisor.log"),
		workers:    worker.NewProject(root),
	}
}

// readJSON is the JS readJson(f, fallback): the file's JSON value, or fallback() when it
// cannot be read or parsed. A file holding null gives null, not the fallback.
func readJSON(path string, fallback func() any) any {
	if value, ok := nodefs.ReadJSON(path); ok {
		return value
	}
	return fallback()
}

// emptyObject is the `{}` fallback.
func emptyObject() any { return jsjson.NewObject() }

// writeJSON is the JS writeJson(f, v): mkdir the parent, then write `<f>.tmp` and rename it
// onto f.
func writeJSON(path string, value any) error {
	if err := nodefs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := jsjson.MarshalIndent(value, "  ")
	if err != nil {
		return err
	}
	return nodefs.ReplaceFile(path, append(data, '\n'))
}

// asObject reads a document the supervisor mutates: an object as it is, an array as {}
// (PE-16: the JS sets named properties on the array, which JSON.stringify drops). Any other
// value is returned as it is, for the caller to treat as the JS treats it.
func asObject(value any) any {
	if _, isArray := value.([]any); isArray {
		return jsjson.NewObject()
	}
	return value
}
