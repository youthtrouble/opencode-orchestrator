package worker

import (
	"errors"
	"testing"
)

// Node calls the JS stderr listener only for a chunk that decodes to some text, and once more
// with U+FFFD when the stream ends inside a character. The end goes through settle, as the
// bridge's close does; a wait error keeps settle from taking a close branch.
func TestRelayDecodesTheStderrStream(t *testing.T) {
	for _, tc := range []struct {
		name         string
		chunks       []string
		want         string
		wantProgress string // the clock reads :01, :02, ... once per relayed chunk
	}{
		{"a split character is relayed once, whole", []string{"\xc3", "\xa9\n"}, "é\n", "2026-10-19T12:00:01.000Z"},
		{"an incomplete end is relayed as one U+FFFD", []string{"end\xe2", "\x82"}, "end�", "2026-10-19T12:00:02.000Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out, _ := relayRun(t)
			relayChunks(r, tc.chunks...)
			bridgeGone := errors.New("bridge gone")
			if _, err := r.settle(nil, bridgeGone); err != bridgeGone {
				t.Fatalf("settle = %v, want the wait error", err)
			}
			if out.String() != tc.want {
				t.Errorf("relayed %q, want %q", out.String(), tc.want)
			}
			if got, _ := r.state.Fields.Get("last_progress_at"); got != tc.wantProgress {
				t.Errorf("last_progress_at = %v, want %s", got, tc.wantProgress)
			}
		})
	}
}
