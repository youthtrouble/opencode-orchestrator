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

// relay decodes the bridge's stderr chunk by chunk. A character split across chunks is carried
// into the next chunk and relayed whole (Node's StringDecoder rule), where the JS decodes each
// chunk on its own and prints U+FFFD for each half. Chunk boundaries are up to the pipe in both
// runtimes, so orch takes the deterministic reading; malformed bytes become U+FFFD as in Node.
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

// flush returns what the stream ended in: an incomplete character, as U+FFFD (the JS printed it
// as part of its last chunk), or "".
func (d *relay) flush() string {
	text := jsstr.DecodeUTF8(d.tail)
	d.tail = nil
	return text
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
