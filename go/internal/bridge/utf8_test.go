package bridge

import (
	"io"
	"os"
	"testing"
	"time"
)

// The JS decodes the child's stderr as a stream, so a character split across two pipe reads is
// whole in the tail, and a stream ending inside a character ends in one U+FFFD.
func TestStderrTailDecodesACharacterSplitAcrossPipeReads(t *testing.T) {
	cases := []struct {
		name   string
		writes []string
		want   string
	}{
		{"split character", []string{"\xc3", "\xa9\n"}, "é"},
		{"ends inside a character", []string{"x\xe2", "\x82"}, "x�"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stderrTail(throughPipe(t, c.writes)); got != c.want {
				t.Errorf("stderrTail = %q, want %q", got, c.want)
			}
		})
	}
}

// throughPipe copies writes through a real pipe into a lockedBuffer, as exec copies a child's
// stderr, waiting after each write until it has been read so each one is its own read.
func throughPipe(t *testing.T, writes []string) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &lockedBuffer{}
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(stderr, r)
		copied <- err
	}()
	sent := 0
	for _, chunk := range writes {
		if _, err := w.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
		sent += len(chunk)
		for deadline := time.Now().Add(5 * time.Second); len(stderr.Bytes()) < sent; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("pipe read %d of %d bytes", len(stderr.Bytes()), sent)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-copied; err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return stderr.Bytes()
}
