// Package client is orch remote's HTTP client for the Work Trains Worker's machine routes:
// push, pull and ack (plan D6, D10). It never follows a redirect: behind Cloudflare Access a
// redirect is the login page, which means the service token was refused.
//
// No error, log line or panic from this package carries a header value, a request body or
// text from a response: the errors hold the route, status codes, an error code the protocol
// defines and fixed phrases. A response can echo what was sent (a proxy quoting a header, an
// Access page quoting the token), and the parsers quote what they refuse, so their messages
// are left out too.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"syscall"
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

// AcceptHeader on a pull tells the Worker that this orch answers an accept with the original
// recommendation from the project's mailbox (apply.ApplyAnswer), never with the pulled text,
// which is the copy redacted for the Worker. The Worker refuses an accept to a pull without it.
const (
	AcceptHeader      = "X-Work-Trains-Accept"
	AcceptFromMailbox = "mailbox"
)

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
// code when the protocol defines it, UnknownErrorCode when the body names another, and empty
// when the body has none. ExpectedIssuer is the Access team domain a wrong_issuer body says the
// Worker expects, kept only when it has exactly a team domain's shape; empty otherwise. The
// body's message is left out: it is free text.
type HTTPStatusError struct {
	Route          string
	Code           int
	ErrorCode      string
	ExpectedIssuer string
}

func (e *HTTPStatusError) Error() string {
	switch {
	case e.ErrorCode == "":
		return fmt.Sprintf("%s: HTTP %d", e.Route, e.Code)
	case e.ExpectedIssuer != "":
		return fmt.Sprintf("%s: HTTP %d %s (the Worker expects Access team %s)", e.Route, e.Code, e.ErrorCode, e.ExpectedIssuer)
	}
	return fmt.Sprintf("%s: HTTP %d %s", e.Route, e.Code, e.ErrorCode)
}

// ProtocolError is a 2xx response whose body breaks the protocol schema. Problem says how, in
// fixed words (see schemaProblem).
type ProtocolError struct {
	Route   string
	Problem string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("%s: bad response: %s", e.Route, e.Problem)
}

// NetworkError is a request that got no usable response: refused, reset, timed out, cancelled
// or answered with something that is not HTTP. Problem says which, in fixed words (see
// networkProblem).
type NetworkError struct {
	Route   string
	Problem string
}

func (e *NetworkError) Error() string { return fmt.Sprintf("%s: network: %s", e.Route, e.Problem) }

// Client talks to one Worker as one machine.
type Client struct {
	baseURL     string
	credentials Credentials
	machineID   string
	userAgent   string
	transport   http.RoundTripper
}

// New is a client for the Worker at baseURL, which must have passed config validation (https,
// or plain http to a loopback host; no query or fragment). baseURL may end in a slash or carry
// a path: every route goes under it.
func New(baseURL string, credentials Credentials, machineID, orchVersion string) *Client {
	return &Client{
		baseURL:     baseURL,
		credentials: credentials,
		machineID:   machineID,
		userAgent:   "orch/" + orchVersion,
		transport:   http.DefaultTransport,
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

// exchange sends body (none when nil) on r and decodes a 2xx answer into response. It sends
// through the transport rather than an http.Client: a Client parses a redirect's Location
// before any CheckRedirect can refuse it, and quotes a malformed one, user info included, in
// its error. The transport hands back every response as it came, so a redirect is never
// followed or parsed, only reported.
func (c *Client) exchange(ctx context.Context, r route, body, response any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := c.newRequest(ctx, r, body)
	if err != nil {
		return err
	}
	reply, err := c.transport.RoundTrip(request)
	if err != nil {
		return &NetworkError{Route: r.String(), Problem: networkProblem(err)}
	}
	defer reply.Body.Close()
	if reply.StatusCode >= 300 && reply.StatusCode < 400 {
		return &AccessDeniedError{Route: r.String(), Status: reply.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes+1))
	if err != nil {
		return &NetworkError{Route: r.String(), Problem: networkProblem(err)}
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
	endpoint, err := c.endpoint(r)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, r.method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, withoutURL(err)
	}
	request.Header.Set("Authorization", "Bearer "+c.credentials.MachineToken)
	request.Header.Set("CF-Access-Client-Id", c.credentials.AccessClientID)
	request.Header.Set("CF-Access-Client-Secret", c.credentials.AccessClientSecret)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set(MachineHeader, c.machineID)
	if r == pullRoute {
		request.Header.Set(AcceptHeader, AcceptFromMailbox)
	}
	if r == pushRoute {
		sum := sha256.Sum256(payload)
		request.Header.Set("Idempotency-Key", hex.EncodeToString(sum[:]))
	}
	return request, nil
}

// endpoint is r's URL: r's path joined to the base URL's path, so a trailing slash or a base
// path never doubles or drops a segment.
func (c *Client) endpoint(r route) (string, error) {
	endpoint, err := url.JoinPath(c.baseURL, r.path)
	return endpoint, withoutURL(err)
}

// decodeReply maps a non-redirect response to the decoded body or a typed error. Every Worker
// response is JSON, so a 2xx or 4xx that is not came from Access (or a proxy in front of it).
func decodeReply(r route, reply *http.Response, data []byte, response any) error {
	if len(data) > maxResponseBytes {
		return &ProtocolError{Route: r.String(), Problem: fmt.Sprintf("response exceeds %d bytes", maxResponseBytes)}
	}
	success := reply.StatusCode >= 200 && reply.StatusCode < 300
	if !isJSON(reply) || !json.Valid(data) {
		if success || reply.StatusCode < 500 {
			return &AccessDeniedError{Route: r.String(), Status: reply.StatusCode}
		}
		return &HTTPStatusError{Route: r.String(), Code: reply.StatusCode}
	}
	if !success {
		code, expectedIssuer := workerErrorOf(data)
		return &HTTPStatusError{Route: r.String(), Code: reply.StatusCode, ErrorCode: code, ExpectedIssuer: expectedIssuer}
	}
	if err := wire.Decode(bytes.NewReader(data), response); err != nil {
		return &ProtocolError{Route: r.String(), Problem: schemaProblem(err)}
	}
	return nil
}

// checkVersion refuses a response for another protocol version; Decode checks only its shape.
func checkVersion(r route, version int) error {
	if version != wire.ProtocolVersion {
		return &ProtocolError{Route: r.String(), Problem: fmt.Sprintf("protocol version %d, want %d", version, wire.ProtocolVersion)}
	}
	return nil
}

func isJSON(reply *http.Response) bool {
	mediaType, _, err := mime.ParseMediaType(reply.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// workerErrorCodes are the error codes of the protocol's Error body (the enum of
// $defs.Error.error in remote/protocol/v1/schema.json; a test keeps the two in step).
var workerErrorCodes = []string{
	"forbidden", "not_found", "invalid_request", "unsupported_version", "conflict",
	"too_late", "payload_too_large", "not_configured", "wrong_issuer", "internal",
}

// UnknownErrorCode stands in for an error code the protocol does not define: the response
// chose it, and it could be anything, an echoed secret included.
const UnknownErrorCode = "unknown"

// accessTeamDomain is the shape of an Error body's expected_issuer (its pattern in
// remote/protocol/v1/schema.json; a test keeps the two in step). A value of any other shape is
// dropped, so the field cannot carry text a response echoed.
var accessTeamDomain = regexp.MustCompile(`^https://[a-z0-9-]+\.cloudflareaccess\.com$`)

// workerErrorOf is the error code of an Error body: one the protocol defines, else
// UnknownErrorCode; empty when data has no error code. The expected issuer comes with it when
// the code is wrong_issuer and the value is a team domain.
func workerErrorOf(data []byte) (code, expectedIssuer string) {
	var body struct {
		Error *string `json:"error"`
		// Untyped, so a non-string value drops only this field, not the error code.
		ExpectedIssuer any `json:"expected_issuer"`
	}
	if json.Unmarshal(data, &body) != nil || body.Error == nil {
		return "", ""
	}
	if !slices.Contains(workerErrorCodes, *body.Error) {
		return UnknownErrorCode, ""
	}
	issuer, isString := body.ExpectedIssuer.(string)
	if *body.Error == "wrong_issuer" && isString && accessTeamDomain.MatchString(issuer) {
		return *body.Error, issuer
	}
	return *body.Error, ""
}

// schemaProblem names, in fixed words, why wire.Decode refused a body that is valid JSON. Only
// the error's type is read, since its text quotes the property name or value it refused. What
// encoding/json leaves untyped there is a field the struct does not declare.
func schemaProblem(err error) string {
	var (
		wrongType *json.UnmarshalTypeError
		shape     *wire.ShapeError
	)
	switch {
	case errors.As(err, &wrongType):
		return "a field of the wrong type"
	case errors.As(err, &shape):
		return "a field missing, null or not in the schema"
	}
	return "a field not in the schema"
}

// networkProblem names, in fixed words, why a request got no usable response. Only the
// error's type is read, since the transport's text can quote the response: a malformed status
// line or header line comes back verbatim.
func networkProblem(err error) string {
	var (
		timeout     interface{ Timeout() bool }
		dns         *net.DNSError
		op          *net.OpError
		certificate *tls.CertificateVerificationError
	)
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return "timed out"
	case errors.As(err, &dns):
		return "the host name did not resolve"
	case errors.As(err, &op) && op.Op == "dial":
		return "could not connect"
	case errors.As(err, &certificate):
		return "the TLS certificate was not trusted"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return "the connection closed before the response ended"
	}
	return "no valid HTTP response"
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
