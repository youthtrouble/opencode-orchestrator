package jsjson

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// UnsupportedTypeError reports a value outside the closed set of jsjson types.
type UnsupportedTypeError struct {
	Value any
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("jsjson: unsupported type %T", e.Value)
}

// ErrInvalidUTF8 rejects Go strings that cannot represent a JavaScript string. Decode raw
// text at its input boundary before encoding; never emit malformed UTF-8 JSON.
var ErrInvalidUTF8 = errors.New("jsjson: invalid UTF-8 string")

// Marshal returns the compact encoding of v, identical to JSON.stringify(v).
// Invalid UTF-8 strings (including object keys) return ErrInvalidUTF8.
func Marshal(v any) ([]byte, error) {
	return MarshalIndent(v, "")
}

// MarshalIndent returns the encoding of v identical to JSON.stringify(v, null, gap): every
// member and element on its own line, indented by gap per level, `": "` between key and
// value, and empty objects and arrays written as {} and []. An empty gap means compact output,
// as in JS. Neither form ends with a newline; callers add one where console.log or the JS
// file writer did.
func MarshalIndent(v any, gap string) ([]byte, error) {
	if !utf8.ValidString(gap) {
		return nil, ErrInvalidUTF8
	}
	e := encoder{gap: gap}
	if err := e.value(v, ""); err != nil {
		return nil, err
	}
	return e.buf.Bytes(), nil
}

type encoder struct {
	buf bytes.Buffer
	gap string
}

func (e *encoder) value(v any, indent string) error {
	switch v := v.(type) {
	case nil:
		e.buf.WriteString("null")
	case bool:
		e.buf.WriteString(strconv.FormatBool(v))
	case float64:
		e.buf.WriteString(formatNumber(v))
	case string:
		if !utf8.ValidString(v) {
			return ErrInvalidUTF8
		}
		writeString(&e.buf, v)
	case []any:
		return e.array(v, indent)
	case *Object:
		if v == nil {
			return &UnsupportedTypeError{Value: v}
		}
		return e.object(v, indent)
	default:
		return &UnsupportedTypeError{Value: v}
	}
	return nil
}

func (e *encoder) array(items []any, indent string) error {
	if len(items) == 0 {
		e.buf.WriteString("[]")
		return nil
	}
	inner := indent + e.gap
	e.buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			e.buf.WriteByte(',')
		}
		e.newline(inner)
		if err := e.value(item, inner); err != nil {
			return err
		}
	}
	e.newline(indent)
	e.buf.WriteByte(']')
	return nil
}

func (e *encoder) object(o *Object, indent string) error {
	if o.Len() == 0 {
		e.buf.WriteString("{}")
		return nil
	}
	inner := indent + e.gap
	e.buf.WriteByte('{')
	for i, key := range o.Keys() {
		if !utf8.ValidString(key) {
			return ErrInvalidUTF8
		}
		if i > 0 {
			e.buf.WriteByte(',')
		}
		e.newline(inner)
		writeString(&e.buf, key)
		e.buf.WriteByte(':')
		if e.gap != "" {
			e.buf.WriteByte(' ')
		}
		value, _ := o.Get(key)
		if err := e.value(value, inner); err != nil {
			return err
		}
	}
	e.newline(indent)
	e.buf.WriteByte('}')
	return nil
}

// newline starts a new indented line; compact output has no line breaks at all.
func (e *encoder) newline(indent string) {
	if e.gap == "" {
		return
	}
	e.buf.WriteByte('\n')
	e.buf.WriteString(indent)
}

// formatNumber implements ECMAScript Number::toString as JSON.stringify applies it: NaN and
// the infinities become null, -0 becomes 0, magnitudes in [1e-6, 1e21) are written in plain
// decimal and everything else in exponent form without zero padding (1e+21, 1e-7). Go's
// shortest round-trip digits ('f'/'e' with precision -1) equal the digits JS chooses.
func formatNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	if abs := math.Abs(f); abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	// Go pads negative exponents to two digits ("1e-07"); JS does not ("1e-7"). Positive
	// exponents here are always >= 21, so they never carry padding.
	if n := len(s); n >= 4 && s[n-4] == 'e' && s[n-3] == '-' && s[n-2] == '0' {
		s = s[:n-2] + s[n-1:]
	}
	return s
}

// writeString quotes s the way JSON.stringify does: only `"`, `\` and control characters
// below U+0020 are escaped (\b \f \n \r \t short forms, the rest as lowercase \u00xx).
// Unlike encoding/json, `/`, `<`, `>`, `&`, U+2028, U+2029 and all non-ASCII text are
// written raw. Working byte by byte is safe because every byte of a multi-byte UTF-8
// sequence is >= 0x80.
func writeString(buf *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hex[c>>4])
				buf.WriteByte(hex[c&0xf])
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}
