package routing

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
)

// The real registry from this checkout, as orchestrator/tests/routing.test.mjs uses.
const checkoutRegistry = "../../../orchestrator/models.json"

const goldenDir = "../../testdata/routing"

func loadRegistry(t *testing.T) *jsjson.Object {
	t.Helper()
	doc, err := registry.Load(checkoutRegistry)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func parse(t *testing.T, text string) *jsjson.Object {
	t.Helper()
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatalf("fixture %s: %v", text, err)
	}
	return value.(*jsjson.Object)
}

// soon is the JS test helper: an ISO time mins minutes from the wall clock.
func soon(mins int) string {
	return time.Now().Add(time.Duration(mins) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
}

func claudeUsage(t *testing.T, session, fable float64) *jsjson.Object {
	return parse(t, fmt.Sprintf(`{"ok": true, "plan": "team", "session": {"pct": %v, "resets_at": %q}, "weekly": {"pct": null, "resets_at": null}, "weekly_by_model": {"fable": {"pct": %v, "resets_at": %q}}}`,
		session, soon(120), fable, soon(600)))
}

func codexUsage(t *testing.T, fiveHour, weekly float64) *jsjson.Object {
	return parse(t, fmt.Sprintf(`{"ok": true, "plan": "plus", "five_hour": {"pct": %v, "resets_at": %q}, "weekly": {"pct": %v, "resets_at": %q}}`,
		fiveHour, soon(120), weekly, soon(5000)))
}

func usageOf(claude, codex *jsjson.Object) *jsjson.Object {
	usage := jsjson.NewObject()
	usage.Set("claude", claude)
	usage.Set("codex", codex)
	return usage
}

func expect[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestRoutingJS ports orchestrator/tests/routing.test.mjs; subtest names are the JS titles.
func TestRoutingJS(t *testing.T) {
	reg := loadRegistry(t)

	t.Run("green everywhere: Fable plans, Opus codes, OpenAI reviews (cross-vendor)", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 20, 30), codexUsage(t, 20, 5)), reg, time.Now(), map[string]bool{OpenAI: true})
		expect(t, "planner.model", r.Role("planner").Model, "anthropic/claude-fable-5-1")
		expect(t, "coder.model", r.Role("coder").Model, "anthropic/claude-opus-5-5")
		expect(t, "reviewer.vendor", r.Role("reviewer").Vendor, OpenAI)
		expect(t, "reviewer.cross_vendor", *r.Role("reviewer").CrossVendor, true)
		expect(t, "coder.via", r.Role("coder").Via, "claude-cli")
		expect(t, "reviewer.via", r.Role("reviewer").Via, "opencode")
	})

	t.Run("Fable weekly red: planning leaves Fable; coding follows 5-hour headroom", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 40, 90), codexUsage(t, 20, 5)), reg, time.Now(), nil)
		expect(t, "planner.model", r.Role("planner").Model, "anthropic/claude-opus-5-5")
		expect(t, "coder.vendor", r.Role("coder").Vendor, OpenAI) // 80 vs 60 headroom
		expectReason(t, r, `Fable weekly cap is red`)
	})

	t.Run("Claude session red: everything Anthropic fails over to OpenAI, review flips to Anthropic? no: cross vendor unavailable -> same vendor cheap", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 95, 50), codexUsage(t, 20, 5)), reg, time.Now(), nil)
		expect(t, "coder.vendor", r.Role("coder").Vendor, OpenAI)
		expect(t, "reviewer.vendor", r.Role("reviewer").Vendor, OpenAI)
		expect(t, "reviewer.cross_vendor", *r.Role("reviewer").CrossVendor, false)
	})

	t.Run("Codex red: OpenAI roles fail over to Anthropic", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 10, 10), codexUsage(t, 99, 40)), reg, time.Now(), nil)
		expect(t, "reviewer.vendor", r.Role("reviewer").Vendor, Anthropic)
		expect(t, "reviewer.model", r.Role("reviewer").Model, "anthropic/claude-sonnet-5-5")
		expect(t, "reviewer.cross_vendor", *r.Role("reviewer").CrossVendor, false)
	})

	t.Run("both red: paused", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 99, 99), codexUsage(t, 99, 99)), reg, time.Now(), nil)
		expect(t, "paused", r.Paused, true)
	})

	t.Run("a red session window stays red even if it resets within 20 minutes (only a flag is set)", func(t *testing.T) {
		c := claudeUsage(t, 90, 10)
		jsjson.Path(c, "session").(*jsjson.Object).Set("resets_at", soon(5))
		p := VendorPostures(usageOf(c, codexUsage(t, 1, 1)), reg, time.Now())
		session, _ := p.Anthropic.Window("session")
		expect(t, "anthropic.windows.session", session, Red)
		expect(t, "anthropic.resets_soon", p.Anthropic.ResetsSoon, true)
	})

	t.Run("unknown Claude usage does not crash and routes to OpenAI", func(t *testing.T) {
		r := ResolveRoles(usageOf(parse(t, `{"ok": false, "error": "no token"}`), codexUsage(t, 10, 1)), reg, time.Now(), nil)
		expect(t, "coder.vendor", r.Role("coder").Vendor, OpenAI)
		expectReason(t, r, `Claude usage unknown`)
	})

	t.Run("effort scales with task size and defaults to medium-size policy", func(t *testing.T) {
		for _, c := range []struct{ role, size, want string }{
			{"coder", "trivial", "low"},
			{"coder", "hard", "xhigh"},
			{"planner", "hard", "max"},
			{"reviewer", "", "medium"}, // JS passes undefined
			{"verifier", "large", "medium"},
		} {
			got, ok := EffortFor(reg, c.role, c.size)
			if !ok || got != c.want {
				t.Errorf("EffortFor(%s, %q) = %q, %v; want %q", c.role, c.size, got, ok, c.want)
			}
		}
	})

	t.Run("frontier tiers need a GREEN frontier posture: amber Fable routes routine planning to Opus", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 30, 70), codexUsage(t, 10, 5)), reg, time.Now(), nil)
		expect(t, "planner.model", r.Role("planner").Model, "anthropic/claude-opus-5-5")
		expectReason(t, r, `Fable weekly cap is amber`)
	})

	t.Run("5-hour window steers; an amber weekly does not block or demote a vendor", func(t *testing.T) {
		x := codexUsage(t, 10, 70) // weekly amber
		r := ResolveRoles(usageOf(claudeUsage(t, 50, 10), x), reg, time.Now(), map[string]bool{OpenAI: true})
		expect(t, "coder.vendor", r.Role("coder").Vendor, OpenAI)
		expect(t, "openai.overall", r.Postures.OpenAI.Overall, Green)
	})

	t.Run("coder is balanced to the vendor with clearly more 5-hour headroom", func(t *testing.T) {
		for _, c := range []struct {
			claudeSession, codexFiveHour float64
			want                         string
		}{
			{60, 10, OpenAI},
			{20, 25, Anthropic}, // within margin: registry order
			{10, 70, Anthropic},
		} {
			r := ResolveRoles(usageOf(claudeUsage(t, c.claudeSession, 10), codexUsage(t, c.codexFiveHour, 10)), reg, time.Now(), nil)
			expect(t, fmt.Sprintf("coder.vendor (claude %v%%, codex %v%%)", c.claudeSession, c.codexFiveHour), r.Role("coder").Vendor, c.want)
		}
	})

	t.Run("a red weekly still blocks the vendor", func(t *testing.T) {
		r := ResolveRoles(usageOf(claudeUsage(t, 10, 10), codexUsage(t, 5, 95)), reg, time.Now(), nil)
		expect(t, "reviewer.vendor", r.Role("reviewer").Vendor, Anthropic)
	})

	t.Run("a preference naming a tier missing from the registry is skipped, not a crash", func(t *testing.T) {
		reg := loadRegistry(t)
		jsjson.Path(reg, "roles", "coder").(*jsjson.Object).Set("prefer", []any{"anthropic.nonexistent", "openai.strong", "anthropic.strong"})
		r := ResolveRoles(usageOf(claudeUsage(t, 10, 10), codexUsage(t, 60, 10)), reg, time.Now(), nil)
		expect(t, "coder.vendor", r.Role("coder").Vendor, Anthropic)
	})
}

func expectReason(t *testing.T, r Resolved, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(strings.Join(r.Reasons, " ")) {
		t.Errorf("reasons = %q, want one matching %q", r.Reasons, pattern)
	}
}

func TestEffortForRoleWithoutPolicy(t *testing.T) {
	if effort, ok := EffortFor(loadRegistry(t), "utility", "hard"); ok {
		t.Errorf("EffortFor(utility) = %q, want no effort (JS null)", effort)
	}
}

// routingCase is one entry of testdata/routing/cases.json "routing"; registry is the case's
// own registry, nil to use the checkout's models.json.
type routingCase struct {
	name     string
	usage    *jsjson.Object
	channels map[string]bool
	registry *jsjson.Object
}

func loadRoutingCases(t *testing.T) (now time.Time, cases []routingCase) {
	t.Helper()
	data, err := os.ReadFile(goldenDir + "/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	doc := parse(t, string(data))
	nowMs, ok := jsjson.ParseDate(jsjson.Path(doc, "now").(string))
	if !ok {
		t.Fatal("cases.json: now does not parse")
	}
	for _, item := range jsjson.Path(doc, "routing").([]any) {
		channels := map[string]bool{}
		channelDoc := jsjson.Path(item, "channels").(*jsjson.Object)
		for _, vendor := range channelDoc.Keys() {
			channels[vendor] = jsjson.Path(channelDoc, vendor) == true
		}
		usage, _ := jsjson.Path(item, "usage").(*jsjson.Object)
		caseRegistry, _ := jsjson.Path(item, "registry").(*jsjson.Object)
		cases = append(cases, routingCase{name: jsjson.Path(item, "name").(string), usage: usage, channels: channels, registry: caseRegistry})
	}
	return time.UnixMilli(nowMs), cases
}

// TestGoldens compares the resolved routing (as JSON) and the prompt summary with what the JS
// produced for the same inputs (testdata/routing/gen.mjs).
func TestGoldens(t *testing.T) {
	checkout := loadRegistry(t)
	now, cases := loadRoutingCases(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := checkout
			if c.registry != nil {
				reg = c.registry
			}
			resolved := ResolveRoles(c.usage, reg, now, c.channels)
			gotJSON, err := jsjson.MarshalIndent(resolved.JSON(), "  ")
			if err != nil {
				t.Fatal(err)
			}
			compareGolden(t, c.name+".resolved.golden", string(gotJSON))
			compareGolden(t, c.name+".prompt.golden", SummarizeForPrompt(resolved, c.usage))
		})
	}
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(goldenDir + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the JS output\n got: %s\nwant: %s", name, got, want)
	}
}

func TestWorstIgnoresUnknownLevelNames(t *testing.T) {
	expect(t, "Worst()", Worst(), Green)
	expect(t, "Worst(amber, bogus)", Worst(Amber, "bogus"), Amber)
	expect(t, "Worst(unknown, green)", Worst(Unknown, Green), Unknown)
}
