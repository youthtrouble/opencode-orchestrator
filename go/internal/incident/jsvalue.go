package incident

import (
	"math"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// Entries read back may be any JSON value, and the JS reads their properties regardless. Of
// the properties it reads, only `at` exists on a non-object: strings and arrays inherit an
// `at` method, which compares and prints as its source text, and which JSON.stringify omits.

// nativeMethod is an inherited method read as a property; the string is its source text.
type nativeMethod string

const atMethod nativeMethod = "function at() { [native code] }"

// property is `e[key]` for an entry: the value and whether it is defined.
func property(e any, key string) (any, bool) {
	switch e.(type) {
	case string, []any:
		if key == "at" {
			return atMethod, true
		}
		return nil, false
	}
	return jsjson.Lookup(e, key)
}

// relationalOperand is the primitive the JS compares a property value as: undefined is NaN
// and a method its source text. Objects and arrays are not modelled (PE-6).
func relationalOperand(v any, present bool) any {
	switch v := v.(type) {
	case nativeMethod:
		return string(v)
	case nil:
		if !present {
			return math.NaN()
		}
	}
	return v
}

// propertyIs is `e[key] === want` for an option value want: a string, or true for a bare flag.
func propertyIs(e any, key string, want any) bool {
	v, present := property(e, key)
	return present && isPrimitiveJSON(v) && v == want
}

func isPrimitiveJSON(v any) bool {
	switch v.(type) {
	case nil, bool, float64, string:
		return true
	}
	return false
}

// templateText is `${v}` for a property value.
func templateText(v any, present bool) string {
	if method, isMethod := v.(nativeMethod); isMethod {
		return string(method)
	}
	return jsjson.TemplateStringOf(v, present)
}
