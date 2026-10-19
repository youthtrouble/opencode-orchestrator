package jsstr

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func TestToLowerSpecialCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"HELLO World 42", "hello world 42"},
		{"İΣΑΣ", "i̇σας"}, // verified with Node: "İΣΑΣ".toLowerCase()
		{"Σ", "σ"},        // nothing cased before it
		{"ΑΣ", "ας"},
		{"ΑΣΑ", "ασα"},
		{"ΑΣ.", "ας."},
		{"ΑΣ.Α", "ασ.α"}, // FULL STOP is MidNumLet, so case-ignorable: a cased letter follows
		{"Α'Σ", "α'ς"},   // APOSTROPHE is skipped looking back
		{"ΆΣ́", "άς́"},
		{"Α Σ", "α σ"},   // a space is not case-ignorable
		{"ΑΣͅΑ", "ασͅα"}, // U+0345 is cased and case-ignorable: skipped
		{"ПРИВЕТ", "привет"},
		{"𐐀𐐁", "𐐨𐐩"},   // Deseret
		{"ᲐᲑᲒ", "აბგ"}, // Georgian Mtavruli
		{"ǅ", "ǆ"},
	}
	for _, c := range cases {
		if got := ToLower(c.in); got != c.want {
			t.Errorf("ToLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// lowerReference is testdata/jsstr/lower.json, written by gen-lower.mjs from Node.
type lowerReference struct {
	Node, ICU, Unicode string
	Lower              map[string][]rune
	Cased, Ignorable   [][2]rune
}

// propertiesChangedAfterGo are assigned code points whose case properties differ between Go's
// Unicode 15.0 tables and Node's newer data (PE-7, like the unassigned ones).
var propertiesChangedAfterGo = map[rune]string{
	0x0295:  "Lowercase in Go's tables, not cased in Node's",
	0x1171E: "Mn (case-ignorable) in Go's tables, not case-ignorable in Node's",
}

// TestToLowerMatchesNodeForEveryCodePoint compares ToLower with Node's toLowerCase for every
// code point, and the Final_Sigma context sets as both observe them. Differences are allowed
// only for code points Go's Unicode tables do not assign and for propertiesChangedAfterGo
// (PE-7); the rest must match.
func TestToLowerMatchesNodeForEveryCodePoint(t *testing.T) {
	data, err := os.ReadFile("../../testdata/jsstr/lower.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref lowerReference
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatal(err)
	}
	cased, ignorable := rangeSet(ref.Cased), rangeSet(ref.Ignorable)
	var unexplained, newerThanGo []string
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		c := string(r)
		want, mapped := ref.Lower[strconv.Itoa(int(r))]
		if !mapped {
			want = []rune{r}
		}
		var diffs []string
		if got := ToLower(c); got != string(want) {
			diffs = append(diffs, fmt.Sprintf("lower %q, node %q", got, string(want)))
		}
		gotCased := strings.HasSuffix(ToLower(c+"Σ"), "ς")
		gotIgnorable := !gotCased && strings.HasSuffix(ToLower("A"+c+"Σ"), "ς")
		if gotCased != cased[r] || gotIgnorable != ignorable[r] {
			diffs = append(diffs, fmt.Sprintf("cased %v ignorable %v, node %v %v", gotCased, gotIgnorable, cased[r], ignorable[r]))
		}
		if len(diffs) == 0 {
			continue
		}
		entry := fmt.Sprintf("U+%04X: %s", r, strings.Join(diffs, "; "))
		if reason, known := propertiesChangedAfterGo[r]; known {
			entry += " (" + reason + ")"
		}
		if isAssignedInGo(r) && propertiesChangedAfterGo[r] == "" {
			unexplained = append(unexplained, entry)
		} else {
			newerThanGo = append(newerThanGo, entry)
		}
	}
	t.Logf("Node %s (ICU %s, Unicode %s) vs Go Unicode %s: %d code points differ (PE-7)", ref.Node, ref.ICU, ref.Unicode, unicode.Version, len(newerThanGo))
	for _, entry := range newerThanGo {
		t.Log("PE-7 " + entry)
	}
	for _, entry := range unexplained {
		t.Error(entry)
	}
}

func rangeSet(ranges [][2]rune) map[rune]bool {
	set := map[rune]bool{}
	for _, r := range ranges {
		for c := r[0]; c <= r[1]; c++ {
			set[c] = true
		}
	}
	return set
}

// isAssignedInGo reports whether Go's Unicode tables assign r (its category is not Cn).
func isAssignedInGo(r rune) bool {
	return !unicode.Is(unicode.Categories["Cn"], r)
}
