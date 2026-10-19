// Package mailbox is the durable, append-only mailbox between the agents and harnesses working
// on one project (lib/mailbox.mjs). Messages live in <project>/.orchestrator/mailbox/
// messages.jsonl, acknowledgements in acks.jsonl; both runtimes append to the same files, so
// each line is written with a single write on an O_APPEND descriptor and the files are never
// rewritten.
//
// The files may be edited by hand. Reading mirrors the JS: unparsable lines and falsy JSON
// values are dropped, every other value is kept whatever its shape. Where a shape makes the JS
// throw a TypeError, the functions here return a *ShapeError.
package mailbox

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
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
	if err := b.append("messages.jsonl", msg); err != nil {
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
	msgs, err := b.read("messages.jsonl")
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
	if err := b.append("acks.jsonl", entry); err != nil {
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
	msgs, err := b.read("messages.jsonl")
	if err != nil {
		return nil, err
	}
	acks, err := b.read("acks.jsonl")
	if err != nil {
		return nil, err
	}
	ackedBy := map[any]*orderedSet{}
	for _, a := range acks {
		key := sameValueZeroKey(jsjson.Lookup(a, "id"))
		if ackedBy[key] == nil {
			ackedBy[key] = &orderedSet{seen: map[any]bool{}}
		}
		ackedBy[key].add(jsjson.Lookup(a, "by"))
	}
	ackSetOf := func(m any) *orderedSet { return ackedBy[sameValueZeroKey(jsjson.Lookup(m, "id"))] }

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
		if q.UnackedBy != "" && ackSetOf(m).has(q.UnackedBy) {
			continue
		}
		selected = append(selected, m)
	}

	start := sliceStart(-q.Limit, len(selected))
	out := make([]*jsjson.Object, 0, len(selected)-start)
	for _, m := range selected[start:] {
		copied := jsjson.Spread(m)
		copied.Set("acked_by", ackSetOf(m).values())
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

// read mirrors readJsonl: a missing file is empty; each non-empty line is JSON.parse'd and
// kept unless it fails to parse or its value is falsy.
func (b *Mailbox) read(name string) ([]any, error) {
	file := filepath.Join(b.Dir, name)
	if _, err := os.Stat(file); err != nil {
		return nil, nil // fs.existsSync: any stat failure counts as missing
	}
	data, err := os.ReadFile(file)
	if err != nil {
		operation := "read"
		var pathError *os.PathError
		if errors.As(err, &pathError) && pathError.Op == "open" {
			operation = "open"
		}
		return nil, nodeFileError(err, operation, file)
	}
	var values []any
	for _, line := range strings.Split(jsstr.DecodeUTF8(data), "\n") {
		if line == "" {
			continue
		}
		if v, err := jsjson.Unmarshal([]byte(line)); err == nil && jsjson.Truthy(v) {
			values = append(values, v)
		}
	}
	return values, nil
}

func (b *Mailbox) append(name string, value *jsjson.Object) error {
	line, err := jsjson.Marshal(value)
	if err != nil {
		return err
	}
	if err := mkdirRecursive(b.Dir); err != nil {
		return nodeFileError(err, "mkdir", b.Dir)
	}
	f, err := os.OpenFile(filepath.Join(b.Dir, name), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return nodeFileError(err, "open", filepath.Join(b.Dir, name))
	}
	// One write per line, so lines appended concurrently by orch and the JS never interleave.
	_, writeErr := f.Write(append(line, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return nodeFileError(writeErr, "write", "")
	}
	if closeErr != nil {
		return nodeFileError(closeErr, "close", "")
	}
	return nil
}

// mkdirRecursive mirrors Node's fs.mkdirSync(dir, {recursive: true}) (MKDirpSync in
// node_file.cc) so the errno matches Node's: os.MkdirAll stats before creating, so a regular file
// at dir reports ENOTDIR and a dangling or looping symlink reports EEXIST, where Node reports
// EEXIST, ENOENT and ELOOP. Node reports every failure against the requested dir; the caller does.
func mkdirRecursive(dir string) error {
	pending := []string{dir}
	for len(pending) > 0 {
		path := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		var errno syscall.Errno // stays 0 when Mkdir succeeds
		if err := os.Mkdir(path, 0o777); err != nil && !errors.As(err, &errno) {
			return err
		}
		switch errno {
		case 0:
		case syscall.EACCES, syscall.ENOSPC, syscall.ENOTDIR, syscall.EPERM:
			return errno
		case syscall.ENOENT:
			parent := filepath.Dir(path)
			if parent == path {
				return syscall.EEXIST
			}
			pending = append(pending, path, parent)
		default:
			info, statErr := os.Stat(path) // follows symlinks, like uv_fs_stat
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				if errno == syscall.EEXIST && len(pending) > 0 {
					return syscall.ENOTDIR
				}
				return syscall.EEXIST
			}
		}
	}
	return nil
}

// nodeErrnoMessages spells each errno the way Node's uvException does. Errnos outside this
// table keep Go's own error text.
var nodeErrnoMessages = map[syscall.Errno]string{
	syscall.EBADF:        "EBADF: bad file descriptor",
	syscall.EACCES:       "EACCES: permission denied",
	syscall.EPERM:        "EPERM: operation not permitted",
	syscall.EISDIR:       "EISDIR: illegal operation on a directory",
	syscall.ENOTDIR:      "ENOTDIR: not a directory",
	syscall.ENOENT:       "ENOENT: no such file or directory",
	syscall.EEXIST:       "EEXIST: file already exists",
	syscall.ENOSPC:       "ENOSPC: no space left on device",
	syscall.EROFS:        "EROFS: read-only file system",
	syscall.EMFILE:       "EMFILE: too many open files",
	syscall.ELOOP:        "ELOOP: too many symbolic links encountered",
	syscall.ENAMETOOLONG: "ENAMETOOLONG: name too long",
	syscall.ENFILE:       "ENFILE: file table overflow",
	syscall.EIO:          "EIO: i/o error",
	syscall.EFBIG:        "EFBIG: file too large",
	syscall.EINTR:        "EINTR: interrupted system call",
	syscall.ENOMEM:       "ENOMEM: not enough memory",
	syscall.EINVAL:       "EINVAL: invalid argument",
	syscall.EBUSY:        "EBUSY: resource busy or locked",
	syscall.ETXTBSY:      "ETXTBSY: text file is busy",
}

// nodeFileError translates a filesystem failure into the message Node prints for the same
// operation, reported against the path Node names.
func nodeFileError(err error, operation, path string) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	message, ok := nodeErrnoMessages[errno]
	if !ok {
		return err
	}
	if operation == "read" || path == "" {
		return fmt.Errorf("%s, %s", message, operation)
	}
	return fmt.Errorf("%s, %s '%s'", message, operation, path)
}
