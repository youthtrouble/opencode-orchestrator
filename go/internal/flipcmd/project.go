// Package flipcmd is `orch flip` (bin/flip.mjs): it records in <project>/.orchestrator/
// flip.json which front door drives the project, whether the conductor paused for budget or
// stopped at a human gate, and decides when to flip between the doors.
//
// Options are the JS values of cliargs.Opts: a bare --active, --reason or --question is true
// and reaches flip.json, the mailbox and stdout as true, as in the JS. flip.json may hold any
// JSON value (a hand edit), and each command treats each shape as the JS does.
package flipcmd

import (
	"path/filepath"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
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

// ReadFlip is the JS readFlip(): flip.json's JSON value, or nil when it cannot be read or
// parsed.
func (p Project) ReadFlip() any {
	return jsjson.ReadFileOrNull(p.FlipPath)
}

// makeStateDir is mkdirSync(dirname(FLIP), {recursive: true}).
func (p Project) makeStateDir() error {
	return nodefs.MkdirAll(filepath.Dir(p.FlipPath))
}

// writeFlip replaces flip.json with value as JSON (indent 2) and a newline, truncating and
// writing in place as writeFileSync does (not atomic, as in the JS).
func (p Project) writeFlip(value any) error {
	data, err := jsjson.MarshalIndent(value, "  ")
	if err != nil {
		return err
	}
	return nodefs.WriteFile(p.FlipPath, append(data, '\n'))
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
