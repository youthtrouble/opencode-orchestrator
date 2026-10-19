// Package jsonl reads and appends the JSON Lines state files that orch and the JS runtime share
// (readJsonl and appendJsonl in lib/mailbox.mjs; lib/incident.mjs inlines the same two). Both
// runtimes append to the same files concurrently, so each line is written with a single write
// on an O_APPEND descriptor and the files are never rewritten.
package jsonl

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// Read mirrors readJsonl: a missing file is empty; each non-empty line is JSON.parse'd and kept
// unless it fails to parse or its value is falsy (0, false, null, ""). Every other value is
// kept whatever its shape, since the files may be edited by hand.
func Read(file string) ([]any, error) {
	if !nodefs.Exists(file) {
		return nil, nil
	}
	data, err := nodefs.ReadFile(file)
	if err != nil {
		return nil, err
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

// Append mirrors appendJsonl: create the parent directories, then append the compact JSON of
// value and a newline in one write, creating the file with mode 0666 less the umask.
func Append(file string, value *jsjson.Object) error {
	line, err := jsjson.Marshal(value)
	if err != nil {
		return err
	}
	if err := nodefs.MkdirAll(filepath.Dir(file)); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return nodefs.FileError(err, "open", file)
	}
	// One write per line, so lines appended concurrently by orch and the JS never interleave.
	_, writeErr := f.Write(append(line, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return nodefs.FileError(writeErr, "write", "")
	}
	if closeErr != nil {
		return nodefs.FileError(closeErr, "close", "")
	}
	return nil
}
