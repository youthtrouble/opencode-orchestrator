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
	"regexp"
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

// CorruptError is a state file that exists but does not parse or breaks an invariant, such as a
// missing or malformed machine id. It is never repaired silently: a new machine id would make
// every project appear twice on the page.
type CorruptError struct {
	Path string
	Err  error
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("%s is not a valid state file (%v); move it aside and run orch remote init again", e.Path, e.Err)
}

func (e *CorruptError) Unwrap() error { return e.Err }

// canonicalUUIDv4 is the only machine id form newUUIDv4 produces.
var canonicalUUIDv4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Load reads the state file. A missing file is an error wrapping fs.ErrNotExist.
func Load(file string) (State, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return State{}, err
	}
	st, err := parse(data)
	if err != nil {
		return State{}, &CorruptError{Path: file, Err: err}
	}
	return st, nil
}

// parse decodes one state object and checks its invariants: a canonical machine id and a
// projects object whose keys are non-empty and whose entries are objects.
func parse(data []byte) (State, error) {
	var st State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&st); err != nil {
		return State{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return State{}, errors.New("unexpected data after the JSON object")
	}
	if !canonicalUUIDv4.MatchString(st.MachineID) {
		return State{}, fmt.Errorf("machine_id %q is not a lowercase UUID v4", st.MachineID)
	}
	// A null entry decodes to a zero Project, so the raw entries are what show it.
	var raw struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return State{}, err
	}
	if raw.Projects == nil {
		return State{}, errors.New("projects is missing or null")
	}
	for key, entry := range raw.Projects {
		if key == "" || bytes.Equal(bytes.TrimSpace(entry), []byte("null")) {
			return State{}, fmt.Errorf("projects entry %q is empty or null", key)
		}
	}
	return st, nil
}

// Save writes st owner-only, creating the directory 0700.
func Save(file string, st State) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	data, err := encode(st)
	if err != nil {
		return err
	}
	return nodefs.WritePrivateFile(file, data)
}

// Ensure returns the state at file, creating it with a new machine id when it does not exist.
// An existing machine id is kept, and an invalid one is a CorruptError, never replaced: project
// keys derive from it, so a new one would make every project appear twice on the page.
// Concurrent callers, separate orch processes included, all get the one id that was published.
func Ensure(file string) (State, error) {
	st, err := Load(file)
	if !errors.Is(err, fs.ErrNotExist) {
		return st, err
	}
	st = State{MachineID: newUUIDv4(), Projects: map[string]Project{}}
	err = create(file, st)
	if errors.Is(err, fs.ErrExist) {
		return Load(file) // another process published first; its id is the machine's
	}
	return st, err
}

// create publishes st at file only when file does not exist. The complete content goes to a
// private temp, which link(2) then adds under the final name: unlike rename, link refuses an
// existing name, so exactly one of several concurrent creators wins (the others get an error
// wrapping fs.ErrExist), and a reader sees no file or the whole of it, never a partial one.
func create(file string, st State) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := encode(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(file)+".tmp-*") // 0600, a fresh name
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // the published name keeps the content; the temp name goes
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), file)
}

func encode(st State) ([]byte, error) {
	data, err := json.MarshalIndent(st, "", "  ")
	return append(data, '\n'), err
}

// newUUIDv4 is a random RFC 9562 version 4 UUID in lowercase 8-4-4-4-12 form.
func newUUIDv4() string {
	var b [16]byte
	rand.Read(b[:]) // crypto/rand.Read never returns an error and never short-reads
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
