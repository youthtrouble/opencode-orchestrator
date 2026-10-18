package flip

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

const goldenDir = "../../testdata/routing"

var now = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func parse(t *testing.T, text string) *jsjson.Object {
	t.Helper()
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatalf("fixture %s: %v", text, err)
	}
	return value.(*jsjson.Object)
}

func isoIn(minutes int) string {
	return now.Add(time.Duration(minutes) * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

// postures is the JS test helper P: both vendors available with the given overall levels and
// main windows resetting in the given minutes (120 by default).
func postures(t *testing.T, openai, anthropic string, openaiResetInMin int) routing.Postures {
	return routing.Postures{
		OpenAI: routing.VendorPosture{Available: true, Overall: openai,
			Detail: parse(t, fmt.Sprintf(`{"five_hour": {"resets_at": %q, "window_mins": 300}}`, isoIn(openaiResetInMin)))},
		Anthropic: routing.VendorPosture{Available: true, Overall: anthropic,
			Detail: parse(t, fmt.Sprintf(`{"session": {"resets_at": %q}}`, isoIn(120)))},
	}
}

// TestFlipJS ports orchestrator/tests/flip.test.mjs; subtest names are the JS titles.
func TestFlipJS(t *testing.T) {
	reg := parse(t, `{"conductor": {"home": "opencode", "green_grace_minutes": 10, "flip_cooldown_minutes": 10}}`)
	decide := func(active string, p routing.Postures, lastFlipAt *time.Time) Decision {
		return Decide(Input{Active: active, Postures: p, Registry: reg, Now: now, LastFlipAt: lastFlipAt})
	}
	expectAction := func(t *testing.T, d Decision, want string) {
		t.Helper()
		if d.Action != want {
			t.Errorf("action = %s (%s), want %s", d.Action, d.Reason, want)
		}
	}

	t.Run("own vendor red and other usable -> forced flip", func(t *testing.T) {
		expectAction(t, decide("opencode", postures(t, "red", "green", 120), nil), ActionFlip)
		if d := decide("claude", postures(t, "amber", "red", 120), nil); d.To != DoorOpencode {
			t.Errorf("to = %q, want opencode", d.To)
		}
	})
	t.Run("both red -> pause", func(t *testing.T) {
		expectAction(t, decide("opencode", postures(t, "red", "red", 120), nil), ActionPause)
	})
	t.Run("away conductor hands back when home is stably green", func(t *testing.T) {
		d := decide("claude", postures(t, "green", "green", 120), nil)
		if d.Action != ActionHandback || d.To != DoorOpencode {
			t.Errorf("decision = [%s %s], want [handback opencode]", d.Action, d.To)
		}
	})
	t.Run("home just reset (<grace) is not stable; amber home is not enough", func(t *testing.T) {
		expectAction(t, decide("claude", postures(t, "green", "green", 295), nil), ActionStay)
		expectAction(t, decide("claude", postures(t, "amber", "green", 120), nil), ActionStay)
	})
	t.Run("cooldown suppresses a hand-back but never a forced flip", func(t *testing.T) {
		recent := now.Add(-2 * time.Minute)
		expectAction(t, decide("claude", postures(t, "green", "green", 120), &recent), ActionStay)
		expectAction(t, decide("claude", postures(t, "green", "red", 120), &recent), ActionFlip)
	})
	t.Run("home conductor with headroom stays", func(t *testing.T) {
		expectAction(t, decide("opencode", postures(t, "amber", "green", 120), nil), ActionStay)
	})
}

// TestGoldens runs Decide on the postures of the routing cases (testdata/routing/cases.json)
// and compares the decisions, as JSON, with the JS output (flip.golden).
func TestGoldens(t *testing.T) {
	reg, err := registry.Load("../../../orchestrator/models.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(goldenDir + "/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := parse(t, string(data))
	nowMs, _ := jsjson.ParseDate(jsjson.Path(cases, "now").(string))
	caseNow := time.UnixMilli(nowMs)
	posturesByCase := map[string]routing.Postures{}
	for _, item := range jsjson.Path(cases, "routing").([]any) {
		usage, _ := jsjson.Path(item, "usage").(*jsjson.Object)
		postureRegistry := reg // as gen.mjs: the case's own registry, if any, sets the thresholds
		if caseRegistry, hasOwn := jsjson.Path(item, "registry").(*jsjson.Object); hasOwn {
			postureRegistry = caseRegistry
		}
		posturesByCase[jsjson.Path(item, "name").(string)] = routing.VendorPostures(usage, postureRegistry, caseNow)
	}

	var decisions []any
	for _, item := range jsjson.Path(cases, "flip").([]any) {
		in := Input{
			Active:   jsjson.Path(item, "active").(string),
			Postures: posturesByCase[jsjson.Path(item, "routing").(string)],
			Registry: reg,
			Now:      caseNow,
		}
		if at, isString := jsjson.Path(item, "last_flip_at").(string); isString {
			ms, _ := jsjson.ParseDate(at)
			lastFlipAt := time.UnixMilli(ms)
			in.LastFlipAt = &lastFlipAt
		}
		decisions = append(decisions, Decide(in).JSON())
	}
	got, err := jsjson.MarshalIndent(decisions, "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(goldenDir + "/flip.golden")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("flip decisions differ from the JS output\n got: %s\nwant: %s", got, want)
	}
}

func TestOtherDoor(t *testing.T) {
	for door, want := range map[string]string{"opencode": "claude", "claude": "opencode", "bogus": "opencode"} {
		if got := OtherDoor(door); got != want {
			t.Errorf("OtherDoor(%q) = %q, want %q", door, got, want)
		}
	}
}
