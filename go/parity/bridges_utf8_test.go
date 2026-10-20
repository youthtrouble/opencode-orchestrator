//go:build parity

package parity

import (
	"strings"
	"testing"
)

// testBridgesSplitUTF8 compares each bridge when its CLI's stderr splits a character across two
// pipe reads (Sol S1/R7). Both runtimes decode the stream, so the character reaches the error,
// the run record and the incident whole; a stream that ends inside a character ends in one
// U+FFFD. The JS outcome is checked for that text before the pair is compared byte for byte, so
// the case cannot pass by both runtimes printing two U+FFFD.
func (h *harness) testBridgesSplitUTF8(t *testing.T) {
	cases := []struct {
		name string
		// head is written to stderr and read apart from stderr (see parity/fakes/claude).
		head, stderr string
		want         string
	}{
		{name: "split-character", head: "\xc3\n", stderr: "\xa9\n", want: "\u00e9"},
		{name: "ends-inside-character", head: "\xe2\x82\n", want: "\ufffd"},
	}
	for _, b := range []bridge{claudeBridge, codexBridge, opencodeBridge} {
		for _, c := range cases {
			t.Run(b.name+"/"+c.name, func(t *testing.T) {
				script := map[string]string{"stderr-head": c.head, "exit": "1\n"}
				if c.stderr != "" {
					script["stderr"] = c.stderr
				}
				js, orch := h.runPair(t, h.bridgeInvocation(t, b, bridgeCase{args: []string{"--prompt", "p"}, script: script}))
				tail := `exited 1: ` + c.want + `"`
				if b.name == opencodeBridge.name { // its error is the tail alone
					tail = `"` + c.want + `"`
				}
				observeDecoded(t, "stdout", js.stdout, tail)
				record := runRecordFile(t, js.files)
				observeDecoded(t, record, js.files[record], tail)
				observeDecoded(t, "state/incidents.jsonl", js.files["state/incidents.jsonl"], tail)
				compareOutcomes(t, js, orch)
			})
		}
	}
}

// observeDecoded fails unless the js output holds want and never a character decoded as two
// U+FFFD, the mark of a stream decoded chunk by chunk.
func observeDecoded(t *testing.T, where, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("js %s lacks %q:\n%s", where, want, got)
	}
	if strings.Contains(got, "\ufffd\ufffd") {
		t.Errorf("js %s decodes a character as two U+FFFD:\n%s", where, got)
	}
}
