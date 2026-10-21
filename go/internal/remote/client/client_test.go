package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

// The fake secrets are not token-shaped on purpose: a redactor would not hide them, so only
// never writing them can keep them out of an error.
var credentials = Credentials{
	MachineToken:       "fake-machine-token-7f3a",
	AccessClientID:     "fake-client-id.access",
	AccessClientSecret: "fake-client-secret-91bc",
}

const machineID = "00000000-0000-4000-8000-000000000001"

// example is the value of a protocol example, the Worker's side of the contract.
func example(t *testing.T, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "remote", "protocol", "v1", "examples", "valid", name))
	if err != nil {
		t.Fatal(err)
	}
	var wrapped struct{ Value json.RawMessage }
	if err := json.Unmarshal(data, &wrapped); err != nil {
		t.Fatal(err)
	}
	return wrapped.Value
}

// recorder is a server that answers every request with handler and keeps each request's
// headers and body.
type recorder struct {
	mu       sync.Mutex
	headers  []http.Header
	bodies   [][]byte
	requests []string
}

func serve(t *testing.T, handler http.HandlerFunc) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.bodies = append(rec.bodies, body)
		rec.requests = append(rec.requests, r.Method+" "+r.URL.Path)
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return New(server.URL+"/", credentials, machineID, "1.2.3"), rec
}

func jsonReply(status int, body json.RawMessage) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		w.Write(body)
	}
}

// noSecrets fails when err's text holds a secret.
func noSecrets(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{credentials.MachineToken, credentials.AccessClientID, credentials.AccessClientSecret} {
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("error %q holds a secret", err)
		}
	}
}

func TestEveryRequestCarriesTheCredentialsAndPushAnIdempotencyKey(t *testing.T) {
	responses := map[string]json.RawMessage{
		"/api/v1/machine/push": example(t, "push-response.json"),
		"/api/v1/machine/pull": example(t, "pull-response.json"),
		"/api/v1/machine/ack":  example(t, "ack-response.json"),
	}
	c, rec := serve(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(200, responses[r.URL.Path])(w, r) })
	ctx := context.Background()
	push := wire.PushRequest{V: 1, Machine: wire.Machine{ID: machineID, OrchVersion: "1.2.3", SentAt: "2026-10-21T09:00:00.000Z"}, Projects: []wire.ProjectSnapshot{}}
	pushed, err := c.Push(ctx, push)
	if err != nil || len(pushed.Projects) != 2 || pushed.Projects[0].Result != "deferred" {
		t.Fatalf("push = %+v, %v", pushed, err)
	}
	pulled, err := c.Pull(ctx)
	if err != nil || len(pulled.Answers) != 1 || pulled.PollAfterSeconds != 30 {
		t.Fatalf("pull = %+v, %v", pulled, err)
	}
	acked, err := c.Ack(ctx, wire.AckRequest{V: 1, Results: []wire.AckResult{{ID: machineID, Type: "answer", Outcome: "applied", Detail: ""}}})
	if err != nil || acked.Settled != 2 {
		t.Fatalf("ack = %+v, %v", acked, err)
	}

	if want := []string{"POST /api/v1/machine/push", "GET /api/v1/machine/pull", "POST /api/v1/machine/ack"}; strings.Join(rec.requests, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %q", rec.requests)
	}
	for i, header := range rec.headers {
		for name, want := range map[string]string{
			"Authorization":           "Bearer " + credentials.MachineToken,
			"Cf-Access-Client-Id":     credentials.AccessClientID,
			"Cf-Access-Client-Secret": credentials.AccessClientSecret,
			"Content-Type":            "application/json",
			"User-Agent":              "orch/1.2.3",
			"X-Work-Trains-Machine":   machineID,
		} {
			if got := header.Get(name); got != want {
				t.Errorf("request %d: %s = %q, want %q", i, name, got, want)
			}
		}
	}
	sum := sha256.Sum256(rec.bodies[0])
	if got := rec.headers[0].Get("Idempotency-Key"); got != hex.EncodeToString(sum[:]) {
		t.Errorf("push Idempotency-Key = %q, want the body's sha256", got)
	}
	if rec.headers[1].Get("Idempotency-Key") != "" || rec.headers[2].Get("Idempotency-Key") != "" {
		t.Error("only push carries an Idempotency-Key")
	}
	var sent wire.PushRequest
	if err := wire.Decode(strings.NewReader(string(rec.bodies[0])), &sent); err != nil || sent.Machine.ID != machineID {
		t.Errorf("push body %s: %v", rec.bodies[0], err)
	}
}

func TestARedirectIsAccessDeniedAndNotFollowed(t *testing.T) {
	c, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cdn-cgi/access/login" {
			jsonReply(200, example(t, "pull-response.json"))(w, r)
			return
		}
		http.Redirect(w, r, "/cdn-cgi/access/login", http.StatusFound)
	})
	_, err := c.Pull(context.Background())
	var denied *AccessDeniedError
	if !errors.As(err, &denied) || denied.Status != http.StatusFound {
		t.Fatalf("err = %v, want AccessDeniedError 302", err)
	}
	if !strings.Contains(err.Error(), "orch remote service-token set") || !strings.Contains(err.Error(), "Access Service Auth policy") {
		t.Errorf("err %q lacks the hint", err)
	}
	if len(rec.requests) != 1 {
		t.Errorf("requests = %q, want the redirect not followed", rec.requests)
	}
}

func TestANonJSONAnswerIsAccessDeniedBelow500AndAStatusErrorAbove(t *testing.T) {
	for _, tc := range []struct {
		status      int
		contentType string
		body        string
		denied      bool
	}{
		{200, "text/html", "<html>Sign in</html>", true},
		{403, "text/html", "<html>Forbidden</html>", true},
		{200, "application/json", "<html>not json</html>", true},
		{502, "text/html", "<html>Bad gateway</html>", false},
	} {
		c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", tc.contentType)
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		_, err := c.Pull(context.Background())
		var denied *AccessDeniedError
		var status *HTTPStatusError
		switch {
		case tc.denied && !errors.As(err, &denied):
			t.Errorf("%d %s: err = %v, want AccessDeniedError", tc.status, tc.contentType, err)
		case !tc.denied && (!errors.As(err, &status) || status.Code != tc.status || status.ErrorCode != ""):
			t.Errorf("%d %s: err = %v, want HTTPStatusError", tc.status, tc.contentType, err)
		}
	}
}

func TestAWorkerErrorIsAStatusErrorWithItsCodeAndWithoutItsMessage(t *testing.T) {
	echo := `{"error":"invalid_request","message":"bad header Bearer ` + credentials.MachineToken + `"}`
	c, _ := serve(t, jsonReply(400, json.RawMessage(echo)))
	_, err := c.Ack(context.Background(), wire.AckRequest{V: 1, Results: []wire.AckResult{}})
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.Code != 400 || status.ErrorCode != "invalid_request" {
		t.Fatalf("err = %v, want HTTPStatusError 400 invalid_request", err)
	}
	if err.Error() != "POST /api/v1/machine/ack: HTTP 400 invalid_request" {
		t.Errorf("err = %q", err)
	}
	noSecrets(t, err)

	c, _ = serve(t, jsonReply(503, example(t, "error-not-found.json")))
	if _, err := c.Pull(context.Background()); !errors.As(err, &status) || status.Code != 503 || status.ErrorCode != "not_found" {
		t.Errorf("err = %v, want HTTPStatusError 503 not_found", err)
	}
}

func TestAPullWithAnUnknownFieldIsAProtocolError(t *testing.T) {
	var pull map[string]any
	if err := json.Unmarshal(example(t, "pull-response.json"), &pull); err != nil {
		t.Fatal(err)
	}
	pull["answers"].([]any)[0].(map[string]any)["extra"] = true
	body, _ := json.Marshal(pull)
	c, _ := serve(t, jsonReply(200, body))
	_, err := c.Pull(context.Background())
	var protocol *ProtocolError
	if !errors.As(err, &protocol) {
		t.Fatalf("err = %v, want ProtocolError", err)
	}
	if !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Errorf("err = %v, want the unknown field named", err)
	}
}

func TestAResponseForAnotherVersionIsAProtocolError(t *testing.T) {
	c, _ := serve(t, jsonReply(200, json.RawMessage(`{"v":2,"settled":0}`)))
	_, err := c.Ack(context.Background(), wire.AckRequest{V: 1, Results: []wire.AckResult{}})
	var protocol *ProtocolError
	if !errors.As(err, &protocol) || !strings.Contains(err.Error(), "protocol version 2") {
		t.Fatalf("err = %v, want ProtocolError for version 2", err)
	}
}

func TestAClosedConnectionIsANetworkErrorWithoutTheURL(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	_, err := c.Pull(context.Background())
	var network *NetworkError
	if !errors.As(err, &network) {
		t.Fatalf("err = %v, want NetworkError", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("err %q names the URL", err)
	}
	noSecrets(t, err)
}

func TestACancelledContextIsANetworkErrorWrappingTheCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	})
	_, err := c.Pull(ctx)
	var network *NetworkError
	if !errors.As(err, &network) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want NetworkError wrapping context.Canceled", err)
	}
}

func TestReadCredentialsNamesTheMissingItemAndHowToSetIt(t *testing.T) {
	store := map[keychain.Account]string{keychain.MachineToken: "t", keychain.AccessClientID: "i"}
	read := func(account keychain.Account) (string, error) {
		value, ok := store[account]
		if !ok {
			return "", keychain.ErrNotFound
		}
		return value, nil
	}
	_, err := ReadCredentials(read)
	var missing *MissingSecretError
	if !errors.As(err, &missing) || missing.Account != keychain.AccessClientSecret || !strings.Contains(err.Error(), "orch remote service-token set") {
		t.Fatalf("err = %v, want the missing client secret", err)
	}
	store[keychain.AccessClientSecret] = "s"
	got, err := ReadCredentials(read)
	if err != nil || got != (Credentials{MachineToken: "t", AccessClientID: "i", AccessClientSecret: "s"}) {
		t.Errorf("credentials = %+v, %v", got, err)
	}
	delete(store, keychain.MachineToken)
	if _, err := ReadCredentials(read); !strings.Contains(err.Error(), "orch remote token rotate") {
		t.Errorf("err = %v, want the token rotate hint", err)
	}
	locked := errors.New("locked")
	if _, err := ReadCredentials(func(keychain.Account) (string, error) { return "", locked }); !errors.Is(err, locked) {
		t.Errorf("err = %v, want the Keychain's error", err)
	}
}
