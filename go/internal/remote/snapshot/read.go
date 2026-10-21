package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/session"
)

// InputError is project state that exists but could not be read. The project is left out of
// the push; the scan of the other projects goes on.
type InputError struct {
	File string
	Err  error
}

func (e *InputError) Error() string { return fmt.Sprintf("%s: %v", e.File, e.Err) }

func (e *InputError) Unwrap() error { return e.Err }

// inputs is what a snapshot is derived from, read from one project's .orchestrator/.
type inputs struct {
	gate         *gate  // flip.json awaiting_human
	budgetPaused bool   // flip.json paused
	flipTo       string // flip.json to
	lane         lane   // workers/conductor.json
	session      *heartbeat
	messages     []message // the whole mailbox, oldest first
}

// gate is a human gate (flip gate): Question is the mailbox id it waits on, if any.
type gate struct{ reason, question string }

// lane is the headless conductor lane; the zero value is no lane.
type lane struct {
	status, harness           string
	alive                     bool // its pid answers signal 0
	startedAt, lastProgressAt time.Time
}

// headless reports a conductor lane that is running and alive.
func (l lane) headless() bool { return l.status == "running" && l.alive }

// heartbeat is a session file (package session), the newest of the doors'.
type heartbeat struct {
	door, state, now, slice string
	progress                *wire.Progress
	updatedAt               time.Time
}

// message is a mailbox line with the fields a snapshot uses.
type message struct {
	id, kind, from, to, subject, body string
	at                                time.Time
	recommended                       *string // nil for questions posted before the contract
	blocking, ackedByHuman            bool
}

func readInputs(root string, alive func(pid any) bool) (inputs, error) {
	var in inputs
	flip, err := readRecord(filepath.Join(paths.ProjectDir(root), "flip.json"))
	if err != nil {
		return inputs{}, err
	}
	in.gate, in.budgetPaused, in.flipTo = gateOf(flip), flip.truthy("paused"), flip.text("to")
	conductor, err := readRecord(filepath.Join(paths.ProjectDir(root), "workers", "conductor.json"))
	if err != nil {
		return inputs{}, err
	}
	in.lane = laneOf(conductor, alive)
	if in.session, err = newestHeartbeat(root); err != nil {
		return inputs{}, err
	}
	if in.messages, err = readMessages(root); err != nil {
		return inputs{}, err
	}
	return in, nil
}

// record is a JSON object from a state file; nil when there is none.
type record map[string]any

// readRecord reads a state file that may hold anything. Like the JS readJSONOrNull the other
// runtimes use, a missing file, one that is not JSON or one whose value is not an object is no
// record: these files are hand-edited and written by several programs. Only a failed read of a
// file that exists is an error.
func readRecord(file string) (record, error) {
	data, err := os.ReadFile(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &InputError{File: file, Err: err}
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return nil, nil
	}
	object, _ := value.(map[string]any)
	return object, nil
}

func (r record) text(key string) string {
	text, _ := r[key].(string)
	return text
}

// truthy is JavaScript truthiness of a JSON value, as the supervisor (supervisor.mjs) tests
// flip.json's paused and awaiting_human: present and not false, 0, "" or null.
func (r record) truthy(key string) bool {
	switch value := r[key].(type) {
	case nil:
		return false
	case bool:
		return value
	case float64:
		return value != 0
	case string:
		return value != ""
	}
	return true // objects and arrays, even empty ones
}

func (r record) object(key string) record {
	object, _ := r[key].(map[string]any)
	return object
}

func (r record) time(key string) time.Time {
	at, _ := parseTime(r[key])
	return at
}

// gateOf is flip.json's awaiting_human. Like the supervisor, any truthy value is a gate, so the
// page shows what holds the supervisor back; flip gate writes an object, and only an object
// carries a reason and a question.
func gateOf(flip record) *gate {
	if !flip.truthy("awaiting_human") {
		return nil
	}
	awaiting := flip.object("awaiting_human")
	return &gate{reason: awaiting.text("reason"), question: awaiting.text("question")}
}

func laneOf(conductor record, alive func(pid any) bool) lane {
	if conductor == nil {
		return lane{}
	}
	return lane{
		status:         conductor.text("status"),
		harness:        conductor.text("harness"),
		alive:          alive(conductor["pid"]),
		startedAt:      conductor.time("started_at"),
		lastProgressAt: conductor.time("last_progress_at"),
	}
}

// newestHeartbeat is the most recently updated session file, or nil. A file without a state or
// a parsable updated_at cannot be ordered, so it is not considered.
func newestHeartbeat(root string) (*heartbeat, error) {
	var newest *heartbeat
	for _, door := range session.Doors {
		beat, err := readRecord(filepath.Join(session.Dir(root), door+".json"))
		if err != nil {
			return nil, err
		}
		updatedAt := beat.time("updated_at")
		if beat.text("state") == "" || updatedAt.IsZero() || (newest != nil && !updatedAt.After(newest.updatedAt)) {
			continue
		}
		newest = &heartbeat{
			door:      door,
			state:     beat.text("state"),
			now:       beat.text("now"),
			slice:     beat.text("slice"),
			progress:  progressOf(beat.object("progress")),
			updatedAt: updatedAt,
		}
	}
	return newest, nil
}

// progressOf is {done, total} when both are whole numbers the schema accepts.
func progressOf(progress record) *wire.Progress {
	done, doneOK := wholeNumber(progress["done"])
	total, totalOK := wholeNumber(progress["total"])
	if !doneOK || !totalOK || done < 0 || total < 1 {
		return nil
	}
	return &wire.Progress{Done: done, Total: total}
}

func wholeNumber(value any) (int, bool) {
	number, isNumber := value.(float64)
	if !isNumber || number != math.Trunc(number) || math.Abs(number) > math.MaxInt32 {
		return 0, false
	}
	return int(number), true
}

// readMessages is the mailbox through package mailbox, so acks are counted as orch counts them.
// A line the wire cannot carry (no sendableID, no kind, no parsable at) is left out: such
// lines are hand-edited, and neither the page nor a reply can use them.
func readMessages(root string) ([]message, error) {
	lines, err := mailbox.New(root).List(mailbox.Query{})
	if err != nil {
		return nil, &InputError{File: paths.MailboxDir(root), Err: err}
	}
	messages := make([]message, 0, len(lines))
	for _, line := range lines {
		text := func(key string) string {
			value, _ := jsjson.Path(line, key).(string)
			return value
		}
		at, atOK := parseTime(jsjson.Path(line, "at"))
		msg := message{
			id: text("id"), kind: text("kind"), from: text("from"), to: text("to"),
			subject: text("subject"), body: text("body"), at: at,
			blocking: jsjson.Path(line, "blocking") == true,
		}
		if !sendableID(msg.id) || msg.kind == "" || !atOK {
			continue
		}
		if recommended := text("recommended"); recommended != "" {
			msg.recommended = &recommended
		}
		ackedBy, _ := jsjson.Path(line, "acked_by").([]any)
		msg.ackedByHuman = slices.Contains(ackedBy, any("human"))
		messages = append(messages, msg)
	}
	return messages, nil
}

// parseTime reads an RFC 3339 timestamp, as every orchestrator file writes them.
func parseTime(value any) (time.Time, bool) {
	text, isString := value.(string)
	if !isString {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	return at, err == nil
}
