package worker

import (
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// State is a running worker's state object and where it is published.
type State struct {
	project Project
	now     func() time.Time
	// Fields is the state object in JS key order; Publish writes it to the lane's state file.
	Fields *jsjson.Object
}

// NewState returns the state fields of a worker of project, timestamped by now.
func (p Project) NewState(fields *jsjson.Object, now func() time.Time) *State {
	return &State{project: p, now: now, Fields: fields}
}

// Publish is the JS publish(state, changes): Object.assign(state, changes, {updated_at}), then
// the whole state is written to `<lane>.json` through a private temp and a rename (PE-21). Only
// after a successful write is state_storage set to "available", so the file shows it from the
// second write on. A failure never stops the worker: it sets state_storage "unavailable" and
// last_state_write_error (Node's err.code), which stays after a later write succeeds.
func (s *State) Publish(changes *jsjson.Object) {
	for _, key := range changes.Keys() {
		value, _ := changes.Get(key)
		s.Fields.Set(key, value)
	}
	s.Fields.Set("updated_at", jsjson.ISOString(s.now()))
	if err := s.write(); err != nil {
		s.Fields.Set("state_storage", "unavailable")
		s.Fields.Set("last_state_write_error", nodefs.ErrorCode(err))
		return
	}
	s.Fields.Set("state_storage", "available")
}

// write is the try block of publish: mkdir, then replace the state file. The state holds
// prompts and errors, and the supervisor may rewrite it concurrently, so the directory and file
// are owner-only and each write uses its own temp (PE-21).
func (s *State) write() error {
	if err := nodefs.MkdirPrivate(s.project.WorkersDir); err != nil {
		return err
	}
	data, err := jsjson.MarshalIndent(s.Fields, "  ")
	if err != nil {
		return err
	}
	file := s.project.StatePath(jsjson.Path(s.Fields, "lane")) // a worker's lane is never null
	return nodefs.WritePrivateFile(file, append(data, '\n'))
}
