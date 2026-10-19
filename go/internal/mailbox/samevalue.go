package mailbox

import "math"

// The JS groups acks in a Map keyed by `a.id` and collects `a.by` in a Set; both compare keys
// with SameValueZero. Hand-edited files can hold any JSON value there, so the keys below keep
// the JS distinctions: undefined and null differ, NaN equals NaN, -0 equals 0, and every
// parsed object or array is a distinct key that nothing else equals.

type undefinedKey struct{}

type nullKey struct{}

type nanKey struct{}

// identityKey is allocated per object or array; it is not zero-sized, so distinct pointers
// never compare equal.
type identityKey struct{ _ byte }

func sameValueZeroKey(v any, present bool) any {
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

// orderedSet is a JS Set of values in insertion order. A nil set is empty.
type orderedSet struct {
	seen  map[any]bool
	items []any
}

func (s *orderedSet) add(v any, present bool) {
	key := sameValueZeroKey(v, present)
	if s.seen[key] {
		return
	}
	s.seen[key] = true
	s.items = append(s.items, v) // undefined is stored as nil and serialises as null, as in JS arrays
}

func (s *orderedSet) has(v string) bool {
	return s != nil && s.seen[v]
}

// values is [...set]; an empty set gives [], never null.
func (s *orderedSet) values() []any {
	if s == nil {
		return []any{}
	}
	return append([]any{}, s.items...)
}
