package jsstr

import (
	"strings"
	"unicode"
)

// ToLower is String.prototype.toLowerCase: the full, locale-independent Unicode lowercase
// mapping. It is unicode.ToLower per code point except for the only two mappings that are
// not one code point to one: U+0130 (İ) becomes "i̇", and a capital sigma that ends a word
// becomes final sigma (the Final_Sigma condition of Unicode §3.13), so "İΣΑΣ" lowers to
// "i̇σας" where strings.ToLower gives "i̇σασ".
//
// Code points that Go's Unicode tables do not know yet but Node's ICU does map to themselves
// (parity exception PE-7; the test against Node lists them).
func ToLower(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var lower strings.Builder
	lower.Grow(len(s))
	for i, r := range runes {
		switch {
		case r == 'İ':
			lower.WriteString("i̇")
		case r == 'Σ' && isFinalSigma(runes, i):
			lower.WriteRune('ς')
		default:
			lower.WriteRune(unicode.ToLower(r))
		}
	}
	return lower.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// isFinalSigma reports whether the sigma at runes[i] is preceded by a cased letter and not
// followed by one, skipping case-ignorable code points in both directions. As in ICU, a code
// point that is both cased and case-ignorable (U+0345, say) is skipped.
func isFinalSigma(runes []rune, i int) bool {
	return casedLetterAt(runes, i, -1) && !casedLetterAt(runes, i, +1)
}

// casedLetterAt reports whether the first code point from runes[i] in direction step that is
// not case-ignorable is cased.
func casedLetterAt(runes []rune, i, step int) bool {
	for j := i + step; j >= 0 && j < len(runes); j += step {
		if !isCaseIgnorable(runes[j]) {
			return isCased(runes[j])
		}
	}
	return false
}

// isCased is the Unicode Cased property: Lowercase, Uppercase or Lt.
func isCased(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// isCaseIgnorable is the Unicode Case_Ignorable property: the categories Mn, Me, Cf, Lm and Sk
// plus the Word_Break values MidLetter, MidNumLet and Single_Quote, for which Go has no table.
func isCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk, wordBreakMids)
}

// wordBreakMids are the code points whose Word_Break is MidLetter, MidNumLet or Single_Quote
// (WordBreakProperty.txt) and that lie outside the categories isCaseIgnorable already covers.
var wordBreakMids = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0027, Hi: 0x0027, Stride: 1}, // APOSTROPHE (Single_Quote)
		{Lo: 0x002e, Hi: 0x002e, Stride: 1}, // FULL STOP
		{Lo: 0x003a, Hi: 0x003a, Stride: 1}, // COLON
		{Lo: 0x00b7, Hi: 0x00b7, Stride: 1}, // MIDDLE DOT
		{Lo: 0x0387, Hi: 0x0387, Stride: 1}, // GREEK ANO TELEIA
		{Lo: 0x055f, Hi: 0x055f, Stride: 1}, // ARMENIAN ABBREVIATION MARK
		{Lo: 0x05f4, Hi: 0x05f4, Stride: 1}, // HEBREW PUNCTUATION GERSHAYIM
		{Lo: 0x2018, Hi: 0x2019, Stride: 1}, // SINGLE QUOTATION MARKS
		{Lo: 0x2024, Hi: 0x2024, Stride: 1}, // ONE DOT LEADER
		{Lo: 0x2027, Hi: 0x2027, Stride: 1}, // HYPHENATION POINT
		{Lo: 0xfe13, Hi: 0xfe13, Stride: 1}, // PRESENTATION FORM FOR VERTICAL COLON
		{Lo: 0xfe52, Hi: 0xfe52, Stride: 1}, // SMALL FULL STOP
		{Lo: 0xfe55, Hi: 0xfe55, Stride: 1}, // SMALL COLON
		{Lo: 0xff07, Hi: 0xff07, Stride: 1}, // FULLWIDTH APOSTROPHE
		{Lo: 0xff0e, Hi: 0xff0e, Stride: 1}, // FULLWIDTH FULL STOP
		{Lo: 0xff1a, Hi: 0xff1a, Stride: 1}, // FULLWIDTH COLON
	},
	LatinOffset: 4,
}
