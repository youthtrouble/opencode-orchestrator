package jsjson

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goldenDir = "../../testdata/json"

// TestGoldens round-trips every input through Unmarshal and Marshal/MarshalIndent and compares
// the bytes with what Node's JSON.stringify produced (testdata/json/gen.mjs).
func TestGoldens(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no golden inputs found in %s (err %v)", goldenDir, err)
	}
	for _, input := range inputs {
		name := strings.TrimSuffix(filepath.Base(input), ".json")
		value, err := Unmarshal(readFile(t, input))
		if err != nil {
			t.Fatalf("%s: Unmarshal: %v", name, err)
		}
		for _, form := range []struct{ suffix, gap string }{{"compact", ""}, {"indent2", "  "}} {
			t.Run(name+"/"+form.suffix, func(t *testing.T) {
				got, err := MarshalIndent(value, form.gap)
				if err != nil {
					t.Fatalf("MarshalIndent: %v", err)
				}
				want := readFile(t, filepath.Join(goldenDir, name+"."+form.suffix+".golden"))
				if string(got) != string(want) {
					t.Errorf("output differs from JSON.stringify\n got: %s\nwant: %s", got, want)
				}
			})
		}
	}
}

func TestObjectReassignmentKeepsPositionAndIndexKeysComeFirst(t *testing.T) {
	o := NewObject()
	o.Set("status", "running")
	o.Set("10", true)
	o.Set("pid", 1.0)
	o.Set("2", false)
	o.Set("status", "finished")
	got := strings.Join(o.Keys(), ",")
	if want := "2,10,status,pid"; got != want {
		t.Errorf("Keys() = %s, want %s", got, want)
	}
	value, _ := o.Get("status")
	if value != "finished" {
		t.Errorf("status = %v, want finished", value)
	}
}

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{math.NaN(), "null"},
		{math.Inf(1), "null"},
		{math.Inf(-1), "null"},
		{math.Copysign(0, -1), "0"},
		{1e21, "1e+21"},
		{1e-7, "1e-7"},
		{-1.5e-9, "-1.5e-9"},
		{1e-6, "0.000001"},
		{123.456, "123.456"},
		{1e300, "1e+300"},
	}
	for _, c := range cases {
		if got := formatNumber(c.in); got != c.want {
			t.Errorf("formatNumber(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestStringsAreNotHTMLOrSeparatorEscaped(t *testing.T) {
	got, err := Marshal("<a href=\"x\">&</a>  \x01é")
	if err != nil {
		t.Fatal(err)
	}
	if want := "\"<a href=\\\"x\\\">&</a>  \\u0001é\""; string(got) != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
}

func TestMarshalRejectsTypesOutsideTheValueSet(t *testing.T) {
	for _, value := range []any{42, map[string]any{}, (*Object)(nil), []string{"x"}} {
		_, err := Marshal([]any{value})
		var unsupported *UnsupportedTypeError
		if !errors.As(err, &unsupported) {
			t.Errorf("Marshal(%T) error = %v, want *UnsupportedTypeError", value, err)
		}
	}
}

func TestMarshalRejectsInvalidUTF8WithoutReturningJSON(t *testing.T) {
	for _, invalid := range []string{"\xff", "\xff\xff", "\xc2", "\xe2\x82", "\xf0\x9f\x92", "\xe2\x82A", "\xed\xa0\x80"} {
		key := NewObject()
		key.Set(invalid, "value")
		for _, value := range []any{invalid, []any{"valid", invalid}, key} {
			got, err := Marshal(value)
			if !errors.Is(err, ErrInvalidUTF8) || got != nil {
				t.Errorf("Marshal(%T, invalid bytes %x) = %x, %v; want nil, ErrInvalidUTF8", value, invalid, got, err)
			}
		}
		if got, err := MarshalIndent([]any{"value"}, invalid); !errors.Is(err, ErrInvalidUTF8) || got != nil {
			t.Errorf("invalid gap %x = %x, %v; want nil, ErrInvalidUTF8", invalid, got, err)
		}
	}
	if got, err := Marshal("\ufffdé😀"); err != nil || string(got) != "\"\ufffdé😀\"" {
		t.Errorf("valid replacement and multibyte characters = %x, %v", got, err)
	}
}

func TestUnmarshalDuplicateKeysLastValueWins(t *testing.T) {
	value, err := Unmarshal([]byte(`{"a":1,"b":2,"a":3}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Marshal(value)
	if want := `{"a":3,"b":2}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestUnmarshalRejectsWhatJSONParseRejects covers the grammar edges JSON.parse is strict about.
func TestUnmarshalRejectsWhatJSONParseRejects(t *testing.T) {
	for _, input := range []string{``, `   `, `{"a":1,}`, `[1,]`, `[01]`, `{a:1}`, `'x'`, "\"tab\there\"", `"\x"`, `NaN`, `[1 2]`, `{"a" 1}`, `[1`, `{"a":`, `{"a"`} {
		var syntax *json.SyntaxError
		_, err := Unmarshal([]byte(input))
		if err == nil {
			t.Errorf("Unmarshal(%q) succeeded, want an error", input)
		} else if !errors.As(err, &syntax) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("Unmarshal(%q) error = %v, want a syntax error", input, err)
		}
	}
}

func TestUnmarshalRejectsTrailingValues(t *testing.T) {
	for _, input := range []string{`1 2`, `{} []`, `"a""b"`} {
		if _, err := Unmarshal([]byte(input)); !errors.Is(err, ErrTrailingData) {
			t.Errorf("Unmarshal(%q) error = %v, want ErrTrailingData", input, err)
		}
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
