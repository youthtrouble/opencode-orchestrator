// Package registry loads models.json, the single source of truth for vendors, tiers, roles
// and thresholds. The parsed document keeps the file's key order, because it is passed
// through verbatim (budget refusals embed it as routing.registry).
package registry

import (
	"fmt"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// NotObjectError reports a registry file whose top-level JSON value is not an object.
type NotObjectError struct {
	File string
}

func (e *NotObjectError) Error() string {
	return fmt.Sprintf("registry: %s: top-level value is not an object", e.File)
}

// Load mirrors loadRegistry in lib/usage.mjs: JSON.parse of the whole file. A read failure is
// worded as Node words it ("ENOENT: no such file or directory, open '<file>'"), because
// commands that report the routing error (doctor's FAIL usage/routing row) print it verbatim.
func Load(file string) (*jsjson.Object, error) {
	data, err := nodefs.ReadFile(file)
	if err != nil {
		return nil, err
	}
	value, err := jsjson.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("registry: %s: %w", file, err)
	}
	doc, ok := value.(*jsjson.Object)
	if !ok {
		return nil, &NotObjectError{File: file}
	}
	return doc, nil
}
