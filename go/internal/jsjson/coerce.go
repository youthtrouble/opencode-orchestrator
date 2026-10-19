package jsjson

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// Spread returns the object literal `{...v}` builds: the own enumerable properties of v. An
// object is copied in JS key order, an array gives its index keys, a string one key per UTF-16
// code unit (a lone surrogate half becomes U+FFFD, see package jsstr); numbers, booleans and
// null (and so an absent value) give no properties.
func Spread(v any) *Object {
	out := NewObject()
	switch v := v.(type) {
	case *Object:
		for _, key := range v.Keys() {
			value, _ := v.Get(key)
			out.Set(key, value)
		}
	case []any:
		for i, item := range v {
			out.Set(strconv.Itoa(i), item)
		}
	case string:
		for i, unit := range utf16.Encode([]rune(v)) {
			out.Set(strconv.Itoa(i), string(utf16.Decode([]uint16{unit})))
		}
	}
	return out
}

// ToNumber is JS Number(v) for a present primitive: null is 0, booleans are 0 or 1, strings
// are parsed as below. An absent value (JS undefined) is NaN, so callers pass math.NaN() for
// it. Objects and arrays are not modelled and give NaN.
func ToNumber(v any) float64 {
	switch v := v.(type) {
	case nil:
		return 0
	case bool:
		if v {
			return 1
		}
		return 0
	case float64:
		return v
	case string:
		return stringToNumber(v)
	}
	return math.NaN()
}

var (
	decimalLiteral  = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
	infinityLiteral = regexp.MustCompile(`^[+-]?Infinity$`)
	radixLiteral    = regexp.MustCompile(`^0(?:[xX][0-9a-fA-F]+|[oO][0-7]+|[bB][01]+)$`)
	radixes         = map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}
)

// stringToNumber is StringToNumber: JS white space around the literal is ignored, an empty
// literal is 0, 0x/0o/0b literals are unsigned integers, anything else that is not a decimal
// literal or a signed Infinity is NaN.
func stringToNumber(s string) float64 {
	s = strings.Trim(s, jsstr.WhiteSpace)
	switch {
	case s == "":
		return 0
	case infinityLiteral.MatchString(s):
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	case decimalLiteral.MatchString(s):
		// ParseFloat rounds like JS; on overflow it returns ±Inf with ErrRange, which is
		// also the JS result.
		f, _ := strconv.ParseFloat(s, 64)
		return f
	case radixLiteral.MatchString(s):
		// big.Float.Float64 rounds to nearest even and gives +Inf past the float64 range,
		// as the JS conversion of a long literal does.
		n, _ := new(big.Int).SetString(s[2:], radixes[s[1]])
		f, _ := new(big.Float).SetInt(n).Float64()
		return f
	}
	return math.NaN()
}

// GreaterThan is JS `a > b` for primitive operands: two strings compare by UTF-16 code units,
// anything else compares as ToNumber of both, and NaN on either side is false. Pass math.NaN()
// for an absent operand (undefined compares as NaN). Object and array operands are not
// modelled (JS would compare their string forms) and compare false.
func GreaterThan(a, b any) bool {
	as, aIsString := a.(string)
	bs, bIsString := b.(string)
	if aIsString && bIsString {
		return jsstr.Compare(as, bs) > 0
	}
	if !isPrimitive(a) || !isPrimitive(b) {
		return false
	}
	return ToNumber(a) > ToNumber(b) // false when either is NaN
}

// GreaterOrEqual is JS `a >= b` under the same rules as GreaterThan: it is not !(a < b), as
// NaN on either side is false here too.
func GreaterOrEqual(a, b any) bool {
	as, aIsString := a.(string)
	bs, bIsString := b.(string)
	if aIsString && bIsString {
		return jsstr.Compare(as, bs) >= 0
	}
	if !isPrimitive(a) || !isPrimitive(b) {
		return false
	}
	return ToNumber(a) >= ToNumber(b)
}

func isPrimitive(v any) bool {
	switch v.(type) {
	case nil, bool, float64, string:
		return true
	}
	return false
}
