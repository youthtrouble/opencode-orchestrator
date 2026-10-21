// Package client is orch remote's HTTP client for the Work Trains Worker's machine routes:
// push, pull and ack (plan D6, D10). It never follows a redirect: behind Cloudflare Access a
// redirect is the login page, which means the service token was refused.
//
// No error, log line or panic from this package carries a header value or a request body: the
// errors hold the route, status codes and the Worker's error code, never what was sent.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

// requestTimeout bounds one whole request, response body included.
const requestTimeout = 15 * time.Second

// maxResponseBytes bounds what a response may hold. The largest valid one, a pull of 50
// answers of 2000 characters each, is far smaller.
const maxResponseBytes = 4 << 20

// MachineHeader names this Mac on every request. The Worker ignores it while one Mac is the
// only machine (v1); it is there so a second Mac needs no client change.
const MachineHeader = "X-Work-Trains-Machine"

// route is one machine route: its method and its path relative to the configured URL.
type route struct{ method, path string }

func (r route) String() string { return r.method + " " + r.path }

var (
	pushRoute = route{http.MethodPost, "/api/v1/machine/push"}
	pullRoute = route{http.MethodGet, "/api/v1/machine/pull"}
	ackRoute  = route{http.MethodPost, "/api/v1/machine/ack"}
)

// accessHint is what to check when Access, not the Worker, answered.
const accessHint = "check the Access service token (orch remote service-token set) and the Access Service Auth policy"

// Credentials are the three Work Trains secrets a request carries.
type Credentials struct {
	MachineToken       string
	AccessClientID     string
	AccessClientSecret string
}

// SecretReader reads one secret from the Keychain; keychain.Read outside tests.
type SecretReader func(keychain.Account) (string, error)

// MissingSecretError is a Keychain item that is not there.
type MissingSecretError struct{ Account keychain.Account }

func (e *MissingSecretError) Error() string {
	setup := "orch remote service-token set"
	if e.Account == keychain.MachineToken {
		setup = "orch remote token rotate"
	}
	return fmt.Sprintf("no %s in the login Keychain; run %s", e.Account, setup)
}

// ReadCredentials reads the three secrets through read.
func ReadCredentials(read SecretReader) (Credentials, error) {
	var values [3]string
	for i, account := range []keychain.Account{keychain.MachineToken, keychain.AccessClientID, keychain.AccessClientSecret} {
		value, err := read(account)
		switch {
		case errors.Is(err, keychain.ErrNotFound):
			return Credentials{}, &MissingSecretError{Account: account}
		case err != nil:
			return Credentials{}, err
		}
		values[i] = value
	}
	return Credentials{MachineToken: values[0], AccessClientID: values[1], AccessClientSecret: values[2]}, nil
}

// AccessDeniedError is a response that came from Cloudflare Access rather than the Worker: a
// redirect (to the Access login) or a body that is not JSON.
type AccessDeniedError struct {
	Route  string
	Status int
}

func (e *AccessDeniedError) Error() string {
	return fmt.Sprintf("%s: access denied (HTTP %d, not a Worker response); %s", e.Route, e.Status, accessHint)
}

// HTTPStatusError is a non-2xx response from the Worker. ErrorCode is the Error body's error
// code, empty when the body was not one. The body's message is left out: it is free text.
type HTTPStatusError struct {
	Route     string
	Code      int
	ErrorCode string
}

func (e *HTTPStatusError) Error() string {
	if e.ErrorCode == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Route, e.Code)
	}
	return fmt.Sprintf("%s: HTTP %d %s", e.Route, e.Code, e.ErrorCode)
}

// ProtocolError is a 2xx response whose body breaks the protocol schema.
type ProtocolError struct {
	Route string
	Err   error
}

func (e *ProtocolError) Error() string { return fmt.Sprintf("%s: bad response: %v", e.Route, e.Err) }
func (e *ProtocolError) Unwrap() error { return e.Err }

// NetworkError is a request that got no response: refused, reset, timed out or cancelled.
type NetworkError struct {
	Route string
	Err   error
}

func (e *NetworkError) Error() string { return fmt.Sprintf("%s: network: %v", e.Route, e.Err) }
func (e *NetworkError) Unwrap() error { return e.Err }

// Client talks to one Worker as one machine.
type Client struct {
	baseURL     string
	credentials Credentials
	machineID   string
	userAgent   string
	http        *http.Client
}

// New is a client for the Worker at baseURL, which must have passed config validation (https,
// or plain http to a loopback host).
func New(baseURL string, credentials Credentials, machineID, orchVersion string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		credentials: credentials,
		machineID:   machineID,
		userAgent:   "orch/" + orchVersion,
		http: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Push sends changed project snapshots.
func (c *Client) Push(ctx context.Context, request wire.PushRequest) (wire.PushResponse, error) {
	var response wire.PushResponse
	if err := c.exchange(ctx, pushRoute, request, &response); err != nil {
		return response, err
	}
	return response, checkVersion(pushRoute, response.V)
}

// Pull fetches the answers and actions waiting to be applied.
func (c *Client) Pull(ctx context.Context) (wire.PullResponse, error) {
	var response wire.PullResponse
	if err := c.exchange(ctx, pullRoute, nil, &response); err != nil {
		return response, err
	}
	return response, checkVersion(pullRoute, response.V)
}

// Ack reports how each pulled answer and action was applied.
func (c *Client) Ack(ctx context.Context, request wire.AckRequest) (wire.AckResponse, error) {
	var response wire.AckResponse
	if err := c.exchange(ctx, ackRoute, request, &response); err != nil {
		return response, err
	}
	return response, checkVersion(ackRoute, response.V)
}

// exchange sends body (none when nil) on r and decodes a 2xx answer into response.
func (c *Client) exchange(ctx context.Context, r route, body, response any) error {
	request, err := c.newRequest(ctx, r, body)
	if err != nil {
		return err
	}
	reply, err := c.http.Do(request)
	if err != nil {
		return &NetworkError{Route: r.String(), Err: withoutURL(err)}
	}
	defer reply.Body.Close()
	if reply.StatusCode >= 300 && reply.StatusCode < 400 {
		return &AccessDeniedError{Route: r.String(), Status: reply.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes+1))
	if err != nil {
		return &NetworkError{Route: r.String(), Err: withoutURL(err)}
	}
	return decodeReply(r, reply, data, response)
}

func (c *Client) newRequest(ctx context.Context, r route, body any) (*http.Request, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, r.method, c.baseURL+r.path, bytes.NewReader(payload))
	if err != nil {
		return nil, withoutURL(err)
	}
	request.Header.Set("Authorization", "Bearer "+c.credentials.MachineToken)
	request.Header.Set("CF-Access-Client-Id", c.credentials.AccessClientID)
	request.Header.Set("CF-Access-Client-Secret", c.credentials.AccessClientSecret)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set(MachineHeader, c.machineID)
	if r == pushRoute {
		sum := sha256.Sum256(payload)
		request.Header.Set("Idempotency-Key", hex.EncodeToString(sum[:]))
	}
	return request, nil
}

// decodeReply maps a non-redirect response to the decoded body or a typed error. Every Worker
// response is JSON, so a 2xx or 4xx that is not came from Access (or a proxy in front of it).
func decodeReply(r route, reply *http.Response, data []byte, response any) error {
	if len(data) > maxResponseBytes {
		return &ProtocolError{Route: r.String(), Err: fmt.Errorf("response exceeds %d bytes", maxResponseBytes)}
	}
	success := reply.StatusCode >= 200 && reply.StatusCode < 300
	if !isJSON(reply) || !json.Valid(data) {
		if success || reply.StatusCode < 500 {
			return &AccessDeniedError{Route: r.String(), Status: reply.StatusCode}
		}
		return &HTTPStatusError{Route: r.String(), Code: reply.StatusCode}
	}
	if !success {
		return &HTTPStatusError{Route: r.String(), Code: reply.StatusCode, ErrorCode: errorCodeOf(data)}
	}
	if err := wire.Decode(bytes.NewReader(data), response); err != nil {
		return &ProtocolError{Route: r.String(), Err: err}
	}
	return nil
}

// checkVersion refuses a response for another protocol version; Decode checks only its shape.
func checkVersion(r route, version int) error {
	if version != wire.ProtocolVersion {
		return &ProtocolError{Route: r.String(), Err: fmt.Errorf("protocol version %d, want %d", version, wire.ProtocolVersion)}
	}
	return nil
}

func isJSON(reply *http.Response) bool {
	mediaType, _, err := mime.ParseMediaType(reply.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// errorCodeOf is the error code of an Error body, empty when data is not one.
func errorCodeOf(data []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	return body.Error
}

// withoutURL drops the URL net/http puts in its errors: the route already names the request,
// and the configured URL is the owner's Worker domain, which the log has no need to repeat.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
