// Package state keeps orch remote's runtime state in
// ~/.local/state/opencode/orchestrator/remote/state.json: the machine id, what was last pushed
// per project, and the last push, pull and error. It is owner-only and never holds secrets.
package state

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
)

// State is the content of state.json.
type State struct {
	MachineID  string             `json:"machine_id"`
	Projects   map[string]Project `json:"projects"`
	LastPushAt *time.Time         `json:"last_push_at"`
	LastPullAt *time.Time         `json:"last_pull_at"`
	LastError  *LastError         `json:"last_error"`
}

// Project is what the Worker last accepted for one project key.
type Project struct {
	LastHash       string     `json:"last_hash"`
	LastAcceptedAt *time.Time `json:"last_accepted_at"`
	RetryAfter     *time.Time `json:"retry_after"`
}

// LastError is the most recent failure. Message must be redacted before it is stored.
type LastError struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// Dir is the remote state directory under home.
func Dir(home string) string {
	return filepath.Join(paths.StateDirIn(home), "remote")
}

// File is state.json under home.
func File(home string) string {
	return filepath.Join(Dir(home), "state.json")
}

// CorruptError is a state file that exists but does not parse.
type CorruptError struct {
	Path string
	Err  error
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("%s does not parse (%v); move it aside and run orch remote init again", e.Path, e.Err)
}

func (e *CorruptError) Unwrap() error { return e.Err }

// Load reads the state file. A missing file is an error wrapping fs.ErrNotExist.
func Load(file string) (State, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return State{}, err
	}
	var st State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&st); err != nil {
		return State{}, &CorruptError{Path: file, Err: err}
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return State{}, &CorruptError{Path: file, Err: errors.New("unexpected data after the JSON object")}
	}
	return st, nil
}

// Save writes st owner-only, creating the directory 0700.
func Save(file string, st State) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return nodefs.WritePrivateFile(file, append(data, '\n'))
}

// Ensure returns the state at file, creating it with a new machine id when it does not exist.
// An existing machine id is kept: project keys derive from it, so a new one would make every
// project appear twice on the page.
func Ensure(file string) (State, error) {
	st, err := Load(file)
	if !errors.Is(err, fs.ErrNotExist) {
		return st, err
	}
	st = State{MachineID: newUUIDv4(), Projects: map[string]Project{}}
	return st, Save(file, st)
}

// newUUIDv4 is a random RFC 9562 version 4 UUID in lowercase 8-4-4-4-12 form.
func newUUIDv4() string {
	var b [16]byte
	rand.Read(b[:]) // crypto/rand.Read never returns an error and never short-reads
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
