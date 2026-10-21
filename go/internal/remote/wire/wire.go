// Package wire holds the Go side of the Work Trains wire protocol: one struct per
// remote/protocol/v1/schema.json $defs entry that orch remote produces or consumes. The schema
// is the source of truth; a field here that disagrees with it is the bug.
//
// Every schema property is required, except an optional one added within v1 (declared in the
// schema's properties but not its required list), which is a pointer field tagged omitempty:
// nil is left out rather than sent as null. A required property the schema allows to be null is
// a pointer without omitempty, which encodes nil as null. Arrays are required too: a producer
// must set every slice field to a non-nil (possibly empty) slice, since a nil slice encodes as
// null. Decode enforces the shape of that contract (every required property present, null only
// where the field is a pointer, no undeclared property); value constraints such as enums,
// formats and ranges are the schema validator's job, not this package's.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// ProtocolVersion is the "v" every machine payload carries.
const ProtocolVersion = 1

// Posture is one vendor's subscription budget as the Mac last saw it.
type Posture struct {
	Level       string   `json:"level"`
	FiveHourPct *float64 `json:"five_hour_pct"`
	WeeklyPct   *float64 `json:"weekly_pct"`
	ResetsAt    *string  `json:"resets_at"`
	// Credits is optional and OpenAI's only; nil when Codex reported none.
	Credits *Credits `json:"credits,omitempty"`
}

// Credits is the OpenAI credits fact: ChatGPT credits Codex spends once the plan limit is
// reached, and banked window resets, which are not money. HasCredits, Unlimited and Balance
// are nil when Codex reported no credits object.
type Credits struct {
	Balance          *string `json:"balance"`
	HasCredits       *bool   `json:"has_credits"`
	Unlimited        *bool   `json:"unlimited"`
	PlanLimitReached bool    `json:"plan_limit_reached"`
	BankedResets     int     `json:"banked_resets"`
}

// Run is how a project is being driven: a headless conductor lane, an interactive session, or none.
type Run struct {
	Kind           string  `json:"kind"`
	Since          *string `json:"since"`
	LastActivityAt *string `json:"last_activity_at"`
}

// Progress is slices done of total.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// CiRun is the latest CI run of one repository workflow.
type CiRun struct {
	Repo       string  `json:"repo"`
	Workflow   string  `json:"workflow"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	URL        *string `json:"url"`
	At         string  `json:"at"`
}

// Event is a recent mailbox message, reduced to its headline.
type Event struct {
	ID      string `json:"id"`
	At      string `json:"at"`
	Kind    string `json:"kind"`
	From    string `json:"from"`
	Subject string `json:"subject"`
}

// Question is an open question for the human, from the project mailbox.
type Question struct {
	ID          string  `json:"id"`
	At          string  `json:"at"`
	From        string  `json:"from"`
	Subject     string  `json:"subject"`
	Body        string  `json:"body"`
	Recommended *string `json:"recommended"`
	Blocking    bool    `json:"blocking"`
}

// ProjectSnapshot is one project's state as pushed by the Mac.
type ProjectSnapshot struct {
	Key          string     `json:"key"`
	Name         string     `json:"name"`
	State        string     `json:"state"`
	StateReason  string     `json:"state_reason"`
	Door         *string    `json:"door"`
	Run          Run        `json:"run"`
	Slice        *string    `json:"slice"`
	Now          *string    `json:"now"`
	Progress     *Progress  `json:"progress"`
	CI           []CiRun    `json:"ci"`
	Events       []Event    `json:"events"`
	Questions    []Question `json:"questions"`
	SnapshotHash string     `json:"snapshot_hash"`
}

// Machine identifies the Mac in a push.
type Machine struct {
	ID          string `json:"id"`
	OrchVersion string `json:"orch_version"`
	SentAt      string `json:"sent_at"`
}

// Budget holds both vendors' postures; either is null when the Mac has no reading.
type Budget struct {
	Anthropic *Posture `json:"anthropic"`
	OpenAI    *Posture `json:"openai"`
}

// PushRequest is Mac -> Worker: the changed project snapshots.
type PushRequest struct {
	V        int               `json:"v"`
	Machine  Machine           `json:"machine"`
	Budget   *Budget           `json:"budget"`
	Projects []ProjectSnapshot `json:"projects"`
}

// PushResult is the Worker's verdict on one pushed project.
type PushResult struct {
	Key               string `json:"key"`
	Result            string `json:"result"`
	RetryAfterSeconds *int   `json:"retry_after_seconds"`
}

// PushResponse is Worker -> Mac: the per-project result of a push.
type PushResponse struct {
	V          int          `json:"v"`
	ServerTime string       `json:"server_time"`
	Projects   []PushResult `json:"projects"`
}

// PulledAnswer is an answer past its undo window, waiting to be applied on the Mac.
type PulledAnswer struct {
	ID         string `json:"id"`
	ProjectKey string `json:"project_key"`
	QuestionID string `json:"question_id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	CreatedAt  string `json:"created_at"`
}

// PulledAction is a pause or resume past its undo window, waiting to be applied on the Mac.
type PulledAction struct {
	ID         string `json:"id"`
	ProjectKey string `json:"project_key"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"created_at"`
}

// PullResponse is Worker -> Mac: the answers and actions not yet acked.
type PullResponse struct {
	V                int            `json:"v"`
	ServerTime       string         `json:"server_time"`
	PollAfterSeconds int            `json:"poll_after_seconds"`
	Answers          []PulledAnswer `json:"answers"`
	Actions          []PulledAction `json:"actions"`
}

// AckResult is the outcome of applying one pulled answer or action.
type AckResult struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

// AckRequest is Mac -> Worker: the outcomes of what was pulled.
type AckRequest struct {
	V       int         `json:"v"`
	Results []AckResult `json:"results"`
}

// AckResponse is Worker -> Mac: how many ack results changed state.
type AckResponse struct {
	V       int `json:"v"`
	Settled int `json:"settled"`
}

// ErrTrailingData is returned by Decode when the reader holds more than one JSON value.
var ErrTrailingData = errors.New("wire: unexpected data after the JSON value")

// ShapeError is a payload that leaves out a required property, sets a non-nullable one to null
// or carries a property whose name is not exactly a declared one (a case variant such as
// "Settled" included). Path names the property, as in "$.projects[0].run.kind".
type ShapeError struct {
	Path    string
	Problem string
}

func (e *ShapeError) Error() string { return fmt.Sprintf("wire: %s: %s", e.Path, e.Problem) }

// Decode reads exactly one JSON value from r into v, a pointer to one of this package's
// structs. It refuses a property the struct does not declare (the schema forbids additional
// properties everywhere, and encoding/json would otherwise accept a case variant of a declared
// name), a missing property and a null where the field is not a pointer, since encoding/json
// alone would decode both of those to the zero value.
func Decode(r io.Reader, v any) error {
	decoder := json.NewDecoder(r)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("wire: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrTrailingData
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(v); err != nil {
		return fmt.Errorf("wire: %w", err)
	}
	return checkProperty(raw, reflect.TypeOf(v).Elem(), "$")
}

// checkShape walks raw, which already decoded into a value of type t, and reports the first
// undeclared property, or required property missing or null where t has no pointer for it.
func checkShape(raw json.RawMessage, t reflect.Type, path string) error {
	if isNull(raw) {
		return nil // the caller has checked that null is allowed here
	}
	switch t.Kind() {
	case reflect.Pointer:
		return checkShape(raw, t.Elem(), path)
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for i, item := range items {
			if err := checkProperty(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(raw, &properties); err != nil {
			return err
		}
		if err := refuseUndeclared(properties, t, path); err != nil {
			return err
		}
		for i := range t.NumField() {
			field := t.Field(i)
			name := propertyName(field)
			value, present := properties[name]
			propertyPath := path + "." + name
			if !present && isOptional(field) {
				continue
			}
			if !present {
				return &ShapeError{Path: propertyPath, Problem: "required property is missing"}
			}
			if err := checkProperty(value, field.Type, propertyPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// refuseUndeclared reports the first property of an object whose name is not exactly one of
// t's JSON tags. encoding/json matches names case-insensitively, so without this check
// {"settled":0,"Settled":99} would pass DisallowUnknownFields and decode Settled as 99.
func refuseUndeclared(properties map[string]json.RawMessage, t reflect.Type, path string) error {
	declared := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		declared[propertyName(t.Field(i))] = true
	}
	for _, name := range slices.Sorted(maps.Keys(properties)) {
		if !declared[name] {
			return &ShapeError{Path: path + "." + name, Problem: "undeclared property"}
		}
	}
	return nil
}

// isOptional reports a field for an optional schema property: one tagged omitempty.
func isOptional(field reflect.StructField) bool {
	_, options, _ := strings.Cut(field.Tag.Get("json"), ",")
	return slices.Contains(strings.Split(options, ","), "omitempty")
}

func propertyName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

// checkProperty refuses a null for a non-pointer type, then checks what is inside the value.
func checkProperty(raw json.RawMessage, t reflect.Type, path string) error {
	if isNull(raw) && t.Kind() != reflect.Pointer {
		return &ShapeError{Path: path, Problem: "must not be null"}
	}
	return checkShape(raw, t, path)
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }
