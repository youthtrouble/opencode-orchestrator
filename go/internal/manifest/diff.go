package manifest

import (
	"slices"
	"strconv"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// Difference compares the files of two manifests.
type Difference struct {
	Added     []string
	Removed   []string
	Changed   []string
	Unchanged int
}

// Object is the JSON the JS prints: {added, removed, changed, unchanged}.
func (d Difference) Object() *jsjson.Object {
	o := jsjson.NewObject()
	o.Set("added", stringValues(d.Added))
	o.Set("removed", stringValues(d.Removed))
	o.Set("changed", stringValues(d.Changed))
	o.Set("unchanged", float64(d.Unchanged))
	return o
}

// objectPrototype is Object.getOwnPropertyNames(Object.prototype) (Node v26.5.1): the JS
// tests membership with `in`, so these names are present in every files object.
var objectPrototype = []string{
	"constructor", "__defineGetter__", "__defineSetter__", "hasOwnProperty", "__lookupGetter__",
	"__lookupSetter__", "isPrototypeOf", "propertyIsEnumerable", "toString", "valueOf", "__proto__",
	"toLocaleString",
}

// arrayPrototype is Object.getOwnPropertyNames(Array.prototype) (Node v26.5.1), present in a
// files value that is an array.
var arrayPrototype = []string{
	"length", "constructor", "at", "concat", "copyWithin", "fill", "find", "findIndex", "findLast",
	"findLastIndex", "lastIndexOf", "pop", "push", "reverse", "shift", "unshift", "slice", "sort",
	"splice", "includes", "indexOf", "join", "keys", "entries", "values", "forEach", "filter", "flat",
	"flatMap", "map", "every", "some", "reduce", "reduceRight", "toReversed", "toSorted", "toSpliced",
	"with", "toLocaleString", "toString",
}

// Diff compares before.files with after.files (any parsed JSON values) as the JS does: a key
// of after is added unless `key in before.files`, which also holds for names inherited from
// the prototype (`constructor`, `toString`, ...); it is changed when the values are not
// strictly equal (an inherited member equals nothing). unchanged counts the rest of after.
func Diff(before, after any) (Difference, error) {
	afterFiles, afterPresent, err := filesOf(after)
	if err != nil {
		return Difference{}, err
	}
	afterEntries, err := ownEntries(afterFiles)
	if err != nil {
		return Difference{}, err
	}
	// after.files is read first, so its errors win, as in the JS.
	beforeFiles, beforePresent, err := filesOf(before)
	if err != nil {
		return Difference{}, err
	}
	d := Difference{Added: []string{}, Removed: []string{}, Changed: []string{}}
	for _, key := range afterEntries.Keys() {
		value, found, err := member(beforeFiles, beforePresent, key)
		if err != nil {
			return Difference{}, err
		}
		afterValue, _ := afterEntries.Get(key)
		switch {
		case !found:
			d.Added = append(d.Added, key)
		case !strictEqual(value, afterValue):
			d.Changed = append(d.Changed, key)
		}
	}
	beforeEntries, err := ownEntries(beforeFiles)
	if err != nil {
		return Difference{}, err
	}
	for _, key := range beforeEntries.Keys() {
		_, found, err := member(afterFiles, afterPresent, key)
		if err != nil {
			return Difference{}, err
		}
		if !found {
			d.Removed = append(d.Removed, key)
		}
	}
	d.Unchanged = afterEntries.Len() - len(d.Added) - len(d.Changed)
	return d, nil
}

// inherited stands for a prototype member (a function or accessor), which is strictly equal
// to no JSON value.
type inherited struct{}

// member is `key in v` and, when found, `v[key]`. Only objects and arrays can be searched;
// `in` on a primitive, null or undefined throws.
func member(v any, present bool, key string) (value any, found bool, err error) {
	switch v := v.(type) {
	case *jsjson.Object:
		if own, ok := v.Get(key); ok {
			return own, true, nil
		}
	case []any:
		if index, err := strconv.ParseUint(key, 10, 32); err == nil && strconv.FormatUint(index, 10) == key && index < uint64(len(v)) {
			return v[index], true, nil
		}
		if key == "length" {
			return float64(len(v)), true, nil
		}
		if slices.Contains(arrayPrototype, key) {
			return inherited{}, true, nil
		}
	default:
		return nil, false, &ShapeError{"Cannot use 'in' operator to search for '" + key + "' in " + jsjson.TemplateStringOf(v, present)}
	}
	if slices.Contains(objectPrototype, key) {
		return inherited{}, true, nil
	}
	return nil, false, nil
}

// strictEqual is `a !== b` negated for parsed JSON values: primitives compare by value,
// while every object and array is a distinct reference.
func strictEqual(a, b any) bool {
	switch a.(type) {
	case nil, bool, float64, string:
		return a == b
	}
	return false
}
