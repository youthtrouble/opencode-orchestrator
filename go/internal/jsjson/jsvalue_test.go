package jsjson

import (
	"math"
	"testing"
	"time"
)

func TestLookupFollowsOptionalChaining(t *testing.T) {
	doc, err := Unmarshal([]byte(`{"a": {"b": null, "c": [1]}, "s": "text"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		keys    []string
		want    any
		present bool
	}{
		{[]string{"a", "b"}, nil, true},
		{[]string{"a", "missing"}, nil, false},
		{[]string{"a", "b", "deeper"}, nil, false},
		{[]string{"s", "length"}, nil, false},
	} {
		got, present := Lookup(doc, c.keys...)
		if got != c.want || present != c.present {
			t.Errorf("Lookup(%v) = %v, %v; want %v, %v", c.keys, got, present, c.want, c.present)
		}
	}
	var nilObject *Object
	if _, present := Lookup(nilObject, "a"); present {
		t.Error("Lookup on a nil *Object should be absent")
	}
}

func TestTruthyMatchesJS(t *testing.T) {
	for _, falsy := range []any{nil, false, 0.0, math.NaN(), ""} {
		if Truthy(falsy) {
			t.Errorf("Truthy(%#v) = true, want false", falsy)
		}
	}
	for _, truthy := range []any{true, -1.0, "0", []any{}, NewObject()} {
		if !Truthy(truthy) {
			t.Errorf("Truthy(%#v) = false, want true", truthy)
		}
	}
}

// Expected strings are String(v) in Node.
func TestTemplateStringMatchesJSString(t *testing.T) {
	for _, c := range []struct {
		value   any
		present bool
		want    string
	}{
		{nil, false, "undefined"},
		{nil, true, "null"},
		{true, true, "true"},
		{33.5, true, "33.5"},
		{1e21, true, "1e+21"},
		{math.NaN(), true, "NaN"},
		{math.Inf(-1), true, "-Infinity"},
		{"text", true, "text"},
		{[]any{1.0, nil, "a"}, true, "1,,a"},
		{NewObject(), true, "[object Object]"},
	} {
		if got := TemplateStringOf(c.value, c.present); got != c.want {
			t.Errorf("TemplateStringOf(%#v, %v) = %q, want %q", c.value, c.present, got, c.want)
		}
	}
	if got := OrText("", "fallback"); got != "fallback" {
		t.Errorf(`OrText("") = %q, want fallback`, got)
	}
}

func TestNumberOrReadsOnlyNumbers(t *testing.T) {
	doc, err := Unmarshal([]byte(`{"n": 0, "s": "5", "z": null}`))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"n": 0, "s": 7, "z": 7, "missing": 7} {
		if got := NumberOr(doc, key, 7); got != want {
			t.Errorf("NumberOr(%q) = %v, want %v", key, got, want)
		}
	}
	if got := NumberOr(nil, "n", 7); got != 7 {
		t.Errorf("NumberOr on a missing object = %v, want 7", got)
	}
}

// Expected values are Date.parse(input) in Node.
func TestParseDateMatchesDateParse(t *testing.T) {
	localNoon := time.Date(2026, 10, 18, 10, 0, 0, 0, time.Local).UnixMilli()
	for _, c := range []struct {
		input string
		want  int64
		ok    bool
	}{
		{"2026-10-18T18:39:59.707947+00:00", 1792348799707, true},
		{"2026-10-18T10:00:00.5Z", 1792317600500, true},
		{"2026-10-18", 1792281600000, true},
		{"2026-10-18T10:00:00", localNoon, true},
		{"2026-10-18T10:00:00.5", localNoon + 500, true},
		{"2026-10-18T10:00", localNoon, true},
		{"2026-10-18T10:00Z", 1792317600000, true},
		{"2026-10-18T10:00+01:00", 1792314000000, true},
		{"2026-10", 1790812800000, true},
		{"2026", 1767225600000, true},
		{"2026-13-01", 0, false},
		{"", 0, false},
		{"not a date", 0, false},
	} {
		got, ok := ParseDate(c.input)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseDate(%q) = %d, %v; want %d, %v", c.input, got, ok, c.want, c.ok)
		}
	}
}

// Known gaps (documented on dateLayouts): V8 accepts these, ParseDate rejects them. The
// Date.parse value is kept so the divergence is explicit.
func TestParseDateDocumentedDivergences(t *testing.T) {
	localNoon := time.Date(2026, 10, 18, 10, 0, 0, 0, time.Local).UnixMilli()
	for _, c := range []struct {
		input     string
		dateParse int64
	}{
		{"2026-10-18T10:00:00z", 1792317600000},
		{"2026-10-18T10:00:00+0100", 1792314000000},
		{"2026-10-18 10:00:00", localNoon},
		{"2026-10-18T24:00:00Z", 1792368000000},
		{"2026-02-30", 1772409600000},
		{"+002026-10-18", 1792281600000},
	} {
		if got, ok := ParseDate(c.input); ok {
			t.Errorf("ParseDate(%q) = %d, true; this gap (Date.parse %d) is documented as rejected", c.input, got, c.dateParse)
		}
	}
}

func TestRoundIsMathRound(t *testing.T) {
	for input, want := range map[float64]float64{2.5: 3, -2.5: -2, -2.6: -3, 15.995: 16} {
		if got := Round(input); got != want {
			t.Errorf("Round(%v) = %v, want %v", input, got, want)
		}
	}
}
