// Package jsjson reads and writes JSON exactly the way Node's JSON.parse and JSON.stringify do,
// so files and stdout written by orch are byte-identical to those written by the JS runtime.
//
// Values are a closed set of Go types: *Object, []any, string, float64, bool and nil (JSON null).
// encoding/json cannot be used for passthrough data: it sorts map keys and escapes <, >, &,
// U+2028 and U+2029, none of which JSON.stringify does.
package jsjson

import (
	"slices"
	"sort"
	"strconv"
)

// Object is a JSON object with JavaScript property order: keys that are canonical array
// indices come first in ascending numeric order, then every other key in insertion order.
// Re-assigning an existing key keeps its position. A JS `undefined` value is modelled by not
// setting the key at all, which is why there is no way to store one.
type Object struct {
	keys   []string
	values map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{values: map[string]any{}}
}

// Set assigns value to key, appending the key if it is new.
func (o *Object) Set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Delete removes key, as the JS delete operator does; a key set again later is appended anew.
func (o *Object) Delete(key string) {
	if _, exists := o.values[key]; !exists {
		return
	}
	delete(o.values, key)
	o.keys = slices.DeleteFunc(o.keys, func(k string) bool { return k == key })
}

// Get returns the value stored under key and whether the key is present.
func (o *Object) Get(key string) (any, bool) {
	value, ok := o.values[key]
	return value, ok
}

// Len returns the number of keys.
func (o *Object) Len() int {
	return len(o.keys)
}

// Keys returns the keys in the order JavaScript enumerates them (and JSON.stringify writes them).
func (o *Object) Keys() []string {
	var indices, names []string
	for _, key := range o.keys {
		if isArrayIndex(key) {
			indices = append(indices, key)
		} else {
			names = append(names, key)
		}
	}
	sort.Slice(indices, func(i, j int) bool {
		a, _ := strconv.ParseUint(indices[i], 10, 32)
		b, _ := strconv.ParseUint(indices[j], 10, 32)
		return a < b
	})
	return append(indices, names...)
}

// maxArrayIndex is 2^32 - 2: ECMAScript array indices stop one short of 2^32 - 1, so the key
// "4294967295" is an ordinary string key and keeps its insertion position.
const maxArrayIndex = 1<<32 - 2

// isArrayIndex reports whether key is the canonical decimal form of an array index: "0", or
// digits without a leading zero, no greater than maxArrayIndex ("01" and "-1" are plain keys).
func isArrayIndex(key string) bool {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(key, 10, 64)
	return err == nil && n <= maxArrayIndex
}
