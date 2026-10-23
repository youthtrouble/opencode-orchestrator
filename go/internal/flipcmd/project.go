// Package flipcmd is `orch flip` (bin/flip.mjs): it records in <project>/.orchestrator/
// flip.json which front door drives the project, whether the conductor paused for budget or
// stopped at a human gate, and decides when to flip between the doors.
//
// flip.json may hold any JSON value. Reads distinguish absent state from unreadable state;
// every writer reads and replaces it inside one ownership transaction.
package flipcmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

// stateRef is the ref every flip message carries.
const stateRef = ".orchestrator/STATE.md"

// Project is the flip state of one project. Now is the clock of every timestamp written and
// of decide's cooldown; NewID gives mailbox message ids. Tests replace both.
type Project struct {
	Root     string
	FlipPath string
	Now      func() time.Time
	NewID    func() string
}

// NewProject returns the flip state of the project at root (an absolute path).
func NewProject(root string) Project {
	return Project{
		Root:     root,
		FlipPath: filepath.Join(root, ".orchestrator", "flip.json"),
		Now:      time.Now,
		NewID:    mailbox.NewUUID,
	}
}

// ErrNoUpdate lets an Update callback inspect state without replacing it. Update consumes
// this sentinel inside the transaction, so lock-release failures still reach the caller.
var ErrNoUpdate = errors.New("flip state unchanged")

// Record distinguishes an absent file from a present JSON value, including null.
type Record struct {
	Value   any
	Present bool
}

// StateError means flip.json exists but cannot safely be interpreted.
type StateError struct {
	Path string
	Code string
	Err  error
}

func (e *StateError) Error() string {
	return fmt.Sprintf("flip state unreadable: %s: %s", e.Path, e.Code)
}
func (e *StateError) Unwrap() error { return e.Err }

// GateReason is the fail-closed decision for unreadable state.
func (e *StateError) GateReason() string {
	return "flip state unreadable: " + e.Code + "; fix or remove .orchestrator/flip.json"
}

// Read returns absent only for ENOENT. Invalid JSON and filesystem failures fail closed.
func (p Project) Read() (Record, error) {
	data, err := nodefs.ReadFile(p.FlipPath)
	if err != nil {
		if nodefs.Code(err) == "ENOENT" {
			return Record{}, nil
		}
		return Record{}, &StateError{p.FlipPath, nodefs.Code(err), err}
	}
	value, err := jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		return Record{}, &StateError{p.FlipPath, "INVALID_JSON", err}
	}
	return Record{Value: value, Present: true}, nil
}

// Update serializes read-modify-write across processes and goroutines. A callback error
// leaves flip.json untouched; ErrNoUpdate skips the write successfully. Callbacks must not
// nest Updates or launch child processes.
func (p Project) Update(fn func(Record) (any, error)) error {
	if err := p.makeStateDir(); err != nil {
		return err
	}
	return ownlock.Transaction(p.FlipPath+".lock", func() error {
		prev, err := p.Read()
		if err != nil {
			return err
		}
		next, err := fn(prev)
		if err == ErrNoUpdate {
			return nil
		}
		if err != nil {
			return err
		}
		return p.writeFlip(next)
	})
}

// makeStateDir is mkdirSync(dirname(FLIP), {recursive: true}).
func (p Project) makeStateDir() error {
	return nodefs.MkdirAll(filepath.Dir(p.FlipPath))
}

// writeFlip replaces flip.json with value as JSON (indent 2) and a newline, atomically and
// owner-only (PE-21), so a supervisor tick never reads an empty or partial gate. The JS
// also writes a temp file and renames it, but leaves the file mode to the umask.
func (p Project) writeFlip(value any) error {
	data, err := jsjson.MarshalIndent(value, "  ")
	if err != nil {
		return err
	}
	return nodefs.WritePrivateFile(p.FlipPath, append(data, '\n'))
}

// postIgnoringErrors posts d to the project mailbox. The JS wraps each post in a try/catch
// whose comment calls the mailbox optional, so a refusal or a filesystem failure changes
// nothing a flip command prints or exits with.
func (p Project) postIgnoringErrors(d mailbox.Draft) {
	box := mailbox.New(p.Root)
	box.Now, box.NewID = p.Now, p.NewID
	_, _ = box.Post(d)
}

// conductor is the mailbox name `conductor-${door}` of a door's conductor.
func conductor(door any) string {
	return "conductor-" + jsjson.TemplateString(door)
}

// doorAnswer is the compact line pause and gate print: {<key>: true, door}.
func doorAnswer(key string, door any) *jsjson.Object {
	out := jsjson.NewObject()
	out.Set(key, true)
	out.Set("door", door)
	return out
}
