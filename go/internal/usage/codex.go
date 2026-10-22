package usage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const codexAppServerTimeout = 20 * time.Second

// appServerKillGrace is how long a child may take to exit after SIGTERM before it is killed.
// The JS only sends SIGTERM; the escalation keeps a child that ignores it from outliving orch.
const appServerKillGrace = time.Second

// errCodexTimeout is the JS rejection of every request pending when the overall timeout
// fires. A child that exits without answering also ends here, after the full timeout, as in
// the JS (which keeps waiting for the answer).
var errCodexTimeout = errors.New("codex app-server timeout")

// SpawnError reports that the codex binary could not be started. The JS has no 'error'
// listener on the child, so there this crashes the whole process instead of failing the
// feed; orch mirrors that by failing the command rather than recording {ok:false}.
type SpawnError struct {
	Bin string
	Err error
}

func (e *SpawnError) Error() string {
	return fmt.Sprintf("spawn %s: %v", e.Bin, e.Err)
}

func (e *SpawnError) Unwrap() error { return e.Err }

// CodexAccount is what readCodexAccountAndLimits returns: the raw results of account/read
// and account/rateLimits/read.
type CodexAccount struct {
	Account any
	Limits  any
}

// ReadCodexAccountAndLimits mirrors readCodexAccountAndLimits over `<bin> app-server`: one
// JSON-RPC session (initialize, initialized, account/read, account/rateLimits/read) bounded
// by an overall timeout; the child is stopped and reaped before it returns (see close).
func ReadCodexAccountAndLimits(bin string, timeout time.Duration) (CodexAccount, error) {
	return readCodexAccountAndLimits(context.Background(), bin, timeout)
}

// readCodexAccountAndLimits is ReadCodexAccountAndLimits whose session also ends, exactly as
// on timeout, when parent is done. Tests use that to end a session once its child is ready.
func readCodexAccountAndLimits(parent context.Context, bin string, timeout time.Duration) (CodexAccount, error) {
	session, err := startAppServer(parent, bin, timeout)
	if err != nil {
		return CodexAccount{}, err
	}
	defer session.close()

	initialize := jsjson.NewObject()
	clientInfo := jsjson.NewObject()
	clientInfo.Set("name", "opencode-orchestrator")
	clientInfo.Set("title", "opencode orchestrator")
	clientInfo.Set("version", "0.1.0")
	capabilities := jsjson.NewObject()
	capabilities.Set("experimentalApi", false)
	initialize.Set("clientInfo", clientInfo)
	initialize.Set("capabilities", capabilities)
	if _, err := session.request("initialize", initialize); err != nil {
		return CodexAccount{}, err
	}
	if err := session.notify("initialized", jsjson.NewObject()); err != nil {
		return CodexAccount{}, err
	}

	account, err := session.request("account/read", jsjson.NewObject())
	if err != nil {
		return CodexAccount{}, err
	}
	limitsParams := jsjson.NewObject()
	limitsParams.Set("excludeResetCreditDetails", true)
	limits, err := session.request("account/rateLimits/read", limitsParams)
	if err != nil {
		return CodexAccount{}, err
	}
	return CodexAccount{Account: account, Limits: limits}, nil
}

// appServer is one running `codex app-server` child and its JSON-RPC state.
type appServer struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	messages <-chan *jsjson.Object
	deadline context.Context
	cancel   context.CancelFunc
	nextID   float64
}

// startAppServer spawns the child; the session deadline is timeout after the spawn (or
// earlier, when parent is done).
func startAppServer(parent context.Context, bin string, timeout time.Duration) (*appServer, error) {
	cmd := exec.Command(bin, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, &SpawnError{Bin: bin, Err: err}
	}
	deadline, cancel := context.WithTimeout(parent, timeout)
	return &appServer{cmd: cmd, stdin: stdin, messages: readMessages(deadline, stdout), deadline: deadline, cancel: cancel}, nil
}

// readMessages delivers every stdout line that parses as a JSON object; other lines are
// skipped, like the JS line handler's JSON.parse catch. It stops when the session ends
// (ctx is done) or stdout closes.
func readMessages(ctx context.Context, stdout io.Reader) <-chan *jsjson.Object {
	messages := make(chan *jsjson.Object)
	go func() {
		defer close(messages)
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if msg, parseErr := jsjson.Unmarshal([]byte(strings.TrimRight(line, "\r\n"))); parseErr == nil {
				if obj, isObject := msg.(*jsjson.Object); isObject {
					select {
					case messages <- obj:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return messages
}

// request sends a JSON-RPC request and waits for the answer with its id: the result, or an
// error carrying the RPC error's message (the method name when it has none).
func (s *appServer) request(method string, params *jsjson.Object) (any, error) {
	s.nextID++
	id := s.nextID
	if err := s.send(method, id, params); err != nil {
		return nil, err
	}
	for {
		select {
		case <-s.deadline.Done():
			return nil, errCodexTimeout
		case msg, open := <-s.messages:
			if !open {
				// stdout closed without an answer: the JS waits for the timeout.
				<-s.deadline.Done()
				return nil, errCodexTimeout
			}
			if msgID, _ := jsjson.Path(msg, "id").(float64); msgID != id {
				continue
			}
			if rpcErr := jsjson.Path(msg, "error"); jsjson.Truthy(rpcErr) {
				return nil, errors.New(jsjson.OrText(jsjson.Path(rpcErr, "message"), method))
			}
			return jsjson.Path(msg, "result"), nil
		}
	}
}

func (s *appServer) notify(method string, params *jsjson.Object) error {
	return s.send(method, 0, params)
}

// send writes one JSON-RPC message line; id 0 makes it a notification. A write error (the
// child already gone) is not reported: the pending answer then times out, as in the JS.
func (s *appServer) send(method string, id float64, params *jsjson.Object) error {
	msg := jsjson.NewObject()
	msg.Set("jsonrpc", "2.0")
	if id != 0 {
		msg.Set("id", id)
	}
	msg.Set("method", method)
	msg.Set("params", params)
	line, err := jsjson.Marshal(msg)
	if err != nil {
		return err
	}
	_, _ = s.stdin.Write(append(line, '\n'))
	return nil
}

// close ends the session: it closes stdin, sends SIGTERM like proc.kill(), kills the child if
// it has not exited after appServerKillGrace, and reaps it. Reaping closes stdout, which ends
// the reader even when a grandchild still holds the pipe open.
func (s *appServer) close() {
	s.cancel()
	_ = s.stdin.Close()
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(appServerKillGrace):
		_ = s.cmd.Process.Kill()
		<-exited
	}
}

// NormalizeCodex mirrors normalizeCodex: plan, the 5-hour and weekly windows (epoch seconds
// turned into ISO strings), and the rate-limit flags of an account/rateLimits/read answer.
func NormalizeCodex(c CodexAccount) *jsjson.Object {
	limits := c.Limits
	rl := firstTruthy(jsjson.Path(limits, "rateLimits"), jsjson.Path(limits, "rateLimitsByLimitId", "codex"))
	out := jsjson.NewObject()
	out.Set("ok", true)
	out.Set("plan", orNull(firstTruthy(jsjson.Path(c.Account, "account", "planType"), jsjson.Path(rl, "planType"))))
	windows := codexWindows(rl)
	out.Set("five_hour", windows[0])
	out.Set("weekly", windows[1])
	out.Set("rate_limit_reached", orNull(jsjson.Path(rl, "rateLimitReachedType")))
	out.Set("ordinary_usage_allowed", jsjson.Path(limits, "ordinaryUsageAllowed"))
	out.Set("reset_credits_available", nullish(jsjson.Path(limits, "rateLimitResetCredits", "availableCount"), 0.0))
	out.Set("credits", orNull(jsjson.Path(rl, "credits")))
	return out
}

func codexWindows(rl any) [2]*jsjson.Object {
	candidates := [2]*jsjson.Object{}
	slots := [2]int{0, 1}
	labeled := false
	for i, key := range []string{"primary", "secondary"} {
		candidates[i], _ = jsjson.Path(rl, key).(*jsjson.Object)
		if duration, ok := jsjson.Path(candidates[i], "windowDurationMins").(float64); ok {
			labeled = true
			slots[i] = 0
			if duration >= 1440 {
				slots[i] = 1
			}
		}
	}
	// Ambiguous durations retain the legacy positional mapping.
	if candidates[0] != nil && candidates[1] != nil && slots[0] == slots[1] {
		slots = [2]int{0, 1}
	}
	mapped := [2]*jsjson.Object{}
	for i, candidate := range candidates {
		if candidate != nil {
			mapped[slots[i]] = candidate
		}
	}
	var windows [2]*jsjson.Object
	for i, candidate := range mapped {
		windows[i] = codexWindow(candidate)
		if candidate == nil && labeled {
			windows[i].Set("applicable", false)
		}
	}
	return windows
}

func codexWindow(w any) *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("pct", jsjson.Path(w, "usedPercent"))
	out.Set("resets_at", isoFromEpochSeconds(jsjson.Path(w, "resetsAt")))
	out.Set("window_mins", jsjson.Path(w, "windowDurationMins"))
	return out
}

// isoFromEpochSeconds is `typeof s === "number" ? new Date(s * 1000).toISOString() : null`.
func isoFromEpochSeconds(s any) any {
	seconds, isNumber := s.(float64)
	if !isNumber {
		return nil
	}
	// new Date(ms) drops the sub-millisecond part toward zero.
	return jsjson.ISOString(time.UnixMilli(int64(seconds * 1000)))
}

// firstTruthy is `a || b`.
func firstTruthy(a, b any) any {
	if jsjson.Truthy(a) {
		return a
	}
	return b
}
