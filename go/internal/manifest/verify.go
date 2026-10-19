package manifest

import (
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// Verification is the outcome of checking a manifest against the files under a root.
type Verification struct {
	Mismatched []string
	Missing    []string
	Checked    int
}

// OK reports whether every recorded file exists with its recorded hash.
func (v Verification) OK() bool {
	return len(v.Mismatched) == 0 && len(v.Missing) == 0
}

// Object is the JSON the JS prints: {ok, mismatched, missing, checked}.
func (v Verification) Object() *jsjson.Object {
	o := jsjson.NewObject()
	o.Set("ok", v.OK())
	o.Set("mismatched", stringValues(v.Mismatched))
	o.Set("missing", stringValues(v.Missing))
	o.Set("checked", float64(v.Checked))
	return o
}

// Verify rehashes each entry of manifest.files (any parsed JSON value) under root, in JS
// property order. An entry whose file does not exist (following symlinks) is missing; one
// whose hash differs from the recorded value, or whose recorded value is not a string, is
// mismatched. A recorded directory fails as reading it does in Node (EISDIR).
func Verify(root string, manifest any) (Verification, error) {
	files, _, err := filesOf(manifest)
	if err != nil {
		return Verification{}, err
	}
	entries, err := ownEntries(files)
	if err != nil {
		return Verification{}, err
	}
	v := Verification{Mismatched: []string{}, Missing: []string{}, Checked: entries.Len()}
	for _, rel := range entries.Keys() {
		path := joinPath(root, rel)
		if !nodefs.Exists(path) {
			v.Missing = append(v.Missing, rel)
			continue
		}
		hash, err := sha256File(path)
		if err != nil {
			return Verification{}, err
		}
		if expected, _ := entries.Get(rel); expected != hash {
			v.Mismatched = append(v.Mismatched, rel)
		}
	}
	return v, nil
}

// filesOf is `manifest.files`: null has no properties to read, and only an object can hold
// files (an array or a primitive gives undefined: nil, present false).
func filesOf(manifest any) (files any, present bool, err error) {
	switch m := manifest.(type) {
	case nil:
		return nil, false, &ShapeError{"Cannot read properties of null (reading 'files')"}
	case *jsjson.Object:
		files, present = m.Get("files")
		return files, present, nil
	}
	return nil, false, nil
}

// ownEntries is Object.entries(v) (and Object.keys(v)) for a value read with filesOf: null
// and undefined throw.
func ownEntries(v any) (*jsjson.Object, error) {
	if v == nil {
		return nil, &ShapeError{"Cannot convert undefined or null to object"}
	}
	return jsjson.Spread(v), nil
}

func stringValues(items []string) []any {
	values := make([]any, len(items))
	for i, item := range items {
		values[i] = item
	}
	return values
}
