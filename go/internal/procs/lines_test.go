package procs

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// linesOf writes chunks to a Lines in order, closes it and returns the lines it passed on.
func linesOf(chunks [][]byte) []string {
	lines := []string{}
	l := &Lines{OnLine: func(line string) { lines = append(lines, line) }}
	for _, chunk := range chunks {
		if n, err := l.Write(chunk); n != len(chunk) || err != nil {
			panic("Lines.Write must accept every byte")
		}
	}
	_ = l.Close()
	return lines
}

// TestLinesMatchesReadline replays testdata/lines.json, written by gen-lines.mjs from Node's
// readline, chunk by chunk.
func TestLinesMatchesReadline(t *testing.T) {
	data, err := os.ReadFile("testdata/lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string
		Chunks []string
		Lines  []string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			chunks := make([][]byte, len(c.Chunks))
			for i, h := range c.Chunks {
				if chunks[i], err = hex.DecodeString(h); err != nil {
					t.Fatal(err)
				}
			}
			if got := linesOf(chunks); !slices.Equal(got, c.Lines) {
				t.Errorf("lines = %q, readline gives %q", got, c.Lines)
			}
		})
	}
}

func TestLinesSplitting(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"lf", []string{"a\nb"}, []string{"a", "b"}},
		{"crlf", []string{"a\r\nb"}, []string{"a", "b"}},
		{"lone cr", []string{"a\rb"}, []string{"a", "b"}},
		{"cr then lf across writes", []string{"a\r", "\nb"}, []string{"a", "b"}},
		{"two empty lines", []string{"\n\n"}, []string{"", ""}},
		{"final line unterminated", []string{"a\nlast"}, []string{"a", "last"}},
		{"no empty trailing line", []string{"a\n"}, []string{"a"}},
		{"empty stream", nil, []string{}},
		{"malformed utf-8", []string{"\xffa\xe2\x82\n"}, []string{"�a�"}},
		{"multibyte split across writes", []string{"\xe2\x82", "\xac\n"}, []string{"€"}},
		{"line separator", []string{"a\u2028b"}, []string{"a", "b"}},
		{"paragraph separator", []string{"a\u2029b"}, []string{"a", "b"}},
		{"line separator split across writes", []string{"a\xe2\x80", "\xa8b"}, []string{"a", "b"}},
		{"cr then line separator", []string{"a\r", "\u2028b"}, []string{"a", "", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := make([][]byte, len(tt.chunks))
			for i, c := range tt.chunks {
				chunks[i] = []byte(c)
			}
			if got := linesOf(chunks); !slices.Equal(got, tt.want) {
				t.Errorf("lines = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLinesHasNoLengthLimit feeds 1 MiB and 16 MiB lines in odd-sized chunks; bufio.Scanner
// would stop at 64 KiB.
func TestLinesHasNoLengthLimit(t *testing.T) {
	for _, size := range []int{1 << 20, 16 << 20} {
		long := strings.Repeat("é{x}", size/5) // multibyte characters straddle chunk edges
		stream := []byte(long + "\nshort\n" + long)
		var chunks [][]byte
		for chunk := range slices.Chunk(stream, 4093) {
			chunks = append(chunks, chunk)
		}
		got := linesOf(chunks)
		if len(got) != 3 || got[0] != long || got[1] != "short" || got[2] != long {
			t.Errorf("%d-byte lines: got %d lines, lengths %v", len(long), len(got), lengths(got))
		}
	}
}

func lengths(lines []string) []int {
	n := make([]int, len(lines))
	for i, line := range lines {
		n[i] = len(line)
	}
	return n
}

// TestLinesFeedsAChild checks the wiring the bridge uses: a Lines as a child's Stdout.
func TestLinesFeedsAChild(t *testing.T) {
	var lines []string
	out := &Lines{OnLine: func(line string) { lines = append(lines, line) }}
	child, err := Start(Spec{Name: "/bin/sh", Args: []string{"-c", `printf 'one\r\ntwo\nthree'`}, Stdout: out, Grace: testGrace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if want := []string{"one", "two", "three"}; !slices.Equal(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}
