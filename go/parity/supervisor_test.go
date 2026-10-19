//go:build parity

package parity

import (
	"path/filepath"
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
	return invocation{
		js:           append([]string{h.node, filepath.Join(h.orchJS, "bin", "supervisor.mjs")}, c.args...),
		orch:         append([]string{h.orchBin, "supervisor"}, c.args...),
		env:          []string{noPostRunRefresh},
		seed:         c.seed,
		crash:        c.crash,
		errorLine:    c.errorLine,
		projectPaths: true,
		normalize:    normalizeFlip(time.Now()),
	}
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

// testSupervisor compares `orch supervisor status|stop|ack` and its usage errors with
// bin/supervisor.mjs. serve and start (S8c) are not compared yet.
func (h *harness) testSupervisor(t *testing.T) {
	const cwdBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
	const orchestratorIsFile = "Error: EEXIST: file already exists, mkdir '$PROJECT/.orchestrator'"
	const stopIsDir = "Error: EISDIR: illegal operation on a directory, open '$PROJECT/.orchestrator/supervisor.stop'"
	const acksIsDir = "Error: EISDIR: illegal operation on a directory, rename '$PROJECT/.orchestrator/supervisor-acks.json.tmp' -> '$PROJECT/.orchestrator/supervisor-acks.json'"
	events := seedSupervisorState(`{"events":{"e1":{"status":"queued"},"e2":{"status":"queued"},"zero":0},"lanes":{}}`)
	ack := func(extra ...string) []string { return append([]string{"ack"}, extra...) }
	cases := []supervisorCase{
		{name: "usage/no-command"},
		{name: "usage/unknown-command", args: []string{"foo"}},
		{name: "usage/cwd-bare", args: []string{"--cwd"}, crash: crashC1, errorLine: cwdBoolean},
		{name: "usage/cwd-bare-with-command", args: []string{"status", "--cwd"}, crash: crashC1, errorLine: cwdBoolean},
		{name: "usage/cwd-relative", args: []string{"status", "--cwd", "sub"}, seed: seedProjectFile("sub/.orchestrator/supervisor-state.json", `{"status":"sub"}`)},

		{name: "status/no-orchestrator", args: []string{"status"}},
		{name: "status/empty-object", args: []string{"status"}, seed: seedSupervisorState(`{}`)},
		{name: "status/realistic", args: []string{"status"}, seed: seedSupervisorState(realisticSupervisorState)},
		{name: "status/null", args: []string{"status"}, seed: seedSupervisorState(`null`)},
		{name: "status/number", args: []string{"status"}, seed: seedSupervisorState(`5`)},
		{name: "status/string", args: []string{"status"}, seed: seedSupervisorState(`"x"`)},
		{name: "status/array", args: []string{"status"}, seed: seedSupervisorState(`[]`)},
		{name: "status/unparsable", args: []string{"status"}, seed: seedSupervisorState(`{"events":`)},
		{name: "status/bom", args: []string{"status"}, seed: seedSupervisorState("\xef\xbb\xbf{}")},
		{name: "status/directory", args: []string{"status"}, seed: seedProjectDir(".orchestrator/supervisor-state.json")},

		{name: "stop/fresh", args: []string{"stop"}},
		{name: "stop/existing", args: []string{"stop"}, seed: seedProjectFile(".orchestrator/supervisor.stop", "")},
		{name: "stop/orchestrator-is-file", args: []string{"stop"}, seed: seedProjectFile(".orchestrator", ""), crash: crashC1, errorLine: orchestratorIsFile},
		{name: "stop/stop-is-directory", args: []string{"stop"}, seed: seedProjectDir(".orchestrator/supervisor.stop"), crash: crashC1, errorLine: stopIsDir},

		{name: "ack/known", args: ack("--event", "e1"), seed: events},
		{name: "ack/unknown", args: ack("--event", "nope"), seed: events},
		{name: "ack/event-bare", args: ack("--event"), seed: events},
		{name: "ack/no-event", args: ack(), seed: events},
		{name: "ack/no-state", args: ack("--event", "e1")},
		{name: "ack/by", args: ack("--event", "e1", "--by", "X"), seed: events},
		{name: "ack/by-bare", args: ack("--event", "e1", "--by"), seed: events},
		{name: "ack/existing-same-id", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`{"e0":{"by":"a","at":"2026-01-01T00:00:00.000Z"},"e1":{"by":"old","at":"2026-01-02T00:00:00.000Z"},"e9":1}`))},
		{name: "ack/existing-other-id", args: ack("--event", "e2"), seed: seeds(events, seedAcks(`{"e1":{"by":"a","at":"2026-01-01T00:00:00.000Z"}}`))},
		{name: "ack/constructor", args: ack("--event", "constructor"), seed: seedSupervisorState(`{"events":{}}`)},
		{name: "ack/proto", args: ack("--event", "__proto__"), seed: seeds(seedSupervisorState(`{"events":{}}`), seedAcks(`{"a":1}`))},
		{name: "ack/proto-own-key", args: ack("--event", "__proto__"), seed: seeds(seedSupervisorState(`{"events":{}}`), seedAcks(`{"__proto__":{"x":1},"a":1}`))},
		{name: "ack/falsy-event", args: ack("--event", "zero"), seed: events},
		{name: "ack/state-null", args: ack("--event", "e1"), seed: seedSupervisorState(`null`), crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'events')"},
		{name: "ack/state-null-no-event", args: ack(), seed: seedSupervisorState(`null`)},
		{name: "ack/acks-null", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`null`)), crash: crashC1, errorLine: "TypeError: Cannot set properties of null (setting 'e1')"},
		{name: "ack/acks-number", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`5`)), crash: crashC1, errorLine: "TypeError: Cannot create property 'e1' on number '5'"},
		{name: "ack/acks-unparsable", args: ack("--event", "e1"), seed: seeds(events, seedAcks(`{`))},
		{name: "ack/acks-directory", args: ack("--event", "e1"), seed: seeds(events, seedProjectDir(".orchestrator/supervisor-acks.json")), crash: crashC1, errorLine: acksIsDir},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.supervisorInvocation(c)) })
	}
	t.Run("ack/PE-16-string-events", h.testSupervisorAckStringEvents)
	t.Run("ack/PE-16-array-acks", h.testSupervisorAckArrayAcks)
	t.Run("cross-runtime", h.testSupervisorCrossRuntime)
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
