package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/client"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

// The fake secrets are not token-shaped on purpose: a redactor would not hide them, so only
// never writing them keeps them out of logs, errors and the state file.
var fakeCredentials = client.Credentials{
	MachineToken:       "fake-machine-token-7f3a",
	AccessClientID:     "fake-client-id.access",
	AccessClientSecret: "fake-client-secret-91bc",
}

// fakeWorker implements the machine routes of remote/protocol/v1 over an in-process server:
// pull serves what has not been acked, ack settles it, push stores the latest hash per key.
// It refuses a request without the fake credentials, as Access and the Worker would.
type fakeWorker struct {
	t      *testing.T
	server *httptest.Server
	clock  *fakeClock

	mu        sync.Mutex
	answers   []wire.PulledAnswer
	actions   []wire.PulledAction
	hashes    map[string]string
	pushes    []timedPush
	acks      []wire.AckRequest // every ack received, dropped ones included
	down      bool              // close every connection unanswered
	dropAcks  int               // read, then drop unanswered, this many acks
	deferKey  string            // defer this key once, for deferFor seconds
	deferFor  int
	pullReply http.HandlerFunc // when set, answers every pull in place of the queue
}

type timedPush struct {
	at      time.Time
	request wire.PushRequest
}

func newFakeWorker(t *testing.T, clock *fakeClock) *fakeWorker {
	w := &fakeWorker{t: t, clock: clock, hashes: map[string]string{}}
	w.server = httptest.NewServer(http.HandlerFunc(w.serveHTTP))
	t.Cleanup(w.server.Close)
	return w
}

func (w *fakeWorker) serveHTTP(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.t.Errorf("read request: %v", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.down {
		closeConnection(rw)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeCredentials.MachineToken ||
		r.Header.Get("CF-Access-Client-Id") != fakeCredentials.AccessClientID ||
		r.Header.Get("CF-Access-Client-Secret") != fakeCredentials.AccessClientSecret {
		reply(rw, http.StatusForbidden, map[string]string{"error": "forbidden", "message": ""})
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/machine/pull":
		w.pull(rw, r)
	case "POST /api/v1/machine/ack":
		w.ack(rw, body)
	case "POST /api/v1/machine/push":
		w.push(rw, body)
	default:
		reply(rw, http.StatusNotFound, map[string]string{"error": "not_found", "message": ""})
	}
}

func (w *fakeWorker) pull(rw http.ResponseWriter, r *http.Request) {
	if w.pullReply != nil {
		w.pullReply(rw, r)
		return
	}
	reply(rw, http.StatusOK, wire.PullResponse{
		V: 1, ServerTime: w.clock.iso(), PollAfterSeconds: 30,
		Answers: append([]wire.PulledAnswer{}, w.answers...),
		Actions: append([]wire.PulledAction{}, w.actions...),
	})
}

func (w *fakeWorker) ack(rw http.ResponseWriter, body []byte) {
	var request wire.AckRequest
	if err := wire.Decode(bytes.NewReader(body), &request); err != nil || len(request.Results) == 0 {
		w.t.Errorf("ack body %s: %v", body, err)
		reply(rw, http.StatusBadRequest, map[string]string{"error": "invalid_request", "message": ""})
		return
	}
	w.acks = append(w.acks, request)
	if w.dropAcks > 0 {
		w.dropAcks--
		closeConnection(rw)
		return
	}
	settled := 0
	for _, result := range request.Results {
		before := len(w.answers) + len(w.actions)
		w.answers = slices.DeleteFunc(w.answers, func(a wire.PulledAnswer) bool { return a.ID == result.ID })
		w.actions = slices.DeleteFunc(w.actions, func(a wire.PulledAction) bool { return a.ID == result.ID })
		settled += before - len(w.answers) - len(w.actions)
	}
	reply(rw, http.StatusOK, wire.AckResponse{V: 1, Settled: settled})
}

func (w *fakeWorker) push(rw http.ResponseWriter, body []byte) {
	var request wire.PushRequest
	if err := wire.Decode(bytes.NewReader(body), &request); err != nil || len(request.Projects) > 50 {
		w.t.Errorf("push body: %v (%d projects)", err, len(request.Projects))
		reply(rw, http.StatusBadRequest, map[string]string{"error": "invalid_request", "message": ""})
		return
	}
	w.pushes = append(w.pushes, timedPush{at: w.clock.Now(), request: request})
	results := []wire.PushResult{}
	for _, project := range request.Projects {
		result := wire.PushResult{Key: project.Key, Result: "accepted"}
		switch {
		case project.Key == w.deferKey:
			w.deferKey = ""
			result.Result, result.RetryAfterSeconds = "deferred", &w.deferFor
		case w.hashes[project.Key] == project.SnapshotHash:
			result.Result = "unchanged"
		default:
			w.hashes[project.Key] = project.SnapshotHash
		}
		results = append(results, result)
	}
	reply(rw, http.StatusOK, wire.PushResponse{V: 1, ServerTime: w.clock.iso(), Projects: results})
}

func reply(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(body)
}

// closeConnection drops the connection without an answer, as a network failure would.
func closeConnection(rw http.ResponseWriter) {
	conn, _, err := rw.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

func (w *fakeWorker) queueAnswer(answer wire.PulledAnswer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answers = append(w.answers, answer)
}

func (w *fakeWorker) queueAction(action wire.PulledAction) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.actions = append(w.actions, action)
}

func (w *fakeWorker) setDown(down bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.down = down
}

func (w *fakeWorker) pushLog() []timedPush {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.pushes)
}

func (w *fakeWorker) ackLog() []wire.AckRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.acks)
}

func (w *fakeWorker) pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.answers) + len(w.actions)
}

// fakeClock is the loop's clock: only Sleep moves it, and each Sleep is recorded.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	onSleep func(n int) // runs after the nth sleep (1-based) has moved the clock
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) iso() string { return c.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	n, hook := len(c.sleeps), c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return ctx.Err()
}

func (c *fakeClock) sleepLog() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sleeps)
}
