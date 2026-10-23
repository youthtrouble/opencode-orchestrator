//go:build parity

package parity

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type supervisorCase struct {
	name      string
	args      []string
	seed      seed
	crash     crashClass
	errorLine string
}

// supervisorInvocation runs bin/supervisor.mjs and `orch supervisor` in the sandbox project,
// never with a --cwd outside it. The only fresh value is the `at` of an ack written by the
// run (normalised as flip's are: seeded ones are compared verbatim).
func (h *harness) supervisorInvocation(c supervisorCase) invocation {
	inv := invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, c.args...),
		orch:         append([]string{h.orchBin, "supervisor"}, c.args...),
		env:          []string{noPostRunRefresh},
		seed:         c.seed,
		crash:        c.crash,
		errorLine:    c.errorLine,
		projectPaths: true,
		normalize:    normalizeFlip(time.Now()),
	}
	if len(c.args) > 0 && c.args[0] == "ack" {
		// PE-39: only ack creates this persistent, empty transaction sidecar.
		// Keep any unexpected content visible to the comparison.
		inv.normalizeWrites = func(o outcome, _ map[string]string) outcome {
			const lock = "project/supervisor-acks.json.lock"
			if content, exists := o.files[lock]; exists && content == "" {
				delete(o.files, lock)
			}
			return o
		}
	}
	return inv
}

func seedSupervisorState(content string) seed {
	return seedProjectFile(".orchestrator/supervisor-state.json", content)
}

func seedAcks(content string) seed {
	return seedProjectFile(".orchestrator/supervisor-acks.json", content)
}

// realisticSupervisorState has the shape of a long-running supervisor's state: events of
// every status, an index-like lane, and the keys a restart leaves behind.
const realisticSupervisorState = `{
  "events": {
    "r1:finished": {"status": "queued", "reason": "finished", "lane": "a", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1792404300, "mailbox_id": "m1", "channels": []},
    "r2:failed": {"status": "delivery_failed", "reason": "failed", "lane": "1", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1792404300, "mailbox_id": null, "channels": [{"type": "command", "status": "delivery_failed", "error": "exit 3"}]},
    "r3:check:1792404300": {"status": "cli_invoked", "reason": "check", "lane": "b", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1792404300, "mailbox_id": "m3"},
    "r4:quiet:1:0": {"status": "intent_persisted", "reason": "quiet", "lane": "b", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1792404300},
    "r5:stale_lock": {"status": "delivery_uncertain", "reason": "stale_lock", "lane": "c", "created_at": "2026-10-19T10:00:00.000Z", "retry_after": 1.5}
  },
  "lanes": {
    "a": {"run": "r1", "phase": "finished", "breadth": "standard", "quiet": false, "checked_at": "2026-10-19T10:00:00.000Z", "next_check": 1792404300, "next_check_at": "2026-10-19T10:05:00.000Z"},
    "1": {"run": "r2", "phase": "failed", "breadth": "focused", "quiet": false, "checked_at": "2026-10-19T10:00:00.000Z", "next_check": 1792404120, "next_check_at": "2026-10-19T10:02:00.000Z"}
  },
  "checked_at": "2026-10-19T10:00:00.000Z",
  "pid": 30560,
  "status": "running",
  "stopped_at": "2026-10-18T09:00:00.123Z",
  "last_flip_check": 1792404000,
  "last_relaunch_at": 1792403000,
  "awaiting_human_logged": false
}`

// testSupervisor compares `orch supervisor` with bin/supervisor.mjs: status, stop and ack and
// the usage errors synchronously, serve and start as daemons (supervisor_serve_test.go).
func (h *harness) testSupervisor(t *testing.T) {
	h.testNestedProjectRoot(t, "supervisor")
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	const stopIsDir = "Error: EISDIR: illegal operation on a directory, open '$PROJECT/.orchestrator/supervisor.stop'"
	events := seedSupervisorState(`{"events":{"e1":{"status":"queued"},"e2":{"status":"queued"},"zero":0},"lanes":{}}`)
	ack := func(extra ...string) []string { return append([]string{"ack"}, extra...) }
	cases := []supervisorCase{
		{name: "usage/no-command"},
		{name: "usage/cwd-bare", args: []string{"--cwd"}, crash: crashC1, errorLine: cwdBoolean},
		{name: "usage/cwd-bare-with-command", args: []string{"status", "--cwd"}, crash: crashC1, errorLine: cwdBoolean},
		{name: "usage/cwd-relative", args: []string{"status", "--cwd", "sub"}, seed: seedProjectFile("sub/.orchestrator/supervisor-state.json", `{"status":"sub"}`)},

		{name: "status/empty-project", args: []string{"status"}, seed: seedProjectDir(".orchestrator")},
		{name: "status/empty-object", args: []string{"status"}, seed: seedSupervisorState(`{}`)},
		{name: "status/realistic", args: []string{"status"}, seed: seedSupervisorState(realisticSupervisorState)},
		{name: "status/null", args: []string{"status"}, seed: seedSupervisorState(`null`)},
		{name: "status/number", args: []string{"status"}, seed: seedSupervisorState(`5`)},
		{name: "status/string", args: []string{"status"}, seed: seedSupervisorState(`"x"`)},
		{name: "status/array", args: []string{"status"}, seed: seedSupervisorState(`[]`)},
		{name: "status/unparsable", args: []string{"status"}, seed: seedSupervisorState(`{"events":`)},
		{name: "status/bom", args: []string{"status"}, seed: seedSupervisorState("\xef\xbb\xbf{}")},
		{name: "status/directory", args: []string{"status"}, seed: seedProjectDir(".orchestrator/supervisor-state.json")},

		{name: "stop/fresh", args: []string{"stop"}, seed: seedProjectDir(".orchestrator")},
		{name: "stop/existing", args: []string{"stop"}, seed: seedProjectFile(".orchestrator/supervisor.stop", "")},
		{name: "stop/stop-is-directory", args: []string{"stop"}, seed: seedProjectDir(".orchestrator/supervisor.stop"), crash: crashC1, errorLine: stopIsDir},

		{name: "ack/known", args: ack("--event", "e1"), seed: events},
		{name: "ack/unknown", args: ack("--event", "nope"), seed: events},
		{name: "ack/event-bare", args: ack("--event"), seed: events},
		{name: "ack/no-event", args: ack(), seed: events},
		{name: "ack/no-state", args: ack("--event", "e1"), seed: seedProjectDir(".orchestrator")},
		{name: "ack/by", args: ack("--event", "e1", "--by", "X"), seed: events},
		{name: "ack/by-bare", args: ack("--event", "e1", "--by"), seed: events},
		{name: "ack/existing-same-id", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`{"e0":{"by":"a","at":"2026-01-01T00:00:00.000Z"},"e1":{"by":"old","at":"2026-01-02T00:00:00.000Z"},"e9":1}`))},
		{name: "ack/existing-other-id", args: ack("--event", "e2"), seed: seeds(events, seedAcks(`{"e1":{"by":"a","at":"2026-01-01T00:00:00.000Z"}}`))},
		{name: "ack/constructor-own-event", args: ack("--event", "constructor"), seed: seedSupervisorState(`{"events":{"constructor":1}}`)},
		{name: "ack/proto", args: ack("--event", "__proto__"), seed: seeds(seedSupervisorState(`{"events":{"__proto__":1}}`), seedAcks(`{"a":1}`))},
		{name: "ack/proto-own-key", args: ack("--event", "__proto__"), seed: seeds(seedSupervisorState(`{"events":{"__proto__":1}}`), seedAcks(`{"__proto__":{"x":1},"a":1}`))},
		{name: "ack/falsy-event", args: ack("--event", "zero"), seed: events},
		{name: "ack/state-null", args: ack("--event", "e1"), seed: seedSupervisorState(`null`), crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'events')"},
		{name: "ack/state-null-no-event", args: ack(), seed: seedSupervisorState(`null`)},
		{name: "ack/acks-null", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`null`)), crash: crashC1, errorLine: "TypeError: Cannot set properties of null (setting 'e1')"},
		{name: "ack/acks-number", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`5`)), crash: crashC1, errorLine: "TypeError: Cannot create property 'e1' on number '5'"},
		{name: "ack/acks-unparsable", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`{`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.supervisorInvocation(c)) })
	}
	for _, c := range []supervisorCase{
		{name: "status/outside-project", args: []string{"status"}},
		{name: "stop/outside-project", args: []string{"stop"}},
		{name: "stop/orchestrator-is-file", args: []string{"stop"}, seed: seedProjectFile(".orchestrator", "")},
		{name: "ack/outside-project", args: ack("--event", "e1")},
	} {
		t.Run(c.name, func(t *testing.T) { h.compareOutsideProject(t, h.supervisorInvocation(c), "--cwd") })
	}
	t.Run("usage/PE-49-unknown-command", h.testSupervisorUnknownCommand)
	t.Run("ack/PE-21-acks-directory", h.testSupervisorAckAcksDirectory)
	t.Run("ack/PE-16-string-events", h.testSupervisorAckStringEvents)
	t.Run("ack/PE-16-array-acks", h.testSupervisorAckArrayAcks)
	t.Run("ack/PE-39-own-events-only", h.testSupervisorAckOwnEvents)
	t.Run("cross-runtime", h.testSupervisorCrossRuntime)
	t.Run("serve", h.testSupervisorServe)
	t.Run("start", h.testSupervisorStart)
}

// PE-39: the frozen JS accepts inherited Object.prototype names as event ids.
// Orch requires a truthy own event key and leaves existing receipts untouched.
func (h *harness) testSupervisorAckOwnEvents(t *testing.T) {
	for _, id := range []string{"constructor", "toString", "__proto__"} {
		t.Run(id, func(t *testing.T) {
			const acks = "project/supervisor-acks.json"
			js, orch := h.runPair(t, h.supervisorInvocation(supervisorCase{
				args: []string{"ack", "--event", id},
				seed: seeds(seedSupervisorState(`{"events":{}}`), seedAcks(`{"a":1}`)),
			}))
			if js.exitCode != 0 || js.stdout != "acknowledged (receipt, not acceptance)\n" || js.stderr != "" {
				t.Errorf("JS inherited %s: exit=%d stdout=%q stderr=%q", id, js.exitCode, js.stdout, js.stderr)
			}
			if id != "__proto__" && !strings.Contains(js.files[acks], `"`+id+`": {`) {
				t.Errorf("JS receipt missing %s: %s", id, js.files[acks])
			}
			if id == "__proto__" && js.files[acks] != "{\n  \"a\": 1\n}\n" {
				t.Errorf("JS __proto__ unexpectedly changed receipts: %q", js.files[acks])
			}
			if orch.exitCode != 1 || orch.stdout != "" || orch.stderr != "unknown event id\n" || orch.files[acks] != `{"a":1}` {
				t.Errorf("orch inherited %s: exit=%d stdout=%q stderr=%q acks=%q", id, orch.exitCode, orch.stdout, orch.stderr, orch.files[acks])
			}
			js.files[acks] = orch.files[acks]
			js.exitCode, js.stdout, js.stderr = orch.exitCode, orch.stdout, orch.stderr
			compareOutcomes(t, js, orch)
		})
	}
}

// acksIsDir is the JS crash line of an ack whose acks file is a directory: the rename of the
// fixed temp fails.
const acksIsDir = "Error: EISDIR: illegal operation on a directory, rename '$PROJECT/.orchestrator/supervisor-acks.json.tmp' -> '$PROJECT/.orchestrator/supervisor-acks.json'"

// testSupervisorAckAcksDirectory asserts PE-21 over an acks file that is a directory: both
// runtimes die of the rename's EISDIR (C1), but orch's line names its own
// `<acks>.tmp-<random>`, which it removes, where the JS names and leaves `<acks>.tmp`.
func (h *harness) testSupervisorAckAcksDirectory(t *testing.T) {
	js, orch := h.runPair(t, h.supervisorInvocation(supervisorCase{
		args:      []string{"ack", "--event", "e1"},
		seed:      seeds(seedSupervisorState(`{"events":{"e1":{"status":"queued"}}}`), seedProjectDir(".orchestrator/supervisor-acks.json")),
		errorLine: acksIsDir, // its $PROJECT has runPair spell the sandbox path in stderr
	}))
	orchLine := "^" + strings.Replace(regexp.QuoteMeta(acksIsDir), `\.tmp'`, `\.tmp-[0-9]+'`, 1) + "\n$"
	if !regexp.MustCompile(orchLine).MatchString(orch.stderr) {
		t.Errorf("orch stderr = %q, want a match for %s", orch.stderr, orchLine)
	}
	assertPE21FailedRename(t, &js, &orch, "project/supervisor-acks.json", `"e1"`)
	if err := crashError(crashC1, acksIsDir, js, orch); err != nil {
		t.Error(err)
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

// testSupervisorUnknownCommand asserts PE-49: the JS prints nothing and exits 0 for an unknown
// command (pinned); orch prints the usage line and exits 2. Neither writes anything.
func (h *harness) testSupervisorUnknownCommand(t *testing.T) {
	js, orch := h.runPair(t, h.supervisorInvocation(supervisorCase{args: []string{"foo"}}))
	if js.exitCode != 0 || js.stdout != "" || js.stderr != "" {
		t.Errorf("js: exit %d, stdout %q, stderr %q; want the pinned silent exit 0", js.exitCode, js.stdout, js.stderr)
	}
	if orch.exitCode != 2 || orch.stdout != "" || orch.stderr != "commands: start | serve | status | stop | ack --event ID\n" {
		t.Errorf("orch: exit %d, stdout %q, stderr %q; want the usage line, exit 2", orch.exitCode, orch.stdout, orch.stderr)
	}
	compareOutcomes(t, outcome{exitCode: 2, stderr: orch.stderr, files: js.files}, orch)
}

// testSupervisorAckStringEvents asserts PE-16(4): with events "ab", the JS acknowledges
// `--event length` (a string's own property) and orch reads the events as {}.
func (h *harness) testSupervisorAckStringEvents(t *testing.T) {
	const acks = "project/supervisor-acks.json"
	js, orch := h.runPair(t, h.supervisorInvocation(supervisorCase{args: []string{"ack", "--event", "length"}, seed: seedSupervisorState(`{"events":"ab"}`)}))
	if js.exitCode != 0 || js.stdout != "acknowledged (receipt, not acceptance)\n" || !strings.Contains(js.files[acks], `"length": {`) {
		t.Errorf("js: exit %d, stdout %q, acks %q", js.exitCode, js.stdout, js.files[acks])
	}
	if orch.exitCode != 1 || orch.stderr != "unknown event id\n" || orch.files[acks] != "" {
		t.Errorf("orch: exit %d, stderr %q, acks %q", orch.exitCode, orch.stderr, orch.files[acks])
	}
	delete(js.files, acks)
	compareOutcomes(t, outcome{exitCode: 1, stderr: orch.stderr, files: js.files}, orch)
}

// testSupervisorAckArrayAcks asserts PE-16(2): over an array acks file the JS replaces the
// array element the index-like event names; orch writes an object.
func (h *harness) testSupervisorAckArrayAcks(t *testing.T) {
	const acks = "project/supervisor-acks.json"
	js, orch := h.runPair(t, h.supervisorInvocation(supervisorCase{
		args: []string{"ack", "--event", "0"},
		seed: seeds(seedSupervisorState(`{"events":{"0":1}}`), seedAcks(`[1]`)),
	}))
	if !strings.HasPrefix(js.files[acks], "[\n  {\n    \"by\": \"coordinator\",") {
		t.Errorf("js acks = %q, want the array with element 0 replaced", js.files[acks])
	}
	if !strings.HasPrefix(orch.files[acks], "{\n  \"0\": {\n    \"by\": \"coordinator\",") {
		t.Errorf("orch acks = %q, want an object", orch.files[acks])
	}
	js.files[acks] = orch.files[acks]
	compareOutcomes(t, js, orch)
}

// testSupervisorCrossRuntime acks one event with each runtime, then a second event with both
// over that file: the acks files must be identical.
func (h *harness) testSupervisorCrossRuntime(t *testing.T) {
	const state = `{"events":{"e1":{"status":"queued"},"e2":{"status":"queued"}}}`
	writers := map[string][]string{
		"js":   {h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")},
		"orch": {h.orchBin, "supervisor"},
	}
	for writer, argv := range writers {
		sb := h.newSandbox(t)
		seedSupervisorState(state)(t, sb)
		written := h.run(t, sb, append(append([]string{}, argv...), "ack", "--event", "e1", "--by", writer), nil, []string{noPostRunRefresh})
		acks, ok := written.files["project/supervisor-acks.json"]
		if written.exitCode != 0 || !ok {
			t.Fatalf("%s ack: exit %d, stderr %s", writer, written.exitCode, written.stderr)
		}
		t.Run(writer+"-first", func(t *testing.T) {
			h.comparePair(t, h.supervisorInvocation(supervisorCase{
				args: []string{"ack", "--event", "e2"},
				seed: seeds(seedSupervisorState(state), seedAcks(acks)),
			}))
		})
	}
}
