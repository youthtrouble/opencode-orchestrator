package bridge

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// childGrace bounds the wait before SIGKILL after orch signals the child, and the wait for the
// child's pipes to close after it exits (PE-10).
const childGrace = 10 * time.Second

// eventHandler is a bridge's side of a child run: the body of its readline "line" listener and
// of its "close" listener. Both run with the jsThread's lock held.
type eventHandler interface {
	// handleEvent applies one stdout line. The returned error is an exception the JS would die
	// of inside its line handler.
	handleEvent(line string) error
	// handleClose is the "close" listener, for a child that closed with exitCode (a number, or
	// nil for a child killed by a signal) after duration.
	handleClose(exitCode any, stderr []byte, duration time.Duration)
}

// jsThread stands for the single JS thread a bridge's handlers run on. Line events, the stderr
// copy, the timer and signal handlers run on different goroutines in Go; mu serializes them,
// so the log order is the order of events, as in single-threaded JS.
type jsThread struct {
	mu      sync.Mutex
	log     *logger
	handler eventHandler
	// crash is the exception that would have killed the JS inside its line handler; once set,
	// later events and log lines are dropped, as the JS process is gone.
	crash   error
	crashed chan struct{} // closed when crash is set
	closed  bool          // the child has closed; the timeout no longer fires
	// timedOut is set once the bridge's own --timeout-min cap has stopped the child (PE-45).
	timedOut bool
}

func newJSThread(log *logger, handler eventHandler) *jsThread {
	return &jsThread{log: log, handler: handler, crashed: make(chan struct{})}
}

// handleLine is the readline "line" listener.
func (t *jsThread) handleLine(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.crash != nil {
		return
	}
	if err := t.handler.handleEvent(line); err != nil {
		t.crash = err
		close(t.crashed)
	}
}

// stopAtTimeout is the timeout firing: it reports whether the child is still running in the
// JS's view (it has not closed and the JS has not crashed; the JS clears its timer on close)
// and, if so, marks the run timed out and writes line as a progress line; an empty line writes
// nothing.
func (t *jsThread) stopAtTimeout(line string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.crash != nil {
		return false
	}
	t.timedOut = true
	if line != "" {
		t.log.printf("%s", line)
	}
	return true
}

// stoppedAtTimeout reports whether the bridge's own cap stopped the child.
func (t *jsThread) stoppedAtTimeout() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timedOut
}

// close is the child's "close" event. It returns the crash the JS died of earlier, or the
// error that kept the child's status or output from being collected; otherwise it runs the
// bridge's close handler.
func (t *jsThread) close(code *int, waitErr error, stderr []byte, duration time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.crash != nil {
		return t.crash
	}
	if waitErr != nil {
		return waitErr
	}
	exitCode := any(nil)
	if code != nil {
		exitCode = float64(*code)
	}
	t.handler.handleClose(exitCode, stderr, duration)
	return nil
}

// childRun is how a bridge spawns its CLI.
type childRun struct {
	name  string
	args  []string
	dir   string
	env   []string  // nil: the environment is inherited whole
	stdin io.Reader // nil: /dev/null
	// timeout is the setTimeout delay after which the child is sent stopSignal, once;
	// timeoutLog is the progress line logged first ("" logs nothing).
	timeout    time.Duration
	timeoutLog string
	// stopSignal is how orch stops the child: at the timeout, after a crash, and in place of a
	// SIGTERM it forwards. The zero value is SIGTERM; opencode is stopped with SIGINT, because
	// SIGTERM leaves its service session running (PE-65).
	stopSignal syscall.Signal
	started    time.Time // the JS `started`, from which duration_ms is measured
}

// stopWith is the signal that stops the child.
func (c childRun) stopWith() syscall.Signal {
	if c.stopSignal == 0 {
		return syscall.SIGTERM
	}
	return c.stopSignal
}

// run runs the child to completion: it forwards SIGTERM (as stopSignal) and SIGINT to the child
// (PE-11, PE-65), sends stopSignal once the timeout passes, and on exit runs the close handler.
// An error is a crash: an argv element Node's spawn rejects, a spawn failure, or a TypeError the
// JS would have died of in its line handler (the child is then sent stopSignal).
//
// The child runs in a process group of its own, so each of those signals, the SIGKILL after
// them and the final kill once the child has exited reach the tools it started (PE-22). The JS
// leaves the CLI in its own group instead; here a signal to the bridge's group (Ctrl-C, worker
// stop) reaches the CLI only through the forwarding, once.
func (c childRun) run(thread *jsThread) error {
	if err := checkNullBytes(c.args); err != nil {
		return err
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	lines := &procs.Lines{OnLine: thread.handleLine}
	stderr := &lockedBuffer{}
	child, err := procs.Start(procs.Spec{
		Name:     c.name,
		Args:     c.args,
		Dir:      c.dir,
		Env:      c.env,
		Stdin:    c.stdin,
		Stdout:   lines,
		Stderr:   stderr,
		Grace:    childGrace,
		OwnGroup: true,
	})
	if err != nil {
		return &jsError{class: "Error", message: err.Error()}
	}
	closed := make(chan struct{})
	go forwardSignals(child, c.stopWith(), signals, thread.crashed, closed)
	timer := time.AfterFunc(c.timeout, func() {
		if thread.stopAtTimeout(c.timeoutLog) {
			child.Signal(c.stopWith())
		}
	})
	code, waitErr := child.Wait()
	// The child has exited: no timeout may fire from here on, so none can log after the last
	// line (the JS clears the timer on close). One that fired before Wait returned may still be
	// logging; in the JS it would have fired before close too.
	timer.Stop()
	lines.Close()
	close(closed)
	return thread.close(code, waitErr, stderr.Bytes(), time.Since(c.started))
}

// forwardSignals relays the signals orch receives to the child, a SIGTERM as stop, and stops the
// child with stop once the JS would have crashed, until the child has closed.
func forwardSignals(child *procs.Child, stop syscall.Signal, signals <-chan os.Signal, crashed <-chan struct{}, closed <-chan struct{}) {
	for {
		select {
		case sig := <-signals:
			if sig == syscall.SIGTERM {
				sig = stop
			}
			child.Signal(sig.(syscall.Signal))
		case <-crashed:
			child.Signal(stop)
			crashed = nil
		case <-closed:
			return
		}
	}
}

// checkNullBytes is the check Node's spawn makes of each argument (not counting the command)
// before it starts anything: one holding a NUL is a TypeError [ERR_INVALID_ARG_VALUE]. Only a
// prompt read from a file or stdin can hold one.
func checkNullBytes(args []string) error {
	for i, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return &jsError{
				class:   "TypeError [ERR_INVALID_ARG_VALUE]",
				message: fmt.Sprintf("The argument 'args[%d]' must be a string without null bytes. Received %s", i, inspectArgValue(arg)),
			}
		}
	}
	return nil
}

// inspectArgValue is how Node's ERR_INVALID_ARG_VALUE shows a string: util.inspect of it, cut
// to 128 UTF-16 units plus "..." when longer. util.inspect splits a string longer than 76 units
// after each "\n" into quoted pieces joined by " +\n  ".
func inspectArgValue(s string) string {
	pieces := []string{s}
	if jsstr.Len(s) > 76 {
		pieces = strings.SplitAfter(s, "\n")
		if pieces[len(pieces)-1] == "" {
			pieces = pieces[:len(pieces)-1]
		}
	}
	quoted := make([]string, len(pieces))
	for i, piece := range pieces {
		quoted[i] = quoteString(piece)
	}
	inspected := strings.Join(quoted, " +\n  ")
	if jsstr.Len(inspected) > 128 {
		return jsstr.Slice(inspected, 0, 128) + "..."
	}
	return inspected
}

// quoteString is util.inspect's strEscape: single quotes, unless the string holds one and can
// do without escaping it in double quotes or backticks; C0 and C1 controls, DEL, the backslash
// and the chosen quote are escaped.
func quoteString(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') {
		switch {
		case !strings.ContainsRune(s, '"'):
			quote = '"'
		case !strings.ContainsRune(s, '`') && !strings.Contains(s, "${"):
			quote = '`'
		}
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}
