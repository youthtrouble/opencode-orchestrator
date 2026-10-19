package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

func TestStatusPrintsAnyParsedValueOrNotStarted(t *testing.T) {
	for content, want := range map[string]string{
		"":                         `{"status":"not_started"}`,
		`null`:                     `null`,
		`5`:                        `5`,
		`"x"`:                      `"x"`,
		`[]`:                       `[]`,
		`{"b":1,"a":2}`:            `{"b":1,"a":2}`,
		`{bad`:                     `{"status":"not_started"}`,
		"\ufeff{}":                 `{"status":"not_started"}`,
		`{"lanes":{"1":{},"a":1}}`: `{"lanes":{"1":{},"a":1}}`,
	} {
		f := newFixture(t)
		if content != "" {
			f.write("supervisor-state.json", content)
		}
		if got := compact(t, f.sup.Status()); got != want {
			t.Errorf("status over %q = %s, want %s", content, got, want)
		}
	}
	f := newFixture(t)
	f.mkdir("supervisor-state.json")
	if got := compact(t, f.sup.Status()); got != `{"status":"not_started"}` {
		t.Errorf("status over a directory = %s", got)
	}
}

func TestStopCreatesTheEmptyStopFile(t *testing.T) {
	f := newFixture(t)
	line, err := f.sup.Stop()
	if err != nil || line != "stop requested; workers are unaffected" || f.read("supervisor.stop") != "" {
		t.Fatalf("Stop() = %q, %v", line, err)
	}
	g := newFixture(t)
	g.mkdir("supervisor.stop")
	_, err = g.sup.Stop()
	wantError(t, err, "EISDIR: illegal operation on a directory, open '"+g.path("supervisor.stop")+"'")
}

func TestAck(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 5_000_000, time.UTC)
	const stamp = `"at":"2026-10-19T12:00:00.005Z"`
	for _, tc := range []struct {
		name, state, acks string
		event, by         any
		wantLine          string
		wantRefused       bool
		wantAcks          string // compact; "" means not written
	}{
		{"a known id", `{"events":{"e1":{"status":"queued"}}}`, "", "e1", "coordinator", "acknowledged (receipt, not acceptance)", false, `{"e1":{"by":"coordinator",` + stamp + `}}`},
		{"an unknown id", `{"events":{}}`, "", "e2", "coordinator", "unknown event id", true, ""},
		{"no --event", `{"events":{"e1":1}}`, "", nil, "coordinator", "unknown event id", true, ""},
		{"a bare --event reads key true", `{"events":{"true":1}}`, "", true, true, "acknowledged (receipt, not acceptance)", false, `{"true":{"by":true,` + stamp + `}}`},
		{"a falsy stored event", `{"events":{"e1":0}}`, "", "e1", "x", "unknown event id", true, ""},
		{"an existing ack keeps its position", `{"events":{"e1":1}}`, `{"e1":{"by":"old"},"e0":1}`, "e1", "X", "acknowledged (receipt, not acceptance)", false, `{"e1":{"by":"X",` + stamp + `},"e0":1}`},
		{"constructor is inherited", `{"events":{}}`, "", "constructor", "coordinator", "acknowledged (receipt, not acceptance)", false, `{"constructor":{"by":"coordinator",` + stamp + `}}`},
		{"__proto__ without an own key sets the prototype", `{"events":{}}`, `{"a":1}`, "__proto__", "coordinator", "acknowledged (receipt, not acceptance)", false, `{"a":1}`},
		{"__proto__ with an own key", `{"events":{}}`, `{"__proto__":{"x":1}}`, "__proto__", "c", "acknowledged (receipt, not acceptance)", false, `{"__proto__":{"by":"c",` + stamp + `}}`},
		{"PE-16: events a string read as {}", `{"events":"ab"}`, "", "length", "c", "unknown event id", true, ""},
		{"PE-16: array acks written as an object", `{"events":{"0":1}}`, `[1]`, "0", "c", "acknowledged (receipt, not acceptance)", false, `{"0":{"by":"c",` + stamp + `}}`},
		{"a primitive state has no events", `5`, "", "e1", "c", "unknown event id", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("supervisor-state.json", tc.state)
			if tc.acks != "" {
				f.write("supervisor-acks.json", tc.acks)
			}
			got, err := f.sup.Ack(tc.event, tc.by, at)
			if err != nil || got.Line != tc.wantLine || got.Refused != tc.wantRefused {
				t.Fatalf("Ack = %+v, %v", got, err)
			}
			if tc.wantAcks == "" {
				if tc.acks == "" && nodefs.Exists(f.path("supervisor-acks.json")) {
					t.Error("acks written on a refusal")
				}
				return
			}
			if got := compactFile(t, f.read("supervisor-acks.json")); got != tc.wantAcks {
				t.Errorf("acks = %s, want %s", got, tc.wantAcks)
			}
		})
	}
}

func TestAckCrashes(t *testing.T) {
	for _, tc := range []struct{ name, state, acks, want string }{
		{"state null", `null`, "", "Cannot read properties of null (reading 'events')"},
		{"acks null", `{"events":{"e1":1}}`, `null`, "Cannot set properties of null (setting 'e1')"},
		{"acks a number", `{"events":{"e1":1}}`, `5`, "Cannot create property 'e1' on number '5'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("supervisor-state.json", tc.state)
			if tc.acks != "" {
				f.write("supervisor-acks.json", tc.acks)
			}
			_, err := f.sup.Ack("e1", "c", time.Now())
			wantError(t, err, tc.want)
		})
	}
	f := newFixture(t)
	f.write("supervisor-state.json", `{"events":{"e1":1}}`)
	f.mkdir("supervisor-acks.json")
	_, err := f.sup.Ack("e1", "c", time.Now())
	acks := f.path("supervisor-acks.json")
	wantError(t, err, "EISDIR: illegal operation on a directory, rename '"+acks+".tmp' -> '"+acks+"'")
	if !strings.Contains(f.read("supervisor-acks.json.tmp"), `"e1"`) {
		t.Error("the .tmp file should be left")
	}
}
