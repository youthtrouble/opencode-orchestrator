//go:build parity

package parity

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type sessionCase struct {
	name  string
	args  []string
	stdin string
	seed  seed
}

// freshUpdatedAt is the one fresh value a beat writes, rewritten only in its exact place and
// format.
var freshUpdatedAt = regexp.MustCompile(`(?m)^(  "updated_at": ")` + isoMillis.String() + `("\n\}\n)`)

func normalizeUpdatedAt(s string) string {
	return freshUpdatedAt.ReplaceAllString(s, "${1}<iso>${2}")
}

// seedSession writes a door's previous record as given (creating .orchestrator/sessions/).
func seedSession(door, content string) seed {
	return seedProjectFile(".orchestrator/sessions/"+door+".json", content)
}

// hookInput is a Claude Code hook's stdin. cwd is relative, so it names each runtime's own
// sandbox project.
func hookInput(event, cwd string) string {
	return `{"session_id":"s1","transcript_path":"/secret/t.jsonl","cwd":"` + cwd + `","hook_event_name":"` + event + `","prompt":"secret prompt"}`
}

// testSession compares `orch session` with bin/session.mjs. Every beat writes a fresh
// updated_at, which is normalised only where it has its exact ISO-ms format.
func (h *harness) testSession(t *testing.T) {
	project := seedProjectDir(".orchestrator")
	previous := `{"door":"claude","session_id":"old","state":"done","now":"n\ud800","slice":"s","progress":{"done":2,"total":5},"updated_at":"2026-10-18T10:00:00.000Z"}`
	// show prints records as stored, so a hand-edited lone surrogate escape is the one known
	// jsjson divergence (see "JSON compatibility"); its records are well-formed.
	stored := strings.Replace(previous, `\ud800`, "😀", 1)
	hook := []string{"beat", "--door", "claude", "--hook"}
	cases := []sessionCase{
		{name: "usage/none"},
		{name: "usage/unknown", args: []string{"frob"}},
		{name: "usage/proto", args: []string{"constructor"}},
		{name: "beat/no-door", args: []string{"beat"}},
		{name: "beat/bad-door", args: []string{"beat", "--door", "codex"}},
		{name: "beat/bad-state", args: []string{"beat", "--door", "claude", "--state", "busy"}},
		{name: "beat/progress-zero-total", args: []string{"beat", "--door", "claude", "--progress", "1/0"}},
		{name: "beat/progress-words", args: []string{"beat", "--door", "claude", "--progress", "one/two"}},
		{name: "beat/progress-unicode-digits", args: []string{"beat", "--door", "claude", "--progress", "١/٢"}},
		{name: "beat/progress-newline", args: []string{"beat", "--door", "claude", "--progress", "1/2\n"}},
		{name: "beat/progress-empty", args: []string{"beat", "--door", "claude", "--progress", ""}, seed: seedSession("claude", previous)},
		{name: "beat/value-missing", args: []string{"beat", "--door", "claude", "--now"}},
		{name: "beat/value-is-option", args: []string{"beat", "--door", "--now", "x"}},
		{name: "beat/unknown-option", args: []string{"beat", "--door", "claude", "--colour", "red"}},
		{name: "beat/positional", args: []string{"beat", "claude"}},
		{name: "beat/bare-dashes", args: []string{"beat", "--"}},
		{name: "beat/hook-flag-takes-no-value", args: []string{"beat", "--door", "claude", "--hook", "x"}},

		{name: "beat/creates-project", args: []string{"beat", "--door", "opencode"}},
		{name: "beat/all-fields", args: []string{"beat", "--door", "claude", "--state", "done", "--now", "reviewing", "--slice", "wt3", "--progress", "007/10", "--session", "abc"}},
		{name: "beat/repeated-key-last-wins", args: []string{"beat", "--door", "opencode", "--door", "claude"}},
		{name: "beat/cut", args: []string{"beat", "--door", "claude", "--now", strings.Repeat("a", 199) + "😀tail", "--slice", strings.Repeat("é", 50)}},
		{name: "beat/malformed-utf8", args: []string{"beat", "--door", "claude", "--now", "a\xffb", "--session", "\xc3"}},
		{name: "beat/json-special-text", args: []string{"beat", "--door", "claude", "--now", "<a&b> \"q\" \\ \u2028 \x01"}},
		{name: "beat/progress-huge", args: []string{"beat", "--door", "claude", "--progress", "99999999999999999999/1"}},
		{name: "beat/progress-infinite", args: []string{"beat", "--door", "claude", "--progress", "1/" + strings.Repeat("9", 400)}},
		{name: "beat/merge", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", previous)},
		{name: "beat/clear", args: []string{"beat", "--door", "claude", "--now", "", "--slice", "", "--session", ""}, seed: seedSession("claude", previous)},
		{name: "beat/previous-odd-shapes", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", `{"session_id":5,"now":["n"],"slice":null,"progress":{"done":"1","total":2},"extra":true,"__proto__":{"now":"p"}}`)},
		{name: "beat/previous-progress-extra-keys", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", `{"progress":{"total":3e0,"done":1.5,"x":1}}`)},
		{name: "beat/previous-unparsable", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", `{"now":`)},
		{name: "beat/previous-array", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", `["x"]`)},
		{name: "beat/previous-null", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", `null`)},
		{name: "beat/previous-bom", args: []string{"beat", "--door", "claude"}, seed: seedSession("claude", "\ufeff{}")},
		{name: "beat/other-door-untouched", args: []string{"beat", "--door", "opencode"}, seed: seedSession("claude", previous)},
		{name: "beat/previous-is-directory", args: []string{"beat", "--door", "claude"}, seed: seedProjectDir(".orchestrator/sessions/claude.json")},
		{name: "beat/sessions-is-file", args: []string{"beat", "--door", "claude"}, seed: seedProjectFile(".orchestrator/sessions", "")},
		{name: "beat/orchestrator-is-file", args: []string{"beat", "--door", "claude"}, seed: seedProjectFile(".orchestrator", "")},
		{name: "beat/cwd", args: []string{"beat", "--door", "claude", "--cwd", "sub"}},
		{name: "beat/cwd-empty-is-working-dir", args: []string{"beat", "--door", "claude", "--cwd", ""}},

		{name: "hook/stop", args: hook, stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/user-prompt-submit", args: hook, stdin: hookInput("UserPromptSubmit", "."), seed: seeds(project, seedSession("claude", previous))},
		{name: "hook/session-start", args: hook, stdin: hookInput("SessionStart", "."), seed: project},
		{name: "hook/session-end", args: hook, stdin: hookInput("SessionEnd", "."), seed: seeds(project, seedSession("claude", previous))},
		{name: "hook/unfollowed-event", args: hook, stdin: hookInput("PreToolUse", "."), seed: project},
		{name: "hook/proto-event", args: hook, stdin: hookInput("constructor", "."), seed: project},
		{name: "hook/event-not-string", args: hook, stdin: `{"cwd":".","hook_event_name":5}`, seed: project},
		{name: "hook/no-session-id-keeps-previous", args: hook, stdin: `{"cwd":".","hook_event_name":"Stop"}`, seed: seeds(project, seedSession("claude", previous))},
		{name: "hook/session-flag-without-hook-id", args: append(hook, "--session", "flag"), stdin: `{"cwd":".","hook_event_name":"Stop","session_id":null}`, seed: project},
		{name: "hook/hook-id-wins-over-flag", args: append(hook, "--session", "flag", "--now", "n"), stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/lone-surrogate-session", args: hook, stdin: `{"cwd":".","hook_event_name":"Stop","session_id":"a\ud83db"}`, seed: project},
		{name: "hook/relative-cwd", args: hook, stdin: hookInput("Stop", "sub"), seed: seedProjectDir("sub/.orchestrator")},
		{name: "hook/cwd-flag-overrides", args: append(hook, "--cwd", "sub"), stdin: hookInput("Stop", "/nonexistent"), seed: seedProjectDir("sub/.orchestrator")},
		{name: "hook/outside-project", args: hook, stdin: hookInput("Stop", ".")},
		{name: "hook/orchestrator-is-file", args: hook, stdin: hookInput("Stop", "."), seed: seedProjectFile(".orchestrator", "")},
		{name: "hook/not-json", args: hook, stdin: "nope", seed: project},
		{name: "hook/empty-stdin", args: hook, seed: project},
		{name: "hook/not-object", args: hook, stdin: `[{"cwd":"."}]`, seed: project},
		{name: "hook/no-cwd", args: hook, stdin: `{"hook_event_name":"Stop"}`, seed: project},
		{name: "hook/cwd-nul", args: hook, stdin: `{"hook_event_name":"Stop","cwd":".\u0000x"}`, seed: project},
		{name: "hook/cwd-not-string", args: hook, stdin: `{"hook_event_name":"Stop","cwd":7}`, seed: project},
		{name: "hook/bad-door-exits-1", args: []string{"beat", "--door", "codex", "--hook"}, stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/state-refused", args: append(hook, "--state", "done"), stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/bad-progress-exits-1", args: append(hook, "--progress", "x"), stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/empty-progress-exits-1", args: append(hook, "--progress", ""), stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/unknown-option-exits-1", args: append(hook, "--colour"), stdin: hookInput("Stop", "."), seed: project},
		{name: "hook/previous-is-directory", args: hook, stdin: hookInput("Stop", "."), seed: seedProjectDir(".orchestrator/sessions/claude.json")},

		{name: "show/empty", args: []string{"show"}, seed: project},
		{name: "show/both-doors", args: []string{"show"}, seed: seeds(seedSession("opencode", `{"door":"opencode","n":1e21,"s":"\u2028<"}`), seedSession("claude", stored))},
		{name: "show/any-json", args: []string{"show"}, seed: seeds(seedSession("opencode", `[1, "x"]`), seedSession("claude", `5`))},
		{name: "show/unparsable", args: []string{"show"}, seed: seeds(seedSession("opencode", `{`), seedSession("claude", stored))},
		{name: "show/directory", args: []string{"show"}, seed: seedProjectDir(".orchestrator/sessions/claude.json")},
		{name: "show/other-files-ignored", args: []string{"show"}, seed: seedSession("codex", `{}`)},
		{name: "show/cwd", args: []string{"show", "--cwd", "sub"}, seed: seedProjectDir("sub/.orchestrator")},
		{name: "show/unknown-option", args: []string{"show", "--door", "claude"}, seed: project},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.sessionInvocation(c)) })
	}
	t.Run("hook/never-stores-the-prompt", func(t *testing.T) {
		js, orch := h.runPair(t, h.sessionInvocation(sessionCase{args: hook, stdin: hookInput("Stop", "."), seed: project}))
		for name, o := range map[string]outcome{"js": js, "orch": orch} {
			text := o.stdout + o.stderr
			for _, content := range o.files {
				text += content
			}
			if o.exitCode != 0 || strings.Contains(text, "secret") {
				t.Errorf("%s: exit %d; the prompt or transcript path reached its output or files:\n%s", name, o.exitCode, text)
			}
		}
		compareOutcomes(t, js, orch)
	})
	t.Run("show/outside-project", func(t *testing.T) {
		h.compareOutsideProject(t, h.sessionInvocation(sessionCase{args: []string{"show"}}), "--cwd")
	})
}

func (h *harness) sessionInvocation(c sessionCase) invocation {
	return invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "session.mjs")}, c.args...),
		orch:         append([]string{h.orchBin, "session"}, c.args...),
		stdin:        []byte(c.stdin),
		seed:         c.seed,
		extraFiles:   []string{"sub/.orchestrator/sessions/claude.json"},
		normalize:    normalizeUpdatedAt,
		projectPaths: true,
	}
}
