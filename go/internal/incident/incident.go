// Package incident is the orchestrator's incident log (lib/incident.mjs): an append-only JSON
// Lines file in the user's state directory, shared with the JS runtime, whose entries are
// redacted and fingerprinted so that repeats of one problem group together. It stays local;
// the CLI's share command writes a sanitized bundle the user may attach to an issue by hand.
//
// The file may be edited by hand, so values of any shape are read back. Where a shape makes
// the JS throw a TypeError, the functions here return a *ShapeError.
package incident

import (
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsonl"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/version"
)

// Kinds are the incident kinds; any other kind is recorded as "note".
var Kinds = []string{"bug", "limit", "misroute", "permission", "crash", "timeout", "contract", "human-correction", "flip", "note"}

// ErrIncomplete is the JS error for a record without component or summary.
var ErrIncomplete = errors.New("incident needs component and summary")

// ShapeError reports a hand-edited value whose shape makes the JS throw a TypeError. The
// message approximates V8's wording; only the error class is part of the contract.
type ShapeError struct {
	Detail string
}

func (e *ShapeError) Error() string { return e.Detail }

// maxSummary is where a recorded summary is cut, in UTF-16 code units.
const maxSummary = 300

// Log is the incident log of one user. Home is what redaction replaces with "~"; Version is
// written into each entry (orch's version, where the JS writes its checkout's git HEAD: PE-4);
// Now is the clock. Tests replace all three.
type Log struct {
	File    string
	Home    string
	Version string
	Now     func() time.Time
}

// New returns the log of the user whose home is home, redacting home. Pass paths.NodeHome:
// an explicitly empty HOME then gives the JS's relative log path and empty-home redaction,
// never the user's own log.
func New(home string) *Log {
	return &Log{File: paths.IncidentsFileIn(home), Home: home, Version: version.String(), Now: time.Now}
}

// Draft is an incident to record.
type Draft struct {
	// Component is the reporting part (plugin, bridge, worker, ...). It is a string, except
	// that the CLI passes true for a bare --component, which the JS writes as JSON true.
	Component any
	// Kind is one of Kinds; "" is "bug" (the JS default) and anything else unknown is "note".
	Kind    string
	Summary string
	Detail  string
	// Context holds extra facts; its string values are redacted. Nil is empty.
	Context *jsjson.Object
	// Project is a path whose basename is recorded; "" records null.
	Project string
}

// Validate reports ErrIncomplete for a draft the JS would refuse: a falsy component or an
// empty summary.
func (d Draft) Validate() error {
	if !jsjson.Truthy(d.Component) || d.Summary == "" {
		return ErrIncomplete
	}
	return nil
}

// Record appends the redacted, fingerprinted entry for d and returns it. Its key order is the
// JS one: at, fp, component, kind, summary, detail, context, project, version, platform.
func (l *Log) Record(d Draft) (*jsjson.Object, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	kind := d.Kind
	if kind == "" {
		kind = "bug"
	}
	if !slices.Contains(Kinds, kind) {
		kind = "note"
	}
	var project any
	if d.Project != "" {
		project = basename(d.Project)
	}
	entry := jsjson.NewObject()
	entry.Set("at", jsjson.ISOString(l.Now()))
	entry.Set("fp", Fingerprint(l.Home, d.Component, kind, d.Summary))
	entry.Set("component", d.Component)
	entry.Set("kind", kind)
	entry.Set("summary", jsstr.Slice(Redact(l.Home, d.Summary), 0, maxSummary))
	entry.Set("detail", Redact(l.Home, d.Detail))
	entry.Set("context", l.redactedContext(d.Context))
	entry.Set("project", project)
	entry.Set("version", l.Version)
	entry.Set("platform", runtime.GOOS) // process.platform for the two supported systems
	if err := jsonl.Append(l.File, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// TryRecord records d in the log of the user this process runs as (HOME as Node reads it, see
// paths.NodeHome) and ignores every failure, as the JS callers do with
// `try { record(...) } catch { /* ignore */ }`: a bridge or worker never fails because its
// incident log does.
func TryRecord(d Draft) {
	home, err := paths.NodeHome(os.LookupEnv)
	if err != nil {
		return
	}
	_, _ = New(home).Record(d)
}

func (l *Log) redactedContext(context *jsjson.Object) *jsjson.Object {
	redacted := jsjson.NewObject()
	if context == nil {
		return redacted
	}
	for _, key := range context.Keys() {
		value, _ := context.Get(key)
		if s, isString := value.(string); isString {
			value = Redact(l.Home, s)
		}
		redacted.Set(key, value)
	}
	return redacted
}

// basename is Node's path.basename for POSIX paths: trailing slashes are ignored and "/"
// gives "" (filepath.Base gives "/").
func basename(path string) string {
	path = strings.TrimRight(path, "/")
	return path[strings.LastIndexByte(path, '/')+1:]
}

// Filter selects entries for Read. Each field is the JS option value: nil when not given, a
// string, or true for a bare flag, which the JS compares as it is: --kind alone matches only
// an entry whose kind is true, and --since alone compares Number(at) >= 1, which no ISO
// timestamp passes. A falsy value ("") disables the filter.
type Filter struct {
	Since, Kind, Component any
}

// Read returns the entries passing f, in file order, never nil.
func (l *Log) Read(f Filter) ([]any, error) {
	entries, err := jsonl.Read(l.File)
	if err != nil {
		return nil, err
	}
	kept := []any{}
	for _, e := range entries {
		if jsjson.Truthy(f.Since) && !jsjson.GreaterOrEqual(relationalOperand(property(e, "at")), f.Since) {
			continue
		}
		if jsjson.Truthy(f.Kind) && !propertyIs(e, "kind", f.Kind) {
			continue
		}
		if jsjson.Truthy(f.Component) && !propertyIs(e, "component", f.Component) {
			continue
		}
		kept = append(kept, e)
	}
	return kept, nil
}
