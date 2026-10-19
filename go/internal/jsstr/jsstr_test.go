package jsstr

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// "a😀b" is a, high surrogate, low surrogate, b: indices 1..3 split the pair.
var sliceCases = []struct {
	s          string
	start, end int
	want       string
}{
	{"hello", 0, 3, "hel"},
	{"hello", -3, 5, "llo"},
	{"hello", 1, -1, "ell"},
	{"hello", -99, 99, "hello"},
	{"hello", 3, 1, ""},
	{"hello", 5, 9, ""},
	{"a😀b", 0, 4, "a😀b"},
	{"a😀b", 1, 3, "😀"},
	{"a😀b", 0, 2, "a\ufffd"},
	{"a😀b", 2, 4, "\ufffdb"},
	{"a😀b", -2, 4, "\ufffdb"},
	{"a😀b", -3, -1, "😀"},
	{"é中", 0, 1, "é"},
	{"", -1, 1, ""},
}

func TestSliceCountsUTF16CodeUnits(t *testing.T) {
	for _, c := range sliceCases {
		if got := Slice(c.s, c.start, c.end); got != c.want {
			t.Errorf("Slice(%q, %d, %d) = %q, want %q", c.s, c.start, c.end, got, c.want)
		}
	}
}

// TestSliceMatchesNode checks the same table against String.prototype.slice, with Node's lone
// surrogates mapped to U+FFFD as Go represents them.
func TestSliceMatchesNode(t *testing.T) {
	type input struct {
		S          string `json:"s"`
		Start, End int
	}
	var inputs []input
	for _, c := range sliceCases {
		inputs = append(inputs, input{c.s, c.start, c.end})
	}
	got := nodeJSON(t, `const r = JSON.parse(process.argv[1]).map(c => c.s.slice(c.Start, c.End).toWellFormed()); console.log(JSON.stringify(r))`, inputs)
	for i, c := range sliceCases {
		if got[i] != c.want {
			t.Errorf("Node %q.slice(%d, %d) = %q, table says %q", c.s, c.start, c.end, got[i], c.want)
		}
	}
}

func TestCompareIsUTF16Order(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"a", "b", -1},
		{"b", "a", 1},
		{"a", "a", 0},
		{"a", "ab", -1},
		{"", "a", -1},
		// U+1F600 is D83D DE00 in UTF-16, which sorts before U+E000; in UTF-8 bytes it sorts after.
		{"😀", "\ue000", -1},
		{"\ue000", "😀", 1},
		{"é", "e\u0301", 1}, // NFC é (00E9) after NFD e (0065 0301)
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if strings.Compare("😀", "\ue000") != 1 {
		t.Fatal("byte order no longer differs; the U+1F600/U+E000 case proves nothing")
	}
}

func TestLenAndPadEndCountUTF16CodeUnits(t *testing.T) {
	if got := Len("a😀é"); got != 4 {
		t.Errorf("Len = %d, want 4", got)
	}
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"note", 8, "note    "},
		{"question", 8, "question"},
		{"handoff-long", 8, "handoff-long"},
		{"😀", 3, "😀 "},
		{"é", 2, "é "},
	}
	for _, c := range cases {
		if got := PadEnd(c.s, c.n); got != c.want {
			t.Errorf("PadEnd(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// nodeJSON runs script with the JSON of arg as process.argv[1] and decodes its stdout as a
// list of strings; it skips when Node is unavailable (the fixed tables still run).
func nodeJSON(t *testing.T, script string, arg any) []string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable; fixed expectations still tested")
	}
	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", "-e", script, string(data)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return got
}

func TestUndecodedTailHoldsBackAnIncompleteCharacter(t *testing.T) {
	for _, tc := range []struct {
		data string
		want int
	}{
		{"", 0},
		{"abc", 0},
		{"a\xc3", 1},            // é cut after its lead byte
		{"a\xc3\xa9", 0},        // é complete
		{"\xe2\x82", 2},         // € missing its last byte
		{"\xf0\x9f\x98", 3},     // 😀 missing its last byte
		{"\xf0\x9f\x98\x80", 0}, // 😀 complete
		{"a\x80", 0},            // a stray continuation byte is not held back
		{"\xf5\x80", 2},         // 0xF5 counts as a four-byte lead, as in Node
		{"\x80\x80\x80\x80", 0}, // no lead byte within the last four
		{"\xff", 0},             // never a lead byte
	} {
		if got := UndecodedTail([]byte(tc.data)); got != tc.want {
			t.Errorf("UndecodedTail(%q) = %d, want %d", tc.data, got, tc.want)
		}
	}
}
