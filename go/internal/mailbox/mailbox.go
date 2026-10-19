// Package mailbox is the durable, append-only mailbox between the agents and harnesses working
// on one project (lib/mailbox.mjs). Messages live in <project>/.orchestrator/mailbox/
// messages.jsonl, acknowledgements in acks.jsonl, both read and appended through package jsonl.
//
// The files may be edited by hand, so values of any shape are read back. Where a shape makes
// the JS throw a TypeError, the functions here return a *ShapeError.
package mailbox

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsonl"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
)

// The errors the JS throws, with its messages. ErrAmbiguousID and ErrNoMessage are wrapped
// with the id or prefix asked for, so err.Error() is the full JS message.
var (
	ErrIncompletePost = errors.New("post needs from and subject")
	ErrAmbiguousID    = errors.New("ambiguous message id prefix")
	ErrNoMessage      = errors.New("no message with id")
)

// ShapeError reports a hand-edited value whose shape makes the JS throw a TypeError. The
// message approximates V8's wording; only the error class is part of the contract.
type ShapeError struct {
	Detail string
}

func (e *ShapeError) Error() string { return e.Detail }

// DefaultLimit is how many of the latest messages List returns unless asked otherwise.
const DefaultLimit = 50

// Mailbox reads and appends the mailbox of one project. Now and NewID are the clock and the
// message id source; tests replace them.
type Mailbox struct {
	Dir   string
	Now   func() time.Time
	NewID func() string
}

// New returns the mailbox of the project at projectRoot.
func New(projectRoot string) *Mailbox {
	return &Mailbox{Dir: paths.MailboxDir(projectRoot), Now: time.Now, NewID: NewUUID}
}

// NewUUID returns a random lowercase RFC 4122 version 4 id, as crypto.randomUUID does.
func NewUUID() string {
	var b [16]byte
	rand.Read(b[:]) // never fails: crypto/rand aborts the program rather than return an error
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Draft is a message to post. To is written as given: callers pass a string, except a reply,
// which addresses whatever the original's `from` holds. An empty Thread is written as null.
type Draft struct {
	From    string
	To      any
	Kind    string
	Subject string
	Body    string
	Refs    []string
	Thread  string
}

// Post appends a message with a fresh id and timestamp and returns it.
func (b *Mailbox) Post(d Draft) (*jsjson.Object, error) {
	if d.From == "" || d.Subject == "" {
		return nil, ErrIncompletePost
	}
	refs := make([]any, len(d.Refs))
	for i, ref := range d.Refs {
		refs[i] = ref
	}
	var thread any
	if d.Thread != "" {
		thread = d.Thread
	}
	msg := jsjson.NewObject()
	msg.Set("id", b.NewID())
	msg.Set("at", jsjson.ISOString(b.Now()))
	msg.Set("from", d.From)
	msg.Set("to", d.To)
	msg.Set("kind", d.Kind)
	msg.Set("subject", d.Subject)
	msg.Set("body", d.Body)
	msg.Set("refs", refs)
	msg.Set("thread", thread)
	if err := jsonl.Append(filepath.Join(b.Dir, "messages.jsonl"), msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// ResolveID accepts a full id or a unique prefix of one (list prints 8-character prefixes)
// and returns the full id. An exact match wins, even over other messages it prefixes.
func (b *Mailbox) ResolveID(idOrPrefix string) (string, error) {
	id, err := b.resolveID(&idOrPrefix)
	if err != nil {
		return "", err
	}
	return *id, nil
}

// resolveID is resolveId with a possibly undefined argument (nil), which the CLI passes for
// `ack` without --id: it matches exactly any message without an id, resolving to undefined,
// and otherwise behaves as the prefix "undefined".
func (b *Mailbox) resolveID(idOrPrefix *string) (*string, error) {
	msgs, err := jsonl.Read(filepath.Join(b.Dir, "messages.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		id, present := jsjson.Lookup(m, "id")
		if idOrPrefix == nil && !present {
			return nil, nil
		}
		if s, isString := id.(string); isString && idOrPrefix != nil && s == *idOrPrefix {
			return &s, nil
		}
	}
	prefix := "undefined"
	if idOrPrefix != nil {
		prefix = *idOrPrefix
	}
	var matches []string
	for _, m := range msgs {
		id, present := jsjson.Lookup(m, "id")
		s, isString := id.(string)
		if !isString {
			return nil, &ShapeError{fmt.Sprintf("message id %s has no startsWith", jsjson.TemplateStringOf(id, present))}
		}
		if strings.HasPrefix(s, prefix) {
			matches = append(matches, s)
		}
	}
	switch {
	case len(matches) == 1:
		return &matches[0], nil
	case len(matches) > 1:
		return nil, fmt.Errorf("%w %s", ErrAmbiguousID, prefix)
	}
	return nil, fmt.Errorf("%w %s", ErrNoMessage, prefix)
}

// AckRequest acknowledges a message. ID is a full id or unique prefix; nil ID and By stand for
// a JS undefined: an absent By omits the key from the entry.
type AckRequest struct {
	ID   *string
	By   *string
	Note string
}

// Ack resolves the id and appends an acknowledgement entry {id, by, at, note}, returning it.
// Acknowledging twice appends twice.
func (b *Mailbox) Ack(r AckRequest) (*jsjson.Object, error) {
	id, err := b.resolveID(r.ID)
	if err != nil {
		return nil, err
	}
	entry := jsjson.NewObject()
	if id != nil {
		entry.Set("id", *id)
	}
	if r.By != nil {
		entry.Set("by", *r.By)
	}
	entry.Set("at", jsjson.ISOString(b.Now()))
	entry.Set("note", r.Note)
	if err := jsonl.Append(filepath.Join(b.Dir, "acks.jsonl"), entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// Query selects messages for List. An empty string disables that filter.
type Query struct {
	To        string // addressed to this recipient or to "*"
	Kind      string
	Since     string // `at` later than this, compared as JS `>`
	UnackedBy string // not acknowledged by this name
	Limit     float64
}

// List returns the latest Limit messages matching q, oldest first, each a copy carrying
// `acked_by`: the distinct `by` values of its acks in first-ack order (an absent `by` is
// null). Limit follows JS slice(-limit): 0, NaN and +Inf keep everything, a negative limit
// drops that many of the oldest, -Inf keeps nothing.
func (b *Mailbox) List(q Query) ([]*jsjson.Object, error) {
	msgs, err := jsonl.Read(filepath.Join(b.Dir, "messages.jsonl"))
	if err != nil {
		return nil, err
	}
	acks, err := jsonl.Read(filepath.Join(b.Dir, "acks.jsonl"))
	if err != nil {
		return nil, err
	}
	ackedBy := map[any]*jsjson.OrderedSet{}
	for _, a := range acks {
		key := jsjson.SameValueZeroKey(jsjson.Lookup(a, "id"))
		if ackedBy[key] == nil {
			ackedBy[key] = &jsjson.OrderedSet{}
		}
		ackedBy[key].Add(jsjson.Lookup(a, "by"))
	}
	ackSetOf := func(m any) *jsjson.OrderedSet { return ackedBy[jsjson.SameValueZeroKey(jsjson.Lookup(m, "id"))] }

	var selected []any
	for _, m := range msgs {
		if q.To != "" && !stringIs(m, "to", "*") && !stringIs(m, "to", q.To) {
			continue
		}
		if q.Kind != "" && !stringIs(m, "kind", q.Kind) {
			continue
		}
		if q.Since != "" && !jsjson.GreaterThan(atOperand(m), q.Since) {
			continue
		}
		if q.UnackedBy != "" && ackSetOf(m).Has(q.UnackedBy) {
			continue
		}
		selected = append(selected, m)
	}

	start := sliceStart(-q.Limit, len(selected))
	out := make([]*jsjson.Object, 0, len(selected)-start)
	for _, m := range selected[start:] {
		copied := jsjson.Spread(m)
		copied.Set("acked_by", ackSetOf(m).Values())
		out = append(out, copied)
	}
	return out, nil
}

func stringIs(m any, key, want string) bool {
	s, isString := jsjson.Path(m, key).(string)
	return isString && s == want
}

// atOperand is `m.at` as the since filter compares it. Strings and arrays inherit an `at`
// method, which compares as its source text; an absent value compares as NaN.
func atOperand(m any) any {
	switch m.(type) {
	case string, []any:
		return "function at() { [native code] }"
	}
	at, present := jsjson.Lookup(m, "at")
	if !present {
		return math.NaN()
	}
	return at
}

// sliceStart is the start index Array.prototype.slice(start) uses: ToIntegerOrInfinity of
// start (NaN is 0, fractions truncate toward zero), counted from the end when negative, then
// clamped to [0, length].
func sliceStart(start float64, length int) int {
	if math.IsNaN(start) {
		return 0
	}
	start = math.Trunc(start) // -0.4 becomes -0, which is not negative
	if start < 0 {
		return length - int(min(-start, float64(length)))
	}
	return int(min(start, float64(length)))
}
