package jsjson

import (
	"math"
	"os/exec"
	"strings"
	"testing"
)

// numberCases are JS Number(s) results; "NaN" marks NaN.
var numberCases = []struct{ in, want string }{
	{"", "0"}, {"   ", "0"}, {"0", "0"}, {"-0", "-0"}, {"7", "7"}, {" 7 ", "7"}, {"\t\n7\u00a0\ufeff", "7"},
	{"\u20287\u3000", "7"}, {"2.5", "2.5"}, {"-5", "-5"}, {"+5", "5"}, {"1.", "1"}, {".5", "0.5"}, {".", "NaN"},
	{"1e3", "1000"}, {"1E-2", "0.01"}, {"1e", "NaN"}, {"1e400", "Infinity"}, {"-1e400", "-Infinity"},
	{"Infinity", "Infinity"}, {"+Infinity", "Infinity"}, {"-Infinity", "-Infinity"}, {"infinity", "NaN"}, {"Inf", "NaN"},
	{"0x10", "16"}, {" 0x10 ", "16"}, {"0XfF", "255"}, {"0o17", "15"}, {"0b101", "5"}, {"-0x10", "NaN"}, {"0x", "NaN"},
	{"0x1fffffffffffff1", "144115188075855860"}, {"0b2", "NaN"}, {"0o8", "NaN"},
	{"abc", "NaN"}, {"1_000", "NaN"}, {"1,5", "NaN"}, {"NaN", "NaN"}, {"12abc", "NaN"}, {"0.0000001", "1e-7"},
	{"\u00857", "NaN"}, // U+0085 is not JS white space
}

func TestToNumberOfStrings(t *testing.T) {
	for _, c := range numberCases {
		if got := numberText(ToNumber(c.in)); got != c.want {
			t.Errorf("ToNumber(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestToNumberMatchesNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable; fixed expectations still tested")
	}
	inputs := make([]any, len(numberCases))
	for i, c := range numberCases {
		inputs[i] = c.in
	}
	arg, err := Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	script := `console.log(JSON.parse(process.argv[1]).map(s => Object.is(Number(s), -0) ? "-0" : String(Number(s))).join("\n"))`
	out, err := exec.Command("node", "-e", script, string(arg)).Output()
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if got != numberCases[i].want {
			t.Errorf("Node Number(%q) = %s, table says %s", numberCases[i].in, got, numberCases[i].want)
		}
	}
}

func TestToNumberOfOtherPrimitives(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{{nil, "0"}, {true, "1"}, {false, "0"}, {2.5, "2.5"}, {[]any{}, "NaN"}, {NewObject(), "NaN"}}
	for _, c := range cases {
		if got := numberText(ToNumber(c.in)); got != c.want {
			t.Errorf("ToNumber(%#v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func numberText(f float64) string {
	if f == 0 && math.Signbit(f) {
		return "-0"
	}
	return TemplateString(f)
}

func TestSpreadCopiesOwnEnumerableProperties(t *testing.T) {
	obj := NewObject()
	obj.Set("b", 1.0)
	obj.Set("1", "one")
	obj.Set("a", nil)
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"object keeps JS order", obj, `{"1":"one","b":1,"a":null}`},
		{"array gives index keys", []any{"x", nil, 3.0}, `{"0":"x","1":null,"2":3}`},
		{"string gives one key per UTF-16 unit", "aé😀", `{"0":"a","1":"é","2":"\ufffd","3":"\ufffd"}`},
		{"empty string", "", `{}`},
		{"number", 5.0, `{}`},
		{"boolean", true, `{}`},
		{"null and absent", nil, `{}`},
	}
	for _, c := range cases {
		got, err := Marshal(Spread(c.in))
		if err != nil {
			t.Fatal(err)
		}
		if want := mustCanonical(t, c.want); string(got) != want {
			t.Errorf("%s: Spread = %s, want %s", c.name, got, want)
		}
	}
	copied := Spread(obj)
	copied.Set("b", 2.0)
	if v, _ := obj.Get("b"); v != 1.0 {
		t.Error("Spread shares storage with its source")
	}
}

// mustCanonical re-encodes a JSON literal so escapes in the expectations match Marshal output.
func mustCanonical(t *testing.T, literal string) string {
	t.Helper()
	v, err := Unmarshal([]byte(literal))
	if err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGreaterThanFollowsJSRelationalComparison(t *testing.T) {
	nan := math.NaN()
	cases := []struct {
		a, b any
		want bool
	}{
		{"b", "a", true},
		{"a", "b", false},
		{"2026-10-18T12:00:00.001Z", "2026-10-18T12:00:00.000Z", true},
		{"\ue000", "😀", true}, // UTF-16 order, not byte order
		{"10", "9", false},    // two strings never compare as numbers
		{5.0, "1", true},
		{5.0, "x", false}, // NaN
		{nan, "1", false}, // absent
		{nil, "-1", true}, // null is 0
		{true, "0.5", true},
		{"2", 1.0, true},
		{NewObject(), "a", false}, // not modelled
	}
	for _, c := range cases {
		if got := GreaterThan(c.a, c.b); got != c.want {
			t.Errorf("GreaterThan(%#v, %#v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestJoinIsArrayJoin(t *testing.T) {
	if got := Join([]any{"a", nil, 1.0, []any{"b", "c"}, NewObject()}, ", "); got != "a, , 1, b,c, [object Object]" {
		t.Errorf("Join = %q", got)
	}
	if got := Join(nil, ","); got != "" {
		t.Errorf("Join(nil) = %q", got)
	}
}
