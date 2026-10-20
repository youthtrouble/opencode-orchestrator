// Package session keeps the conductor session heartbeats: every conductor, interactive or
// headless, keeps one small record in <project>/.orchestrator/sessions/<door>.json, so a
// dashboard can tell a human chatting with a conductor from an idle project. It writes the
// bytes lib/session.mjs writes.
package session

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
)

// Doors are in byte order, which is the order Read returns them in.
var Doors = []string{"claude", "opencode"}

// States a heartbeat can record.
var States = []string{"working", "waiting", "done", "ended"}

// hookEventStates maps the Claude Code hook events a heartbeat follows to their state; any
// other event writes nothing.
var hookEventStates = map[string]string{
	"UserPromptSubmit": "working",
	"SessionStart":     "working",
	"Stop":             "waiting",
	"SessionEnd":       "ended",
}

// Limits in UTF-16 code units, the unit JS strings count in.
const (
	nowMaxUnits   = 200
	sliceMaxUnits = 40
)

var progressPattern = regexp.MustCompile(`^(\d+)/(\d+)$`)

// Error is a failure the command reports as one line (`session <command>: <message>`) and
// exit 1.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Dir is <project>/.orchestrator/sessions.
func Dir(root string) string {
	return filepath.Join(paths.ProjectDir(root), "sessions")
}

func relativeFile(door string) string {
	return ".orchestrator/sessions/" + door + ".json"
}

// IsDoor and IsState report whether a command-line value names a door or a state.
func IsDoor(door string) bool   { return slices.Contains(Doors, door) }
func IsState(state string) bool { return slices.Contains(States, state) }

// Progress is "N/M" as {done: N, total: M}.
type Progress struct{ Done, Total float64 }

// ParseProgress parses "N/M" with M >= 1; ok is false for anything else. Digits beyond float64
// precision round as JS Number() rounds them.
func ParseProgress(text string) (progress Progress, ok bool) {
	match := progressPattern.FindStringSubmatch(text)
	if match == nil {
		return Progress{}, false
	}
	done, total := parseDigits(match[1]), parseDigits(match[2])
	return Progress{Done: done, Total: total}, total >= 1
}

// parseDigits is Number(digits) for a string of ASCII digits: a magnitude past float64's range
// is +Inf (ParseFloat's range error carries that value), as in JS.
func parseDigits(digits string) float64 {
	value, _ := strconv.ParseFloat(digits, 64)
	return value
}

// HookInput is what a heartbeat reads of the JSON a Claude Code hook writes to stdin. The
// prompt and transcript_path are never read, so they cannot be stored or printed.
type HookInput struct {
	Event     any     // hook_event_name, whatever its JSON type
	SessionID *string // session_id when it is a string
	Cwd       string  // cwd when it is a string, else ""
}

// ParseHookInput parses the hook's stdin.
func ParseHookInput(text string) (HookInput, error) {
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		return HookInput{}, &Error{Message: "hook input is not valid JSON"}
	}
	input, isObject := value.(*jsjson.Object)
	if !isObject {
		return HookInput{}, &Error{Message: "hook input is not a JSON object"}
	}
	parsed := HookInput{Event: jsjson.Path(input, "hook_event_name")}
	if id, isString := jsjson.Path(input, "session_id").(string); isString {
		parsed.SessionID = &id
	}
	parsed.Cwd, _ = jsjson.Path(input, "cwd").(string)
	return parsed, nil
}

// HookEventState is the state a hook event records; ok is false for an event a heartbeat does
// not follow, or one that is not a string.
func HookEventState(event any) (state string, ok bool) {
	name, isString := event.(string)
	if !isString {
		return "", false
	}
	state, ok = hookEventStates[name]
	return state, ok
}

// Change is one field of a beat: the zero value keeps the previous value, Clear stores null
// and To stores a value.
type Change[T any] struct {
	set   bool
	value *T
}

func Clear[T any]() Change[T] { return Change[T]{set: true} }
func To[T any](v T) Change[T] { return Change[T]{set: true, value: &v} }

// Beat is one heartbeat update. State is always given.
type Beat struct {
	State     string
	SessionID Change[string]
	Now       Change[string]
	Slice     Change[string]
	Progress  Change[Progress]
}

// Write writes the door's heartbeat merged over the previous one and stamped at. It reports
// whether a previous file that was not a JSON object was replaced, so the caller can say so.
func Write(root, door string, beat Beat, at time.Time) (replacedUnreadable bool, err error) {
	file := filepath.Join(Dir(root), door+".json")
	if err := ensureDir(root); err != nil {
		return false, err
	}
	previous, unreadable, err := readPrevious(file, door)
	if err != nil {
		return false, err
	}
	kept := keptFields(previous)
	record := jsjson.NewObject()
	record.Set("door", door)
	record.Set("session_id", merge(beat.SessionID, kept.sessionID, asIs))
	record.Set("state", beat.State)
	record.Set("now", merge(beat.Now, kept.now, cutTo(nowMaxUnits)))
	record.Set("slice", merge(beat.Slice, kept.slice, cutTo(sliceMaxUnits)))
	record.Set("progress", merge(beat.Progress, kept.progress, progressObject))
	record.Set("updated_at", jsjson.ISOString(at))
	data, err := jsjson.MarshalIndent(record, "  ")
	if err != nil {
		return false, err
	}
	if err := nodefs.WritePrivateFile(file, append(data, '\n')); err != nil {
		return false, &Error{Message: fmt.Sprintf("could not write %s: %s", relativeFile(door), nodefs.Code(err))}
	}
	return unreadable, nil
}

// merge is the stored value of one field: the kept one when the change is unset, null when it
// clears, else the given value normalised.
func merge[T any](change Change[T], kept any, normalise func(T) any) any {
	switch {
	case !change.set:
		return kept
	case change.value == nil:
		return nil
	}
	return normalise(*change.value)
}

// asIs stores text whole. A Go string is always well-formed, as the JS makes its own with
// toWellFormed.
func asIs(text string) any { return text }

// cutTo cuts text to maxUnits UTF-16 units; a split surrogate pair leaves U+FFFD, as the JS
// leaves after toWellFormed.
func cutTo(maxUnits int) func(string) any {
	return func(text string) any { return jsstr.Slice(text, 0, maxUnits) }
}

func progressObject(p Progress) any {
	object := jsjson.NewObject()
	object.Set("done", p.Done)
	object.Set("total", p.Total)
	return object
}

// ensureDir creates .orchestrator/ with default permissions, as every state-creating command
// creates it, and the sessions directory owner-only.
func ensureDir(root string) error {
	if err := nodefs.MkdirAll(paths.ProjectDir(root)); err != nil {
		return &Error{Message: "could not create .orchestrator/sessions: " + nodefs.Code(err)}
	}
	if err := nodefs.MkdirPrivate(Dir(root)); err != nil {
		return &Error{Message: "could not create .orchestrator/sessions: " + nodefs.Code(err)}
	}
	return nil
}

// readPrevious is the previous record, or nil when there is none. A file that is not a JSON
// object is reported unreadable (and replaced by the write); one that cannot be read at all
// fails the beat.
func readPrevious(file, door string) (record *jsjson.Object, unreadable bool, err error) {
	data, err := nodefs.ReadFile(file)
	if err != nil {
		if nodefs.Code(err) == "ENOENT" {
			return nil, false, nil
		}
		return nil, false, &Error{Message: fmt.Sprintf("could not read %s: %s", relativeFile(door), nodefs.Code(err))}
	}
	value, err := jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		return nil, true, nil
	}
	record, isObject := value.(*jsjson.Object)
	return record, !isObject, nil
}

// kept are the previous values worth keeping, as JSON values: each field only when it has its
// own shape, else nil (null).
type kept struct {
	sessionID, now, slice, progress any
}

func keptFields(previous *jsjson.Object) kept {
	return kept{
		sessionID: keptText(previous, "session_id"),
		now:       keptText(previous, "now"),
		slice:     keptText(previous, "slice"),
		progress:  keptProgress(previous),
	}
}

func keptText(previous *jsjson.Object, key string) any {
	if text, isString := jsjson.Path(previous, key).(string); isString {
		return text
	}
	return nil
}

func keptProgress(previous *jsjson.Object) any {
	progress, isObject := jsjson.Path(previous, "progress").(*jsjson.Object)
	if !isObject {
		return nil
	}
	done, doneIsNumber := jsjson.Path(progress, "done").(float64)
	total, totalIsNumber := jsjson.Path(progress, "total").(float64)
	if !doneIsNumber || !totalIsNumber {
		return nil
	}
	return progressObject(Progress{Done: done, Total: total})
}

// Read returns every door's record as stored, keyed by door, and the files that could not be
// read or parsed, each as "<file>: <reason>".
func Read(root string) (sessions *jsjson.Object, skipped []string) {
	sessions = jsjson.NewObject()
	for _, door := range Doors {
		data, err := nodefs.ReadFile(filepath.Join(Dir(root), door+".json"))
		if err != nil {
			if code := nodefs.Code(err); code != "ENOENT" {
				skipped = append(skipped, relativeFile(door)+": "+code)
			}
			continue
		}
		value, err := jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
		if err != nil {
			skipped = append(skipped, relativeFile(door)+": not valid JSON")
			continue
		}
		sessions.Set(door, value)
	}
	return sessions, skipped
}
