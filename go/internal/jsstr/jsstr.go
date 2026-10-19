// Package jsstr reproduces the JavaScript string operations orch output depends on. JS strings
// are sequences of UTF-16 code units, so lengths, indices, padding and ordering count code
// units, not bytes or runes.
//
// A Go string cannot hold a lone surrogate. Where a JS slice splits a surrogate pair, the
// half kept becomes U+FFFD; Node prints a lone surrogate as the same three bytes, so text output
// is identical and only JSON escapes written by the JS ("\ud83d") differ.
package jsstr

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Len is the JS `s.length`: the number of UTF-16 code units.
func Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// Slice is String.prototype.slice(start, end): a negative index counts from the end, both are
// clamped to [0, length], and an end at or before start yields "". Pass Len(s) (or any larger
// number) as end for the one-argument form.
func Slice(s string, start, end int) string {
	units := utf16.Encode([]rune(s))
	start, end = relativeIndex(start, len(units)), relativeIndex(end, len(units))
	if start >= end {
		return ""
	}
	return string(utf16.Decode(units[start:end]))
}

func relativeIndex(i, length int) int {
	if i < 0 {
		return max(length+i, 0)
	}
	return min(i, length)
}

// Compare orders strings by UTF-16 code units, as JS `<` and the default Array.prototype.sort
// do. It differs from Go's byte order for code points above U+FFFF against U+E000..U+FFFF.
func Compare(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

// PadEnd is String.prototype.padEnd(length) with the default space filler.
func PadEnd(s string, length int) string {
	if n := length - Len(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// DecodeUTF8 matches Node's Buffer.toString("utf8") (and readFileSync(f, "utf8")): one U+FFFD
// per malformed sequence, where a valid but incomplete prefix counts as one sequence; a BOM is
// kept.
func DecodeUTF8(data []byte) string {
	var text strings.Builder
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			// Consume the incomplete prefix, but leave the byte that makes it invalid
			// for the next iteration (it may start a valid character).
			for size < len(data) && !utf8.FullRune(data[:size+1]) {
				size++
			}
		}
		text.WriteRune(r)
		data = data[size:]
	}
	return text.String()
}
