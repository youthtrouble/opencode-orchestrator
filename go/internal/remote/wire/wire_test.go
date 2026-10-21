package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The protocol examples from this checkout, so a schema change that the structs do not follow
// fails here.
const examplesDir = "../../../../remote/protocol/v1/examples"

// newValue maps each schema def Go decodes to a fresh value of its struct.
var newValue = map[string]func() any{
	"PushRequest":     func() any { return new(PushRequest) },
	"PushResponse":    func() any { return new(PushResponse) },
	"PullResponse":    func() any { return new(PullResponse) },
	"AckRequest":      func() any { return new(AckRequest) },
	"AckResponse":     func() any { return new(AckResponse) },
	"ProjectSnapshot": func() any { return new(ProjectSnapshot) },
	"Question":        func() any { return new(Question) },
	"Event":           func() any { return new(Event) },
	"CiRun":           func() any { return new(CiRun) },
	"Progress":        func() any { return new(Progress) },
	"Run":             func() any { return new(Run) },
	"Posture":         func() any { return new(Posture) },
}

type example struct {
	Def   string          `json:"def"`
	Value json.RawMessage `json:"value"`
}

func readExamples(t *testing.T, kind string) map[string]example {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(examplesDir, kind, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no %s examples under %s: %v", kind, examplesDir, err)
	}
	examples := map[string]example{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var ex example
		if err := json.Unmarshal(data, &ex); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		examples[filepath.Base(file)] = ex
	}
	return examples
}

func TestEveryValidExampleRoundTrips(t *testing.T) {
	covered := map[string]bool{}
	for name, ex := range readExamples(t, "valid") {
		constructor, ok := newValue[ex.Def]
		if !ok {
			continue
		}
		covered[ex.Def] = true
		value := constructor()
		if err := Decode(bytes.NewReader(ex.Value), value); err != nil {
			t.Errorf("%s: decode %s: %v", name, ex.Def, err)
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, ex.Value, encoded) {
			t.Errorf("%s: re-encoded %s differs\n got %s\nwant %s", name, ex.Def, encoded, ex.Value)
		}
	}
	for def := range newValue {
		if !covered[def] {
			t.Errorf("no valid example exercises %s", def)
		}
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(left, right)
}

func TestDecodeRejectsAnUndeclaredProperty(t *testing.T) {
	ex := readExamples(t, "invalid")["push-request-extra-property.json"]
	err := Decode(bytes.NewReader(ex.Value), new(PushRequest))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("Decode = %v, want an unknown field error", err)
	}
}

func TestDecodeRejectsACaseVariantOfADeclaredProperty(t *testing.T) {
	cases := map[string]struct {
		payload string
		value   any
		path    string
	}{
		"top level": {`{"v":1,"settled":0,"Settled":99}`, new(AckResponse), "$.Settled"},
		"nested object": {`{"v":1,"server_time":"2026-01-01T00:00:00Z","poll_after_seconds":30,"answers":[],` +
			`"actions":[{"id":"a","project_key":"k","kind":"pause","created_at":"2026-01-01T00:00:00Z","KIND":"resume"}]}`,
			new(PullResponse), "$.actions[0].KIND"},
		"alias alone": {`{"v":1,"Settled":99}`, new(AckResponse), "$.Settled"},
	}
	for name, c := range cases {
		err := Decode(strings.NewReader(c.payload), c.value)
		var shape *ShapeError
		if !errors.As(err, &shape) || shape.Path != c.path || shape.Problem != "undeclared property" {
			t.Errorf("%s: Decode = %v, want %s undeclared", name, err, c.path)
		}
	}
}

func TestDecodeRejectsTrailingData(t *testing.T) {
	err := Decode(strings.NewReader(`{"v":1,"settled":0} {}`), new(AckResponse))
	if !errors.Is(err, ErrTrailingData) {
		t.Errorf("Decode = %v, want ErrTrailingData", err)
	}
	if err := Decode(strings.NewReader("{\"v\":1,\"settled\":0}\n"), new(AckResponse)); err != nil {
		t.Errorf("a trailing newline is not data: %v", err)
	}
}

func TestNullablePropertiesEncodeAsNull(t *testing.T) {
	encoded, err := json.Marshal(Run{Kind: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"kind":"none","since":null,"last_activity_at":null}`; string(encoded) != want {
		t.Errorf("Run = %s, want %s", encoded, want)
	}
}

func TestDecodeRejectsAMissingProperty(t *testing.T) {
	err := Decode(strings.NewReader(`{"v":1}`), new(AckResponse))
	var shape *ShapeError
	if !errors.As(err, &shape) || shape.Path != "$.settled" || shape.Problem != "required property is missing" {
		t.Errorf("Decode = %v, want a missing $.settled", err)
	}
}

func TestDecodeRejectsNullOnlyWhereTheFieldIsNotNullable(t *testing.T) {
	cases := map[string]struct{ payload, path string }{
		"top level":     {`null`, "$"},
		"nested scalar": {`{"v":1,"server_time":"2026-01-01T00:00:00Z","projects":[{"key":"k","result":null,"retry_after_seconds":null}]}`, "$.projects[0].result"},
		"array":         {`{"v":1,"server_time":"2026-01-01T00:00:00Z","projects":null}`, "$.projects"},
	}
	for name, c := range cases {
		err := Decode(strings.NewReader(c.payload), new(PushResponse))
		var shape *ShapeError
		if !errors.As(err, &shape) || shape.Path != c.path || shape.Problem != "must not be null" {
			t.Errorf("%s: Decode = %v, want %s must not be null", name, err, c.path)
		}
	}
	nullable := `{"v":1,"server_time":"2026-01-01T00:00:00Z","projects":[{"key":"k","result":"accepted","retry_after_seconds":null}]}`
	if err := Decode(strings.NewReader(nullable), new(PushResponse)); err != nil {
		t.Errorf("a null retry_after_seconds is allowed: %v", err)
	}
}

func TestDecodeChecksPropertiesInsideNullableObjects(t *testing.T) {
	ex := readExamples(t, "valid")
	for name, e := range ex {
		if e.Def != "PushRequest" {
			continue
		}
		var request map[string]any
		if err := json.Unmarshal(e.Value, &request); err != nil {
			t.Fatal(err)
		}
		budget, ok := request["budget"].(map[string]any)
		if !ok {
			continue
		}
		delete(budget, "openai")
		payload, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var shape *ShapeError
		if err := Decode(bytes.NewReader(payload), new(PushRequest)); !errors.As(err, &shape) || shape.Path != "$.budget.openai" {
			t.Errorf("%s without budget.openai: Decode = %v", name, err)
		}
		return
	}
	t.Fatal("no valid PushRequest example has a budget object")
}
