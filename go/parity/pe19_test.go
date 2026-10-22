package parity

// PE-19 (go/README.md): orch writes `orch <cmd>` where the reference JS writes
// `node ~/.config/opencode/orchestrator/bin/<cmd>.mjs` or `<cmd>.mjs`. The harness rewrites
// the JS side only, so every other byte stays compared. This file has no build tag so that
// `go test ./...` runs its unit tests; the harness uses it under the parity tag.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
)

// pe19Scripts is the closed list of the JS scripts whose names PE-19 maps.
const pe19Scripts = `claude-exec|codex-exec|opencode-exec|worker|supervisor|flip|mailbox|manifest|incident|usage|doctor|kickoff|watch|policy`

var (
	installedScriptCommand = regexp.MustCompile(`node ~/\.config/opencode/orchestrator/bin/(` + pe19Scripts + `)\.mjs`)
	scriptFileName         = regexp.MustCompile(`\b(` + pe19Scripts + `)\.mjs\b`)
)

// jsCommandsAsOrch maps the JS's command strings to orch's: the installed command line first,
// then a bare script file name.
func jsCommandsAsOrch(s string) string {
	s = installedScriptCommand.ReplaceAllString(s, "orch $1")
	return scriptFileName.ReplaceAllString(s, "orch $1")
}

// jsHelpAsOrch derives orch's --help text from a JS bridge's (lines 2-12 of its source): the
// trailing import line dropped, each line's leading "// " or "//" stripped, the names mapped,
// and the usage line's continuation lines re-aligned under its first "[", which the longer
// command name moves.
func jsHelpAsOrch(help string) (string, error) {
	lines := strings.Split(help, "\n")
	last := len(lines) - 1
	if !strings.HasPrefix(lines[last], "import ") {
		return "", fmt.Errorf("the JS help does not end in an import line: %q", lines[last])
	}
	lines = lines[:last]
	jsColumn, orchColumn := -1, -1
	for i, line := range lines {
		text, isComment := strings.CutPrefix(line, "//")
		if !isComment {
			return "", fmt.Errorf("JS help line %d is not a comment: %q", i+1, line)
		}
		text = strings.TrimPrefix(text, " ")
		mapped := jsCommandsAsOrch(text)
		continuation := strings.TrimLeft(text, " ")
		switch {
		case mapped != text:
			jsColumn, orchColumn = strings.Index(text, "["), strings.Index(mapped, "[")
		case jsColumn > 0 && strings.HasPrefix(continuation, "[") && len(text)-len(continuation) == jsColumn:
			mapped = strings.Repeat(" ", orchColumn) + continuation
		}
		lines[i] = mapped
	}
	return strings.Join(lines, "\n"), nil
}

// budgetRedIncident is the incident line a bridge records when the budget gate refuses it,
// after jsCommandsAsOrch: its summary names the bridge, so its fingerprint differs by runtime.
// The closing quote anchors the summary, so a longer one is not masked.
var budgetRedIncident = regexp.MustCompile(`(?m)^(\{"at":"[^"]*","fp":")([0-9a-f]{12})(","component":"bridge","kind":"limit","summary":"(orch (?:` + pe19Scripts + `) refused: budget red)")`)

// maskBudgetRedFingerprints masks the fp of every budget-red bridge incident line in s. With
// check set (the orch side), each masked fp must be the fingerprint of its own summary.
func maskBudgetRedFingerprints(s string, check bool) (string, error) {
	var mismatch error
	masked := budgetRedIncident.ReplaceAllStringFunc(s, func(line string) string {
		m := budgetRedIncident.FindStringSubmatch(line)
		if want := incident.Fingerprint("/parity-home", "bridge", "limit", m[4]); check && m[2] != want && mismatch == nil {
			mismatch = fmt.Errorf("budget-red incident fp %s, want %s (the fingerprint of %q)", m[2], want, m[4])
		}
		return m[1] + "<fp>" + m[3]
	})
	return masked, mismatch
}

// promptHead is a run record's prompt_head (the first 400 UTF-16 units of the prompt) as a
// JSON string.
var (
	promptHead    = regexp.MustCompile(`"prompt_head": ?"((?:[^"\\]|\\.)*)"`)
	mappedCommand = regexp.MustCompile(`\borch (?:` + pe19Scripts + `)\b`)
)

// withPromptHeadsAligned cuts both prompt_heads of a file to the JS's when the JS cut its
// head from a prompt with longer command strings: after jsCommandsAsOrch that head is shorter
// than orch's, or ends inside a command it could not map. A JS head that, without such a
// partial command, is a strict prefix of orch's and holds a mapped command or ended in a
// partial one is compared as that prefix; every other head is left as it is.
func withPromptHeadsAligned(jsContent, orchContent string) (string, string) {
	jsMatch, orchMatch := promptHead.FindStringSubmatchIndex(jsContent), promptHead.FindStringSubmatchIndex(orchContent)
	if jsMatch == nil || orchMatch == nil {
		return jsContent, orchContent
	}
	jsHead, orchHead := jsContent[jsMatch[2]:jsMatch[3]], orchContent[orchMatch[2]:orchMatch[3]]
	head, partial := withoutPartialCommand(jsHead)
	if jsHead == orchHead || len(head) >= len(orchHead) || !strings.HasPrefix(orchHead, head) || !partial && !mappedCommand.MatchString(head) {
		return jsContent, orchContent
	}
	return jsContent[:jsMatch[2]] + head + jsContent[jsMatch[3]:], orchContent[:orchMatch[2]] + head + orchContent[orchMatch[3]:]
}

// withoutPartialCommand drops a JS command string that head ends inside of: a proper prefix,
// at a word start, of `node ~/.config/opencode/orchestrator/bin/<name>.mjs`, or `<name>.`,
// `<name>.m` or `<name>.mj`.
func withoutPartialCommand(head string) (string, bool) {
	for start := range head {
		if start > 0 && head[start-1] != ' ' {
			continue
		}
		tail := head[start:]
		for _, name := range strings.Split(pe19Scripts, "|") {
			installed := "node ~/.config/opencode/orchestrator/bin/" + name + ".mjs"
			if len(tail) < len(installed) && strings.HasPrefix(installed, tail) ||
				tail == name+"." || tail == name+".m" || tail == name+".mj" {
				return head[:start], true
			}
		}
	}
	return head, false
}

func TestJSCommandsAsOrchMapsTheFourteenScripts(t *testing.T) {
	for _, name := range strings.Split(pe19Scripts, "|") {
		installed := "run: node ~/.config/opencode/orchestrator/bin/" + name + ".mjs --x"
		if got, want := jsCommandsAsOrch(installed), "run: orch "+name+" --x"; got != want {
			t.Errorf("jsCommandsAsOrch(%q) = %q, want %q", installed, got, want)
		}
		bare := "(" + name + ".mjs post)"
		if got, want := jsCommandsAsOrch(bare), "(orch "+name+" post)"; got != want {
			t.Errorf("jsCommandsAsOrch(%q) = %q, want %q", bare, got, want)
		}
	}
}

func TestJSCommandsAsOrchLeavesOtherNamesAlone(t *testing.T) {
	for _, s := range []string{
		"notascript.mjs",
		"orchestrator/models.json",
		"node ~/.config/opencode/orchestrator/bin/session.mjs",
		"lib/routing.mjs",
		"flipper.mjs",
	} {
		if got := jsCommandsAsOrch(s); got != s {
			t.Errorf("jsCommandsAsOrch(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestJSHelpAsOrchStripsCommentsAndRealigns(t *testing.T) {
	const js = "// Does a thing.\n//\n//   claude-exec.mjs [--a A]\n//                   [--b B]\n//\n// Exit codes: 0 ok.\nimport { spawn } from \"node:child_process\";"
	const want = "Does a thing.\n\n  orch claude-exec [--a A]\n                   [--b B]\n\nExit codes: 0 ok."
	got, err := jsHelpAsOrch(js)
	if err != nil || got != want {
		t.Errorf("jsHelpAsOrch = %q, %v\nwant %q", got, err, want)
	}
}

func TestJSHelpAsOrchRefusesAnUnexpectedShape(t *testing.T) {
	for _, js := range []string{"// header\nconst x = 1;", "header\nimport x from \"y\";"} {
		if _, err := jsHelpAsOrch(js); err == nil {
			t.Errorf("jsHelpAsOrch(%q) succeeded", js)
		}
	}
}

func TestMaskBudgetRedFingerprints(t *testing.T) {
	summary := "orch claude-exec refused: budget red"
	fp := incident.Fingerprint("/parity-home", "bridge", "limit", summary)
	line := func(fp, kind, summary string) string {
		return `{"at":"2026-10-22T12:00:00.000Z","fp":"` + fp + `","component":"bridge","kind":"` + kind + `","summary":"` + summary + `","detail":""}`
	}
	other := line("aaaaaaaaaaaa", "limit", "claude run ended on a session-limit message")
	got, err := maskBudgetRedFingerprints(line(fp, "limit", summary)+"\n"+other+"\n", true)
	if want := line("<fp>", "limit", summary) + "\n" + other + "\n"; err != nil || got != want {
		t.Errorf("mask = %q, %v\nwant %q", got, err, want)
	}
	if _, err := maskBudgetRedFingerprints(line("bbbbbbbbbbbb", "limit", summary), true); err == nil {
		t.Error("a wrong orch fingerprint passed the check")
	}
	if _, err := maskBudgetRedFingerprints(line("bbbbbbbbbbbb", "limit", summary), false); err != nil {
		t.Errorf("the JS side was checked: %v", err)
	}
	longer := line("cccccccccccc", "limit", summary+", extra")
	if got, err := maskBudgetRedFingerprints(longer, true); err != nil || got != longer {
		t.Errorf("a longer summary was masked: %q, %v", got, err)
	}
}

func TestWithPromptHeadsAligned(t *testing.T) {
	record := func(head string) string { return "{\n  \"prompt_head\": \"" + head + "\"\n}\n" }
	const orchHead = "run orch mailbox list then orch flip decide now"
	for name, c := range map[string]struct{ js, want string }{
		"mapped and shorter":   {"run orch mailbox list then", "run orch mailbox list then"},
		"cut inside a command": {"run orch mailbox list then node ~/.config/opencode/orchest", "run orch mailbox list then "},
		"cut inside a name":    {"run orch mailbox list then flip.mj", "run orch mailbox list then "},
	} {
		js, orch := withPromptHeadsAligned(record(c.js), record(orchHead))
		if want := record(c.want); js != want || orch != want {
			t.Errorf("%s: js %q, orch %q; want both %q", name, js, orch, want)
		}
	}
	for name, c := range map[string]struct{ js, orch string }{
		"no mapped command":  {"run then", "run then more"},
		"not a prefix":       {"run orch mailbox list now", orchHead},
		"js head is longer":  {orchHead + " x", orchHead},
		"partial not a word": {"run orch mailbox list thennode ~/", orchHead},
	} {
		if js, orch := withPromptHeadsAligned(record(c.js), record(c.orch)); js != record(c.js) || orch != record(c.orch) {
			t.Errorf("%s: heads changed: js %q, orch %q", name, js, orch)
		}
	}
}
