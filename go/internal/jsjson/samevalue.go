package jsjson

import "math"

// JS Map keys and Set members compare with SameValueZero. Hand-edited files can hold any JSON
// value where the JS uses one as a key, so the keys below keep the JS distinctions: undefined
// and null differ, NaN equals NaN, -0 equals 0, and every parsed object or array is a distinct
// key that nothing else equals.

type undefinedKey struct{}

type nullKey struct{}

type nanKey struct{}

// identityKey is allocated per object or array; it is not zero-sized, so distinct pointers
// never compare equal.
type identityKey struct{ _ byte }

// SameValueZeroKey returns a Go map key that equals another exactly when the JS values
// (read with Lookup: present false is undefined) are SameValueZero.
func SameValueZeroKey(v any, present bool) any {
	switch v := v.(type) {
	case nil:
		if !present {
			return undefinedKey{}
		}
		return nullKey{}
	case float64:
		if math.IsNaN(v) {
			return nanKey{}
		}
		return v // Go map keys already equate -0 and +0
	case string, bool:
		return v
	}
	return new(identityKey)
}

// OrderedSet is a JS Set of values in insertion order. The zero value and a nil set are empty.
type OrderedSet struct {
	seen  map[any]bool
	items []any
}

// Add is set.add(v) for a value read with Lookup.
func (s *OrderedSet) Add(v any, present bool) {
	key := SameValueZeroKey(v, present)
	if s.seen[key] {
		return
	}
	if s.seen == nil {
		s.seen = map[any]bool{}
	}
	s.seen[key] = true
	s.items = append(s.items, v) // undefined is stored as nil and serialises as null, as in JS arrays
}

// Has is set.has(v) for a string.
func (s *OrderedSet) Has(v string) bool {
	return s != nil && s.seen[v]
}

// Values is [...set]; an empty set gives [], never null.
func (s *OrderedSet) Values() []any {
	if s == nil {
		return []any{}
	}
	return append([]any{}, s.items...)
}
