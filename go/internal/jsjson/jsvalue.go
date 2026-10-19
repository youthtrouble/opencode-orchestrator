package jsjson

import (
	"math"
	"strings"
)

// Lookup follows keys through nested objects the way JS optional chaining (v?.a?.b) does:
// it returns the value and true when every step is an object holding the key, and nil and
// false (JS undefined) as soon as a step is missing or not an object. A present JSON null
// is returned as nil and true.
func Lookup(v any, keys ...string) (any, bool) {
	for _, key := range keys {
		obj, isObject := v.(*Object)
		if !isObject || obj == nil {
			return nil, false
		}
		next, present := obj.Get(key)
		if !present {
			return nil, false
		}
		v = next
	}
	return v, true
}

// Path is Lookup without the presence flag, for the many JS reads where undefined and null
// behave the same (v?.a ?? x, v?.a || x, if (v?.a)).
func Path(v any, keys ...string) any {
	value, _ := Lookup(v, keys...)
	return value
}

// Truthy reports JS truthiness: null (and therefore an absent value), false, 0, NaN and ""
// are falsy; every object and array is truthy.
func Truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v == v && v != 0 // v == v excludes NaN
	case string:
		return v != ""
	default:
		return true
	}
}

// TemplateStringOf is TemplateString for a value read with Lookup: an absent value (present
// false, JS undefined) prints "undefined".
func TemplateStringOf(v any, present bool) string {
	if !present {
		return "undefined"
	}
	return TemplateString(v)
}

// TemplateString returns String(v), the text a JS template literal `${v}` inserts, for a
// present value; null prints "null". Use TemplateStringOf where the value may be absent.
func TemplateString(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		switch {
		case math.IsNaN(v):
			return "NaN"
		case math.IsInf(v, 1):
			return "Infinity"
		case math.IsInf(v, -1):
			return "-Infinity"
		}
		return formatNumber(v)
	case string:
		return v
	case []any:
		return Join(v, ",")
	default:
		return "[object Object]"
	}
}

// Join is Array.prototype.join(sep): elements by String(), null (and undefined) as "".
func Join(items []any, sep string) string {
	parts := make([]string, len(items))
	for i, item := range items {
		if item != nil {
			parts[i] = TemplateString(item)
		}
	}
	return strings.Join(parts, sep)
}

// OrText is `${v || fallback}`: String(v) when v is truthy, else fallback.
func OrText(v any, fallback string) string {
	if Truthy(v) {
		return TemplateString(v)
	}
	return fallback
}

// NumberOr reads v?.[key] as a number: the value when it is a number, else fallback. It
// stands for both `v?.[key] ?? fallback` and a numeric read whose non-numbers act as
// fallback (NaN, say); the JS coercion of numeric strings is not modelled, as the documents
// read this way never hold them.
func NumberOr(v any, key string, fallback float64) float64 {
	if n, isNumber := Path(v, key).(float64); isNumber {
		return n
	}
	return fallback
}
