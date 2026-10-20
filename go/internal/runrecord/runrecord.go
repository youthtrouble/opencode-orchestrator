// Package runrecord writes the compact JSON record every delegated run (codex exec, claude -p)
// leaves under <project>/.orchestrator/runs/, so any agent or human can audit what was asked,
// what came back and what it cost (lib/run-record.mjs writeRunRecord). File names and bytes
// match the JS runtime's.
package runrecord

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
)

// ErrNoStringMethod rejects a truthy field the JS would throw a TypeError on: a started_at
// that is not a string (no .replace), or a chosen thread_id/session_id that is neither a
// string nor an array (no .slice).
var ErrNoStringMethod = errors.New("run record field lacks the string method the JS calls")

// Write stores record, indented by two spaces plus a newline, at
// <projectRoot>/.orchestrator/runs/<stamp>-<harness>-<id>.json, replacing any file of that
// name, and returns that path. stamp is started_at (now when it is absent or empty) with every
// ':' and '.' replaced by '-'; harness is `${record.harness}` ("undefined" when absent); id is
// runID's. Every '/' in the name becomes '_', so a session id cannot place the file outside
// runs/. Records hold prompts and answers, so runs/ and the record are owner-only (PE-21).
func Write(projectRoot string, record *jsjson.Object, now time.Time) (string, error) {
	dir := paths.RunsDir(projectRoot)
	if err := nodefs.MkdirPrivate(dir); err != nil {
		return "", err
	}
	startedAt, err := stringOr(record, "started_at", jsjson.ISOString(now))
	if err != nil {
		return "", err
	}
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(startedAt)
	id, err := runID(record)
	if err != nil {
		return "", err
	}
	harness := jsjson.TemplateStringOf(jsjson.Lookup(record, "harness"))
	data, err := jsjson.MarshalIndent(record, "  ")
	if err != nil {
		return "", err
	}
	name := strings.ReplaceAll(fmt.Sprintf("%s-%s-%s", stamp, harness, id), "/", "_")
	file := filepath.Join(dir, name+".json")
	if err := nodefs.WritePrivateFile(file, append(data, '\n')); err != nil {
		return "", err
	}
	return file, nil
}

// stringOr is `record[key] || fallback` for a field that must be a string when truthy.
func stringOr(record *jsjson.Object, key, fallback string) (string, error) {
	value := jsjson.Path(record, key)
	if !jsjson.Truthy(value) {
		return fallback, nil
	}
	s, isString := value.(string)
	if !isString {
		return "", fmt.Errorf("%w: %s", ErrNoStringMethod, key)
	}
	return s, nil
}

// runID is `${(record.thread_id || record.session_id || "run").slice(0, 8)}`: the first truthy
// value wins (an empty array is truthy, so it never falls through), a string keeps its first 8
// UTF-16 units and an array its first 8 elements, joined the way a template literal prints it.
func runID(record *jsjson.Object) (string, error) {
	for _, key := range []string{"thread_id", "session_id"} {
		switch value := jsjson.Path(record, key).(type) {
		case string:
			if value != "" {
				return jsstr.Slice(value, 0, 8), nil
			}
		case []any:
			return jsjson.TemplateString(value[:min(len(value), 8)]), nil
		default:
			if jsjson.Truthy(value) {
				return "", fmt.Errorf("%w: %s", ErrNoStringMethod, key)
			}
		}
	}
	return "run", nil
}
