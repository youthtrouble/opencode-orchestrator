package procs

import (
	"bytes"
	"sync"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// SyncSpec is a spawnSync call: Name resolved on PATH as Start resolves it, run with Args in
// Dir (the working directory when "") with the parent's environment and /dev/null as stdin.
// A positive Timeout is spawnSync's timeout option. Grace (positive) bounds the wait before
// SIGKILL once the timeout's SIGTERM was sent and the wait for pipes still held by
// grandchildren once the child has exited; spawnSync waits forever for both (PE-10 ext.).
type SyncSpec struct {
	Name    string
	Args    []string
	Dir     string
	Timeout time.Duration
	Grace   time.Duration
}

// SyncResult is what spawnSync returns. Status is the exit status, nil when the child was
// killed by a signal or could not be started (JS null). Stdout and Stderr are decoded as
// UTF-8 ("" when the child could not be started, where spawnSync gives undefined). TimedOut
// is spawnSync's ETIMEDOUT error: the timeout fired, whatever the child then did (a child
// that handles SIGTERM keeps its exit status, as in Node). Overflowed is spawnSync's ENOBUFS
// error: stdout and stderr together passed NodeMaxBuffer, so the child was sent SIGTERM and
// only the first NodeMaxBuffer bytes were kept (PE-20). Neither sets Err, since the callers
// read spawnSync's error only for ETIMEDOUT. Err is the *StartError of a child that could not
// be started, or a failure to collect its status.
type SyncResult struct {
	Status         *int
	Stdout, Stderr string
	TimedOut       bool
	Overflowed     bool
	Err            error
}

// RunSync runs the child to completion as spawnSync does, but in a process group of its own,
// so a timeout or an overflow stops the child's descendants with it (PE-22). Descendants in a
// session of their own (a detached worker runner) are not in that group and keep running.
func RunSync(s SyncSpec) SyncResult {
	limit := NewOutputLimit(NodeMaxBuffer)
	stdout, stderr := limit.Stream(), limit.Stream()
	child, err := Start(Spec{Name: s.Name, Args: s.Args, Dir: s.Dir, Stdout: stdout, Stderr: stderr, Grace: s.Grace, OwnGroup: true})
	if err != nil {
		return SyncResult{Err: err}
	}
	timeout := startTimeout(child, s.Timeout)
	stopWatching := terminateOnOverflow(child, limit)
	status, err := child.Wait()
	stopWatching()
	return SyncResult{
		Status:     status,
		Stdout:     jsstr.DecodeUTF8(stdout.Bytes()),
		Stderr:     jsstr.DecodeUTF8(stderr.Bytes()),
		TimedOut:   timeout.stop(),
		Overflowed: limit.Overflowed(),
		Err:        err,
	}
}

// terminateOnOverflow sends the child SIGTERM once its output passes the limit, as spawnSync
// does on ENOBUFS; Child.Signal then arms the PE-10 SIGKILL, so a child that ignores SIGTERM
// ends one Grace later (Node closes the pipes instead, and such a child dies of EPIPE or
// SIGPIPE once it writes again: PE-20). stop must be called once the child has been reaped.
func terminateOnOverflow(child *Child, limit *OutputLimit) (stop func()) {
	reaped, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-limit.Exceeded():
			child.Signal(syscall.SIGTERM)
		case <-reaped:
		}
	}()
	return func() {
		close(reaped)
		<-finished
	}
}

// NodeMaxBuffer is Node's default maxBuffer for spawnSync and execFileSync, 1 MiB. Node 26
// counts it over stdout and stderr together: a probe overflowed on 600,000 bytes of each.
const NodeMaxBuffer = 1 << 20

// OutputLimit is one maxBuffer shared by every Stream it hands out (PE-20). The streams keep
// their bytes until they have kept the limit between them; later bytes are discarded, the
// writes still succeeding so that the child's pipes keep draining, and Exceeded is closed.
// Node keeps every whole read made before it notices instead (1,114,112 bytes in a probe).
type OutputLimit struct {
	mu       sync.Mutex
	room     int
	exceeded chan struct{}
}

// NewOutputLimit returns a limit of limit bytes over all of its streams.
func NewOutputLimit(limit int) *OutputLimit {
	return &OutputLimit{room: limit, exceeded: make(chan struct{})}
}

// Stream returns a new writer that captures one stream under the limit.
func (l *OutputLimit) Stream() *LimitedStream {
	return &LimitedStream{limit: l}
}

// Exceeded is closed once the streams have been given more bytes than the limit.
func (l *OutputLimit) Exceeded() <-chan struct{} {
	return l.exceeded
}

// Overflowed reports whether Exceeded has been closed.
func (l *OutputLimit) Overflowed() bool {
	select {
	case <-l.exceeded:
		return true
	default:
		return false
	}
}

// admit reserves room for up to n bytes and returns how many may be kept, closing Exceeded the
// first time some must be discarded.
func (l *OutputLimit) admit(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := min(n, l.room)
	l.room -= kept
	if kept < n && !l.Overflowed() {
		close(l.exceeded)
	}
	return kept
}

// LimitedStream is one stream captured under an OutputLimit.
type LimitedStream struct {
	limit *OutputLimit
	kept  bytes.Buffer
}

// Write keeps what the limit admits and discards the rest; it never fails.
func (s *LimitedStream) Write(p []byte) (int, error) {
	s.kept.Write(p[:s.limit.admit(len(p))])
	return len(p), nil
}

// Bytes is what the stream kept. It must be read only once the writes have ended.
func (s *LimitedStream) Bytes() []byte {
	return s.kept.Bytes()
}

// timeout sends SIGTERM to the child once its deadline passes (Child.Signal then arms the
// SIGKILL escalation).
type timeout struct {
	timer *time.Timer
	done  chan struct{} // closed once the deadline's function has run
	fired bool          // the deadline's SIGTERM reached the child; read after done
}

func startTimeout(child *Child, after time.Duration) *timeout {
	t := &timeout{done: make(chan struct{})}
	if after > 0 {
		t.timer = time.AfterFunc(after, func() {
			defer close(t.done)
			t.fired = child.Signal(syscall.SIGTERM)
		})
	}
	return t
}

// stop disarms the deadline and reports whether it signalled the child. A deadline that
// passes after the child has been reaped sends nothing and is no timeout; one that is running
// when stop is called is waited for, so its outcome is known.
func (t *timeout) stop() bool {
	if t.timer == nil || t.timer.Stop() {
		return false
	}
	<-t.done
	return t.fired
}
