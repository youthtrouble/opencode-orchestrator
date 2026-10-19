package procs

import (
	"bytes"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// Lines is an io.WriteCloser that splits what is written to it into lines as Node's readline
// does and passes each one to OnLine:
//
//   - "\r\n", "\n" and a lone "\r" end a line; a "\r" that ends one Write and a "\n" that
//     starts the next are a single break. (readline only joins them when the "\n" arrives
//     within crlfDelay, 100 ms; Lines always does.)
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

	pending []byte // the current line so far
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
	for {
		end := bytes.IndexAny(p, "\r\n")
		if end < 0 {
			l.pending = append(l.pending, p...)
			return written, nil
		}
		l.emit(p[:end])
		if p[end] == '\r' {
			if end+1 == len(p) {
				l.afterCR = true
			} else if p[end+1] == '\n' {
				end++
			}
		}
		p = p[end+1:]
	}
}

// Close passes the final line when the stream did not end with a line break.
func (l *Lines) Close() error {
	l.pending = l.pending[:len(l.pending)-undecodedTail(l.pending)]
	if len(l.pending) > 0 {
		l.emit(nil)
	}
	return nil
}

func (l *Lines) emit(tail []byte) {
	l.pending = append(l.pending, tail...)
	l.OnLine(jsstr.DecodeUTF8(l.pending))
	l.pending = l.pending[:0]
}

// undecodedTail is how many bytes at the end of data Node's StringDecoder holds back as the
// start of a character still to be completed (src/string_decoder.cc): a lead byte with fewer
// continuation bytes than it announces, within the last four bytes. The continuation bytes
// are not validated, and 0xF5-0xF7 count as four-byte leads, as in Node.
func undecodedTail(data []byte) int {
	if len(data) == 0 || data[len(data)-1]&0x80 == 0 {
		return 0
	}
	for n := 1; n <= len(data) && n <= 4; n++ {
		b := data[len(data)-n]
		if b&0xC0 == 0x80 {
			continue
		}
		var length int
		switch {
		case b&0xE0 == 0xC0:
			length = 2
		case b&0xF0 == 0xE0:
			length = 3
		case b&0xF8 == 0xF0:
			length = 4
		default:
			return 0
		}
		if n >= length {
			return 0
		}
		return n
	}
	return 0
}
