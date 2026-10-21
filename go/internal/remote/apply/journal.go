package apply

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// journalLimit is how many entries a journal remembers. The Worker re-sends an answer or an
// action only until its ack lands, so a replay is of one of the last few; older ones drop out.
const journalLimit = 256

// answerKind is the kind of an answer's entry, beside the action kinds pause and resume: the
// Worker numbers answers and actions apart, so the kind is part of an entry's key.
const answerKind = "answer"

// actionJournal remembers outcomes so that a replay (its ack lost) resends its outcome instead
// of acting again on state a human may have changed since. It is Go-only and rewritten whole
// and atomically, so a crash leaves the old or the new journal, never a torn one.
//
// A project's journal, <project>/.orchestrator/remote/actions.json, holds its pause and resume
// actions, recorded in flight before flip.json is touched and settled after. The unresolved
// journal, unresolved.json in the remote state directory, holds the rejections of answers and
// actions whose project could not be resolved, keyed by project key, so a replay after
// discovery finds the project resends the rejection rather than applying it.
type actionJournal struct {
	path       string
	unresolved bool
}

// journalKey identifies an entry. ProjectKey is empty in a project's journal, which is the
// project's own.
type journalKey struct {
	ProjectKey string `json:"project_key,omitempty"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
}

// journalEntry is one answer or action. An entry without a Result is in flight: recorded
// before the action touched flip.json, its outcome not yet.
type journalEntry struct {
	journalKey
	Result Result `json:"result,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (e journalEntry) settled() bool    { return e.Result != "" }
func (e journalEntry) outcome() Outcome { return Outcome{e.Result, e.Detail} }

func newActionJournal(root string) actionJournal {
	return actionJournal{path: filepath.Join(root, ".orchestrator", "remote", "actions.json")}
}

func newUnresolvedJournal(stateDir string) actionJournal {
	return actionJournal{path: filepath.Join(stateDir, "unresolved.json"), unresolved: true}
}

// lookup is the entry of key, found false when the journal has none.
func (j actionJournal) lookup(key journalKey) (entry journalEntry, found bool, err error) {
	entries, err := j.load()
	if err != nil {
		return journalEntry{}, false, err
	}
	for _, e := range entries {
		if e.journalKey == key {
			return e, true, nil
		}
	}
	return journalEntry{}, false, nil
}

// record replaces the entry of entry's key, or adds it as the newest, keeping the newest
// journalLimit entries.
func (j actionJournal) record(entry journalEntry) error {
	entries, err := j.load()
	if err != nil {
		return err
	}
	kept := []journalEntry{}
	for _, e := range entries {
		if e.journalKey != entry.journalKey {
			kept = append(kept, e)
		}
	}
	kept = append(kept, entry)
	if len(kept) > journalLimit {
		kept = kept[len(kept)-journalLimit:]
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	if err := nodefs.MkdirPrivate(filepath.Dir(j.path)); err != nil {
		return err
	}
	return nodefs.WritePrivateFile(j.path, append(data, '\n'))
}

// load is every entry, oldest first; none when the journal does not exist yet. A journal that
// cannot be read, does not parse or breaks an invariant is an error: applying without it could
// act twice or resend a wrong outcome.
func (j actionJournal) load() ([]journalEntry, error) {
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := decodeEntries(data)
	if err == nil {
		err = j.validate(entries)
	}
	if err != nil {
		return nil, &CorruptJournalError{Path: j.path, Err: err}
	}
	return entries, nil
}

// decodeEntries is a JSON array of entries with only the known fields and nothing after it.
func decodeEntries(data []byte) ([]journalEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var entries []journalEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, errors.New("not an array")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the array")
	}
	return entries, nil
}

// validate checks every entry and that no two share a key.
func (j actionJournal) validate(entries []journalEntry) error {
	seen := map[journalKey]bool{}
	for i, e := range entries {
		problem := j.entryProblem(e)
		if problem == "" && seen[e.journalKey] {
			problem = "a duplicate key"
		}
		if problem != "" {
			return fmt.Errorf("entry %d has %s", i, problem)
		}
		seen[e.journalKey] = true
	}
	return nil
}

// entryProblem is what is wrong with e, "" when nothing: an entry has an id, a kind the journal
// holds, a known result or none, and a detail exactly when settled; a project key is in, and
// only in, the unresolved journal, whose entries are all rejections.
func (j actionJournal) entryProblem(e journalEntry) string {
	switch {
	case e.ID == "":
		return "no id"
	case e.Kind != "pause" && e.Kind != "resume" && (e.Kind != answerKind || !j.unresolved):
		return "an unsupported kind"
	case (e.ProjectKey != "") != j.unresolved:
		return "a misplaced project key"
	case e.Result != "" && e.Result != Applied && e.Result != Superseded && e.Result != Rejected:
		return "an unsupported result"
	case e.settled() == (e.Detail == ""):
		return "a detail that does not match its result"
	case j.unresolved && e.Result != Rejected:
		return "an outcome other than a rejection"
	}
	return ""
}

// CorruptJournalError is a journal that exists but does not parse or breaks an invariant. It
// is never repaired silently: a lost entry could let a replay act again.
type CorruptJournalError struct {
	Path string
	Err  error
}

func (e *CorruptJournalError) Error() string {
	return fmt.Sprintf("%s is not a valid action journal: %v", e.Path, e.Err)
}

func (e *CorruptJournalError) Unwrap() error { return e.Err }
