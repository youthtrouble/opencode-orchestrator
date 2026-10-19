package procs

import "github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"

// Lines is an io.WriteCloser that splits what is written to it into lines as Node's readline
// does and passes each one to OnLine:
//
//   - "\r\n", "\n", a lone "\r", U+2028 and U+2029 end a line (readline's lineEnding,
//     /\r?\n|\r(?!\n)|\u2028|\u2029/). A "\r" that ends one Write and a "\n" that starts the
//     next are a single break. (readline only joins them when the "\n" arrives within
//     crlfDelay, 100 ms; Lines always does.) A U+2028 or U+2029 split across writes still
//     ends the line, because readline splits decoded text.
//   - Close passes the final unterminated line, if it is not empty. A character cut off by
//     the end of the stream is dropped, because readline never flushes its StringDecoder.
//   - Each line is decoded as Buffer.toString does (jsstr.DecodeUTF8): malformed UTF-8
//     becomes U+FFFD, and a character split across writes is joined first.
//   - Lines may be of any length.
//
// OnLine runs on the writing goroutine, which for a Spec writer is exec's copy goroutine: it
// must not block for long, or the child stalls on a full pipe. Lines is not safe for
// concurrent writes.
type Lines struct {
	OnLine func(string)

	pending []byte // the bytes after the last line break
	afterCR bool   // the last Write ended in "\r", so a leading "\n" is part of that break
}

func (l *Lines) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) == 0 {
		return 0, nil
	}
	if l.afterCR {
		l.afterCR = false
		if p[0] == '\n' {
			p = p[1:]
		}
	}
	// Only the bytes the previous Write could not decode yet, the start of a U+2028 or U+2029
	// among them, can begin a break that p completes.
	scanned := len(l.pending) - jsstr.UndecodedTail(l.pending)
	l.pending = append(l.pending, p...)
	lineStart := 0
	for {
		at, size := lineBreak(l.pending[scanned:])
		if at < 0 {
			break
		}
		l.OnLine(jsstr.DecodeUTF8(l.pending[lineStart : scanned+at]))
		scanned += at + size
		lineStart = scanned
	}
	if lineStart > 0 {
		l.afterCR = lineStart == len(l.pending) && l.pending[lineStart-1] == '\r'
		l.pending = append(l.pending[:0], l.pending[lineStart:]...)
	}
	return written, nil
}

// lineBreak returns the offset and byte length of the first line break in b, or -1. A "\r"
// that ends b is a break on its own; Write joins it with a "\n" that starts the next Write.
func lineBreak(b []byte) (at, size int) {
	for i, c := range b {
		switch {
		case c == '\n':
			return i, 1
		case c == '\r':
			if i+1 < len(b) && b[i+1] == '\n' {
				return i, 2
			}
			return i, 1
		case c == 0xE2 && isLineOrParagraphSeparator(b[i:]):
			return i, 3
		}
	}
	return -1, 0
}

// isLineOrParagraphSeparator reports whether b starts with U+2028 or U+2029 in UTF-8. Their
// lead byte 0xE2 cannot be a continuation byte, so a match is always a whole character.
func isLineOrParagraphSeparator(b []byte) bool {
	return len(b) >= 3 && b[1] == 0x80 && (b[2] == 0xA8 || b[2] == 0xA9)
}

// Close passes the final line when the stream did not end with a line break.
func (l *Lines) Close() error {
	l.pending = l.pending[:len(l.pending)-jsstr.UndecodedTail(l.pending)]
	if len(l.pending) > 0 {
		l.OnLine(jsstr.DecodeUTF8(l.pending))
		l.pending = l.pending[:0]
	}
	return nil
}
