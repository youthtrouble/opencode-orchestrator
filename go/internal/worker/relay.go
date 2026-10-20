package worker

import (
	"regexp"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// The JS reads the bridge's progress with these, once per relayed chunk: the first session or
// thread id in the chunk, and the leftmost `tool: <name>` or `ran: ` (a "ran: " first gives no
// tool name). Go's regexp is leftmost-first and its \w is ASCII, as in JS.
var (
	sessionLine  = regexp.MustCompile(`(thread|session) ([0-9a-f-]{36})`)
	progressLine = regexp.MustCompile(`tool: (\w+)|ran: `)
)

// relay decodes the bridge's stderr as a stream, as the JS's setEncoding("utf8") does with
// Node's StringDecoder: a character split across chunks is carried into the next chunk and
// relayed whole, and malformed bytes become U+FFFD.
type relay struct {
	tail []byte // the start of a character the last chunk ended in
}

// decode returns the text of chunk, carrying an incomplete last character to the next one.
func (d *relay) decode(chunk []byte) string {
	data := append(d.tail, chunk...)
	complete := len(data) - jsstr.UndecodedTail(data)
	d.tail = append([]byte(nil), data[complete:]...)
	return jsstr.DecodeUTF8(data[:complete])
}

// flush returns what the stream ended in: an incomplete character, as U+FFFD (Node's decoder
// emits it as one last chunk before the end of the stream), or "".
func (d *relay) flush() string {
	text := jsstr.DecodeUTF8(d.tail)
	d.tail = nil
	return text
}

// relayDecoded is the JS stderr listener, which Node calls only for a chunk that decodes to some
// text: a chunk holding just the start of a character is neither relayed nor counted as progress.
func (r *run) relayDecoded(text string) {
	if text != "" {
		r.relayChunk(text)
	}
}

// relayEnd relays the U+FFFD of a stream that ended inside a character, as the JS listener
// receives it before the bridge's close event.
func (r *run) relayEnd() {
	r.relayDecoded(r.relay.flush())
}

// relayChunk is the JS stderr listener for one decoded chunk: write it to the worker's stdout,
// publish the session id it names, then publish last_progress_at (and last_tool when the chunk
// names a tool), which every chunk does.
func (r *run) relayChunk(text string) {
	r.writeStdout(text)
	if m := sessionLine.FindStringSubmatch(text); m != nil {
		session := jsjson.NewObject()
		session.Set("session_id", m[2])
		r.state.Publish(session)
	}
	progress := jsjson.NewObject()
	progress.Set("last_progress_at", jsjson.ISOString(r.rt.Now()))
	if m := progressLine.FindStringSubmatch(text); m != nil && m[1] != "" {
		progress.Set("last_tool", m[1])
	}
	r.state.Publish(progress)
}
