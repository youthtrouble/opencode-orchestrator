//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// flipAt is an ISO timestamp flip.json holds under "at" (a record's, paused's, a gate's,
// pending_launch's or previous's), as written with indent 2.
var flipAt = regexp.MustCompile(`("at": ")(` + isoMillis.String() + `)(")`)

// normalizeFlip rewrites the fresh values of a flip run that starts after since: an "at"
// written in the run, and the id and at of a newly appended mailbox line. An "at" is fresh
// when it lies between since and the time it is normalised, which is after the output was
// collected; any other "at" was seeded (fixed past or future values, or a record another run
// wrote before the case) and is compared verbatim, so a command that wrongly rewrote a kept
// timestamp cannot be masked. ISO strings of four-digit years order as their times do.
func normalizeFlip(since time.Time) func(string) string {
	start := jsjson.ISOString(since)
	return func(s string) string {
		collected := jsjson.ISOString(time.Now())
		s = flipAt.ReplaceAllStringFunc(s, func(field string) string {
			parts := flipAt.FindStringSubmatch(field)
			if at := parts[2]; at < start || at > collected {
				return field
			}
			return parts[1] + "<iso>" + parts[3]
		})
		return workerMailbox.ReplaceAllString(s, "${1}<uuid>${2}<iso>${3}")
	}
}

type flipCase struct {
	name      string
	args      []string
	seed      seed
	crash     crashClass
	errorLine string
	leftover  string // the rename of flip.json fails: the JS leaves flip.json.tmp holding this (PE-21)
}

// flipInvocation runs bin/flip.mjs and `orch flip` in the sandbox project, never with a --cwd
// outside it. Its fresh values are those written after it is built. A `run` it makes must
// find the conductor lane locked (see flipRunInvocation); launches go through runFlipLaunch.
func (h *harness) flipInvocation(c flipCase) invocation {
	return invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "flip.mjs")}, c.args...),
		orch:         append([]string{h.orchBin, "flip"}, c.args...),
		env:          []string{noPostRunRefresh},
		seed:         c.seed,
		crash:        c.crash,
		errorLine:    c.errorLine,
		projectPaths: true,
		extraFiles:   []string{".git/info/exclude", worktreeExclude},
		normalize:    normalizeFlip(time.Now()),
	}
}

func seedFlip(content string) seed { return seedProjectFile(".orchestrator/flip.json", content) }

// testFlip compares `orch flip decide|run|pause|gate|status` and its usage errors with
// bin/flip.mjs.
func (h *harness) testFlip(t *testing.T) {
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	const orchestratorIsFile = "Error: EEXIST: file already exists, mkdir '$PROJECT/.orchestrator'"
	const flipIsDir = "Error: EISDIR: illegal operation on a directory, rename '$PROJECT/.orchestrator/flip.json.tmp' -> '$PROJECT/.orchestrator/flip.json'"
	typeError := func(on string) string { return "TypeError: Cannot create property 'awaiting_human' on " + on }
	seeded := `{"to":"claude","paused":{"door":"claude","at":"2026-01-01T00:00:00.000Z","reason":"r"},"awaiting_human":{"door":"opencode","at":"2026-01-02T00:00:00.000Z","reason":"g","question":null},"z":1}`
	cases := []flipCase{
		{name: "usage/no-command"},
		{name: "usage/unknown-command", args: []string{"frob", "--active", "claude"}},
		{name: "usage/cwd-bare", args: []string{"--cwd"}, crash: crashC1, errorLine: cwdBoolean},
		{name: "usage/cwd-bare-with-command", args: []string{"status", "--cwd"}, crash: crashC1, errorLine: cwdBoolean},

		{name: "status/empty-project", args: []string{"status"}, seed: seedProjectDir(".orchestrator")},
		{name: "status/record", args: []string{"status"}, seed: seedFlip(`{"at":"2026-01-01T00:00:00.000Z","from":"opencode","to":"claude","reason":null,"previous":null}`)},
		{name: "status/zero", args: []string{"status"}, seed: seedFlip(`0`)},
		{name: "status/false", args: []string{"status"}, seed: seedFlip(`false`)},
		{name: "status/null", args: []string{"status"}, seed: seedFlip(`null`)},
		{name: "status/empty-string", args: []string{"status"}, seed: seedFlip(`""`)},
		{name: "status/string", args: []string{"status"}, seed: seedFlip(`"abc"`)},
		{name: "status/array", args: []string{"status"}, seed: seedFlip(`[1,2]`)},
		{name: "status/number", args: []string{"status"}, seed: seedFlip(`5`)},
		{name: "status/unparsable", args: []string{"status"}, seed: seedFlip(`{"to":`)},
		{name: "status/bom", args: []string{"status"}, seed: seedFlip("\xef\xbb\xbf{}")},
		{name: "status/invalid-utf8", args: []string{"status"}, seed: seedFlip("{\"to\":\"a\xffb\"}")},
		{name: "status/directory", args: []string{"status"}, seed: seedProjectDir(".orchestrator/flip.json")},
		{name: "status/prototype-keys", args: []string{"status"}, seed: seedFlip(`{"__proto__":{"a":1},"constructor":2}`)},
		{name: "status/cwd-relative", args: []string{"status", "--cwd", "sub"}, seed: seedProjectFile("sub/.orchestrator/flip.json", `{"to":"sub"}`)},

		{name: "pause/fresh", args: []string{"pause"}},
		{name: "pause/keeps-keys", args: []string{"pause"}, seed: seedFlip(seeded)},
		{name: "pause/active", args: []string{"pause", "--active", "claude", "--reason", "R"}},
		{name: "pause/active-bare", args: []string{"pause", "--active"}},
		{name: "pause/reason-bare", args: []string{"pause", "--reason"}},
		{name: "pause/string", args: []string{"pause"}, seed: seedFlip(`"abc"`)},
		{name: "pause/array", args: []string{"pause"}, seed: seedFlip(`[1,2]`)},
		{name: "pause/number", args: []string{"pause"}, seed: seedFlip(`5`)},
		{name: "pause/prototype-keys", args: []string{"pause"}, seed: seedFlip(`{"__proto__":{"a":1},"constructor":2}`)},
		{name: "pause/orchestrator-is-file", args: []string{"pause"}, seed: seedProjectFile(".orchestrator", ""), crash: crashC1, errorLine: orchestratorIsFile},
		{name: "pause/PE-21-flip-is-directory", args: []string{"pause"}, seed: seedProjectDir(".orchestrator/flip.json"), errorLine: flipIsDir, leftover: `"paused"`},
		{name: "pause/brief", args: []string{"pause", "--brief", ".orchestrator/briefs/conductor-kickoff.md"}, seed: seedFlip(seeded)},
		{name: "pause/brief-bare", args: []string{"pause", "--brief"}},
		{name: "pause/brief-empty", args: []string{"pause", "--brief", ""}},
		{name: "pause/mailbox-is-file", args: []string{"pause"}, seed: seedProjectFile(".orchestrator/mailbox", "x")},

		{name: "gate/fresh", args: []string{"gate"}},
		{name: "gate/deletes-paused-keeps-gate-position", args: []string{"gate"}, seed: seedFlip(seeded)},
		{name: "gate/appends", args: []string{"gate"}, seed: seedFlip(`{"to":"claude","paused":{"door":"claude"},"z":1}`)},
		{name: "gate/question", args: []string{"gate", "--question", "Q"}},
		{name: "gate/question-bare", args: []string{"gate", "--question"}},
		{name: "gate/reason-long", args: []string{"gate", "--reason", strings.Repeat("long reason ", 10)}},
		{name: "gate/reason-bare", args: []string{"gate", "--reason"}},
		{name: "gate/active", args: []string{"gate", "--active", "claude"}},
		{name: "gate/active-bare", args: []string{"gate", "--active"}},
		{name: "gate/number", args: []string{"gate"}, seed: seedFlip(`5`), crash: crashC1, errorLine: typeError("number '5'")},
		{name: "gate/fraction", args: []string{"gate"}, seed: seedFlip(`1.5`), crash: crashC1, errorLine: typeError("number '1.5'")},
		{name: "gate/string", args: []string{"gate"}, seed: seedFlip(`"abc"`), crash: crashC1, errorLine: typeError("string 'abc'")},
		{name: "gate/true", args: []string{"gate"}, seed: seedFlip(`true`), crash: crashC1, errorLine: typeError("boolean 'true'")},
		{name: "gate/orchestrator-is-file", args: []string{"gate"}, seed: seedProjectFile(".orchestrator", ""), crash: crashC1, errorLine: orchestratorIsFile},
		{name: "gate/clear", args: []string{"gate", "--clear"}, seed: seedFlip(seeded)},
		{name: "gate/clear-without-gate", args: []string{"gate", "--clear"}, seed: seedFlip(`{"to":"claude"}`)},
		{name: "gate/clear-queues-relaunch", args: []string{"gate", "--clear"}, seed: seedFlip(`{"to":"claude","awaiting_human":{"door":"claude","at":"2026-01-02T00:00:00.000Z","reason":"g","question":"q"},"z":1}`)},
		{name: "gate/clear-keeps-pending-launch", args: []string{"gate", "--clear"}, seed: seedFlip(`{"pending_launch":{"to":"opencode","from":"claude","reason":"handback","resume":null,"at":"2026-01-01T00:00:00.000Z"},"awaiting_human":{"door":"claude"}}`)},
		{name: "gate/clear-queues-relaunch-without-door", args: []string{"gate", "--clear"}, seed: seedFlip(`{"awaiting_human":"yes"}`)},
		// --no-relaunch (kickoff) also drops seeded's paused (Sol R9).
		{name: "gate/clear-no-relaunch", args: []string{"gate", "--clear", "--no-relaunch"}, seed: seedFlip(seeded)},
		{name: "gate/clear-no-relaunch-drops-pending-launch", args: []string{"gate", "--clear", "--no-relaunch"}, seed: seedFlip(`{"pending_launch":{"to":"opencode","from":"claude","reason":"handback","resume":null,"at":"2026-01-01T00:00:00.000Z"},"awaiting_human":{"door":"claude"},"z":1}`)},
		{name: "gate/clear-no-relaunch-drops-paused", args: []string{"gate", "--clear", "--no-relaunch"}, seed: seedFlip(`{"paused":{"at":"2026-01-01T00:00:00.000Z","door":"claude","reason":"r","brief":"b.md"},"z":1}`)},
		{name: "gate/clear-fresh", args: []string{"gate", "--clear"}, seed: seedProjectDir(".orchestrator")},
		{name: "gate/clear-string", args: []string{"gate", "--clear"}, seed: seedFlip(`"abc"`)},
		{name: "gate/clear-number", args: []string{"gate", "--clear"}, seed: seedFlip(`5`)},
		{name: "gate/clear-array", args: []string{"gate", "--clear"}, seed: seedFlip(`[1]`)},
		{name: "gate/clear-null", args: []string{"gate", "--clear"}, seed: seedFlip(`null`)},
		{name: "gate/clear-PE-21-directory", args: []string{"gate", "--clear"}, seed: seedProjectDir(".orchestrator/flip.json"), errorLine: flipIsDir, leftover: "{}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.leftover != "" {
				h.compareFlipPE21FailedRename(t, h.flipInvocation(c), c.leftover)
				return
			}
			h.comparePair(t, h.flipInvocation(c))
		})
	}
	for _, c := range []flipCase{
		{name: "status/outside-project", args: []string{"status"}},
		{name: "gate/clear-outside-project", args: []string{"gate", "--clear"}},
	} {
		t.Run(c.name, func(t *testing.T) { h.compareOutsideProject(t, h.flipInvocation(c), "--cwd") })
	}
	t.Run("gate/array", h.testFlipGateArray)
	t.Run("gate/PE-1", h.testFlipGateSubjectCut)
	t.Run("decide", h.testFlipDecide)
	t.Run("cross-runtime", h.testFlipCrossRuntime)
	t.Run("run", h.testFlipRun)
}

// testFlipGateArray asserts that gate over an array flip.json records the gate in both
// runtimes: the array is replaced by an object, which JSON keeps the gate in.
func (h *harness) testFlipGateArray(t *testing.T) {
	const flipJSON = "project/flip.json"
	js, orch := h.runPair(t, h.flipInvocation(flipCase{args: []string{"gate"}, seed: seedFlip(`[1,2]`)}))
	if !strings.HasPrefix(js.files[flipJSON], "{\n  \"awaiting_human\": {") {
		t.Errorf("js flip.json = %q, want the gate", js.files[flipJSON])
	}
	compareOutcomes(t, js, orch)
}

// compareFlipPE21FailedRename asserts PE-21 for a write of flip.json onto a directory: both
// runtimes die of the EISDIR of the rename onto it (C1, inv.errorLine), but orch's line names
// its own `flip.json.tmp-<random>`, which it removes, where the JS names and leaves
// `flip.json.tmp` holding written. The rest is compared.
func (h *harness) compareFlipPE21FailedRename(t *testing.T, inv invocation, written string) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	if !privateTempSuffix.MatchString(orch.stderr) {
		t.Errorf("orch stderr = %q, want orch's temp name", orch.stderr)
	}
	assertPE21FailedRename(t, &js, &orch, "project/flip.json", written)
	if err := crashError(crashC1, inv.errorLine, js, orch); err != nil {
		t.Error(err)
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

// comparePE21OpenVsRename asserts PE-21 for a private write onto a directory: the JS dies of
// the EISDIR of its open (inv.errorLine names '<file>'), orch of the rename of its own
// `<file>.tmp-<random>` onto the directory, leaving no temp. The rest is compared.
func (h *harness) comparePE21OpenVsRename(t *testing.T, inv invocation) {
	t.Helper()
	js, orch := h.runPair(t, inv)
	openPrefix := "Error: EISDIR: illegal operation on a directory, open '"
	file, isOpen := strings.CutPrefix(inv.errorLine, openPrefix)
	if !isOpen {
		t.Fatalf("errorLine %q is no EISDIR open", inv.errorLine)
	}
	file = strings.TrimSuffix(file, "'")
	renameLine := "Error: EISDIR: illegal operation on a directory, rename '" + file + ".tmp' -> '" + file + "'\n"
	if js.exitCode != 1 || !slices.Contains(strings.Split(js.stderr, "\n"), inv.errorLine) {
		t.Errorf("js: exit %d, stderr %q; want %q", js.exitCode, js.stderr, inv.errorLine)
	}
	if orch.exitCode != 1 || privateTempSuffix.ReplaceAllString(orch.stderr, ".tmp") != renameLine || !privateTempSuffix.MatchString(orch.stderr) {
		t.Errorf("orch: exit %d, stderr %q; want %q with orch's temp name", orch.exitCode, orch.stderr, renameLine)
	}
	for name := range orch.files {
		if strings.Contains(name, ".tmp") {
			t.Errorf("orch left %s", name)
		}
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

// testFlipGateSubjectCut asserts PE-1 in the human's message: the 80-unit cut splits an emoji,
// which the JS writes as the escape of its lone high surrogate and orch as U+FFFD.
func (h *harness) testFlipGateSubjectCut(t *testing.T) {
	const messages = "project/mailbox/messages.jsonl"
	reason := strings.Repeat("r", 79) + "😀 tail"
	js, orch := h.runPair(t, h.flipInvocation(flipCase{args: []string{"gate", "--reason", reason}}))
	cut := `"subject":"awaiting human: ` + strings.Repeat("r", 79)
	if !strings.Contains(js.files[messages], cut+`\ud83d"`) {
		t.Errorf("js subject: %s", js.files[messages])
	}
	if !strings.Contains(orch.files[messages], cut+"�\"") {
		t.Errorf("orch subject: %s", orch.files[messages])
	}
	js.files[messages] = strings.Replace(js.files[messages], `\ud83d"`, "�\"", 1)
	compareOutcomes(t, js, orch)
}

// flipCaches are the usage caches decide reads, built when the test starts so that resets
// relative to that time are the same document in both runtimes, which run seconds apart.
type flipCaches struct {
	stable, freshlyReset, amber, openaiRed, anthropicRed, bothRed, unknown, green seed
}

func newFlipCaches(t *testing.T) flipCaches {
	t.Helper()
	caches := readCacheFixtures(t)
	fixture := func(name string) any {
		value, _ := caches.Get(name)
		return value
	}
	builtAt := time.Now()
	// withOpenAIReset is the green cache whose OpenAI 5-hour window resets after minutes.
	withOpenAIReset := func(minutes int) *jsjson.Object {
		data, err := jsjson.Marshal(fixture("green"))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := jsjson.Unmarshal(data)
		if err != nil {
			t.Fatal(err)
		}
		window := jsjson.Path(copied, "codex", "five_hour").(*jsjson.Object)
		window.Set("resets_at", jsjson.ISOString(builtAt.Add(time.Duration(minutes)*time.Minute)))
		return copied.(*jsjson.Object)
	}
	openaiRed := jsjson.Spread(fixture("green"))
	openaiRed.Set("codex", jsjson.Path(fixture("red"), "codex"))
	return flipCaches{
		stable:       seedCache(withOpenAIReset(120), builtAt), // stably green: reset 180 min ago
		freshlyReset: seedCache(withOpenAIReset(297), builtAt), // reset 3 min ago, within the 10 min grace
		amber:        seedCache(fixture("amber"), builtAt),
		openaiRed:    seedCache(openaiRed, builtAt),
		anthropicRed: seedCache(anthropicRed(t, caches), builtAt),
		bothRed:      seedCache(fixture("red"), builtAt),
		unknown:      seedCache(fixture("unknown"), builtAt),
		green:        seedCache(fixture("green"), builtAt), // 2099 resets read as freshly reset
	}
}

// testFlipDecide compares decide over fresh caches (none collects: each runtime must leave the
// seeded usage.json as it was) and flip.json values for the cooldown. The minute margins
// around every threshold dwarf the seconds between seeding and running.
func (h *harness) testFlipDecide(t *testing.T) {
	c := newFlipCaches(t)
	ago := func(minutes int) string {
		return jsjson.ISOString(time.Now().Add(-time.Duration(minutes) * time.Minute))
	}
	claude := []string{"decide", "--active", "claude"}
	opencode := []string{"decide", "--active", "opencode"}
	decision := func(action, to, reason string) string {
		return `{"action":"` + action + `","to":` + to + `,"reason":"` + reason + `"}` + "\n"
	}
	var (
		handback    = decision("handback", `"opencode"`, "home door opencode is stably green again")
		cooling     = decision("stay", `"claude"`, "cooldown after a recent flip")
		notStable   = decision("stay", `"claude"`, "home door opencode not stably green yet")
		pause       = decision("pause", "null", "both vendors red or unknown; wait for the nearest reset")
		nonDoorFlip = func(vendor string) string {
			return decision("flip", `"opencode"`, vendor+" is red; openai has headroom")
		}
	)
	cases := []struct {
		name  string
		args  []string
		cache seed
		flip  string
		want  string // the decision both runtimes print
	}{
		{"opencode/both-green", opencode, c.green, "", decision("stay", `"opencode"`, "home door with headroom")},
		{"opencode/openai-red", opencode, c.openaiRed, "", decision("flip", `"claude"`, "openai is red; anthropic has headroom")},
		{"opencode/both-red", opencode, c.bothRed, "", pause},
		{"opencode/unknown", opencode, c.unknown, "", pause},
		{"claude/anthropic-red", claude, c.anthropicRed, "", decision("flip", `"opencode"`, "anthropic is red; openai has headroom")},
		{"claude/stable", claude, c.stable, "", handback},
		{"claude/stable-flipped-2-min-ago", claude, c.stable, `{"at":"` + ago(2) + `"}`, cooling},
		{"claude/stable-flipped-30-min-ago", claude, c.stable, `{"at":"` + ago(30) + `"}`, handback},
		{"claude/freshly-reset", claude, c.freshlyReset, "", notStable},
		{"claude/amber", claude, c.amber, "", notStable},
		{"claude/green-2099", claude, c.green, "", notStable},
		{"claude/flip-unparsable", claude, c.stable, `{"at":`, handback},
		{"claude/flip-null", claude, c.stable, `null`, handback},
		{"claude/flip-string", claude, c.stable, `"abc"`, handback},
		{"claude/flip-array", claude, c.stable, `[1]`, handback},
		{"claude/flip-future", claude, c.stable, `{"at":2030}`, cooling},
		{"claude/flip-garbage", claude, c.stable, `{"at":"garbage"}`, handback},
		{"claude/flip-paused-without-at", claude, c.stable, `{"paused":{"at":"` + ago(2) + `","door":"claude"}}`, handback},
		{"active/absent", []string{"decide"}, c.stable, "", ""},
		{"active/empty", []string{"decide", "--active", ""}, c.stable, "", ""},
		{"active/bare", []string{"decide", "--active"}, c.stable, "", nonDoorFlip("undefined")},
		{"active/gemini", []string{"decide", "--active", "gemini"}, c.stable, "", nonDoorFlip("undefined")},
		{"active/constructor", []string{"decide", "--active", "constructor"}, c.stable, "", nonDoorFlip("function Object() { [native code] }")},
		{"active/__proto__", []string{"decide", "--active", "__proto__"}, c.stable, "", nonDoorFlip("[object Object]")},
	}
	for _, dc := range cases {
		t.Run(dc.name, func(t *testing.T) {
			s := dc.cache
			if dc.flip != "" {
				s = seeds(s, seedFlip(dc.flip))
			}
			h.compareDecide(t, h.flipInvocation(flipCase{args: dc.args, seed: s}), func(js, orch *outcome) {
				if js.stdout != dc.want {
					t.Errorf("js decided %q, want %q", js.stdout, dc.want)
				}
			})
		})
	}
	t.Run("PE-15", func(t *testing.T) {
		legacy := time.Now().Add(-2 * time.Minute).Format("2006-01-02 15:04:05")
		inv := h.flipInvocation(flipCase{args: claude, seed: seeds(c.stable, seedFlip(`{"at":"`+legacy+`"}`))})
		h.compareDecide(t, inv, func(js, orch *outcome) {
			const jsWant = `{"action":"stay","to":"claude","reason":"cooldown after a recent flip"}` + "\n"
			const orchWant = `{"action":"handback","to":"opencode","reason":"home door opencode is stably green again"}` + "\n"
			if js.stdout != jsWant || orch.stdout != orchWant {
				t.Errorf("PE-15: js %q (want %q), orch %q (want %q)", js.stdout, jsWant, orch.stdout, orchWant)
			}
			orch.stdout = js.stdout
		})
	})
}

// compareDecide runs a decide pair, requires that neither runtime rewrote the seeded usage
// cache (no collection), lets difference assert a documented difference, then compares.
func (h *harness) compareDecide(t *testing.T, inv invocation, difference func(js, orch *outcome)) {
	t.Helper()
	const cache = "state/usage.json"
	var seededCache string
	seed := inv.seed
	inv.seed = func(t *testing.T, sb sandbox) {
		seed(t, sb)
		data, err := os.ReadFile(filepath.Join(sb.home, ".local/state/opencode/orchestrator/usage.json"))
		if err != nil {
			t.Fatal(err)
		}
		seededCache = string(data)
	}
	js, orch := h.runPair(t, inv)
	for runtime, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.files[cache] != seededCache {
			t.Errorf("%s rewrote the usage cache (collected): %s", runtime, o.files[cache])
		}
	}
	if difference != nil {
		difference(&js, &orch)
	}
	compareOutcomes(t, js, orch)
}

// testFlipCrossRuntime has each runtime's pause and gate write flip.json and both runtimes'
// status and gate --clear read it: the reads must agree whichever runtime wrote the file.
func (h *harness) testFlipCrossRuntime(t *testing.T) {
	writers := map[string][]string{
		"js":   {h.node, filepath.Join(h.orchJS, "bin", "flip.mjs")},
		"orch": {h.orchBin, "flip"},
	}
	for writer, argv := range writers {
		for _, write := range [][]string{{"pause", "--active", "claude", "--reason", "both red"}, {"gate", "--question", "q1"}} {
			sb := h.newSandbox(t)
			written := h.run(t, sb, append(append([]string{}, argv...), write...), nil, []string{noPostRunRefresh})
			content, ok := written.files["project/flip.json"]
			if written.exitCode != 0 || !ok {
				t.Fatalf("%s %v: exit %d, stderr %s", writer, write, written.exitCode, written.stderr)
			}
			for _, read := range [][]string{{"status"}, {"gate", "--clear"}} {
				t.Run(writer+"-"+write[0]+"/"+read[len(read)-1], func(t *testing.T) {
					h.comparePair(t, h.flipInvocation(flipCase{args: read, seed: seedFlip(content)}))
				})
			}
		}
	}
}
