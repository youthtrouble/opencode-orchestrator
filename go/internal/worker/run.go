package worker

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// bridgeGrace bounds the wait for the bridge's pipes once it has exited (PE-10). The worker
// never kills its bridge: the bridge bounds its own CLI, and killing it would lose its run
// record and orphan the CLI.
const bridgeGrace = 10 * time.Second

// run is one foreground worker from its first publish to its final line. The bridge's stdout
// and stderr copies, the signal handler and the main goroutine all change the state, publish
// it and write stdout; mu serializes them as the single JS thread does, so a relayed chunk and
// its publishes are one event, and the final line is printed after the last of them.
type run struct {
	launch *Launch
	rt     Runtime

	mu     sync.Mutex
	state  *State
	child  *procs.Child // nil until the bridge has started
	output []byte       // the bridge's stdout
	relay  relay
	closed bool // the bridge has closed: no more stdout, stderr or signal events (see settle)
}

// handleSignals is the JS SIGINT and SIGTERM handlers, installed for the rest of the process.
func (r *run) handleSignals(signals <-chan os.Signal) {
	for sig := range signals {
		r.interrupt(sig.(syscall.Signal))
	}
}

// interrupt is one signal: publish "interrupted", then pass the signal on to the bridge's
// process group (or the bridge alone). Foreground handles signals only once the bridge has
// started (see runBridge). The worker keeps waiting for the bridge to close, and the branch it
// then takes overwrites the status.
func (r *run) interrupt(sig syscall.Signal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.finish("interrupted", jsjson.NewObject())
	if r.child != nil {
		r.child.SignalGroup(sig)
	}
}

// finish is the JS finish(status, extra): publish {status, ...extra}.
func (r *run) finish(status string, extra *jsjson.Object) {
	changes := jsjson.NewObject()
	changes.Set("status", status)
	for _, key := range extra.Keys() {
		value, _ := extra.Get(key)
		changes.Set(key, value)
	}
	r.state.Publish(changes)
}

// runBridge is the JS try block: start the bridge in a session of its own with the prompt on
// its stdin, publish "running", wait for it to close and take the branch its result calls for.
// It returns the close code (a number, or nil for a bridge killed by a signal).
//
// The lock is held from the start to the publish, so no relayed chunk precedes "running". An
// EPIPE on the bridge's stdin (a bridge that exits without reading its prompt) is ignored
// (PE-13), where the JS dies of it.
//
// signals is handled only once the bridge is installed: the JS handlers always see the child,
// since its spawn is synchronous, so a signal captured earlier waits in the channel and is
// handled after "running", reaching the bridge's group. If the start fails nothing handles
// them, as the JS dies of its spawn error.
func (r *run) runBridge(prompt string, signals <-chan os.Signal) (any, error) {
	r.mu.Lock()
	child, err := procs.Start(procs.Spec{
		Name:       r.rt.Self,
		Args:       r.launch.bridgeArgs(),
		Dir:        r.launch.project.Root,
		Stdin:      strings.NewReader(prompt),
		Stdout:     bridgeStdout{r},
		Stderr:     bridgeStderr{r},
		Grace:      bridgeGrace,
		NewSession: true,
	})
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	r.child = child
	go r.handleSignals(signals) // blocks on r.mu until "running" is published
	running := jsjson.NewObject()
	running.Set("status", "running")
	running.Set("worker_pid", float64(child.Pid()))
	r.state.Publish(running)
	r.mu.Unlock()

	return r.settle(child.Wait())
}

// settle is the JS from the bridge's close event to process.exit: relay what the stderr stream
// ended in and take the branch the result calls for. It runs under the lock and marks the run
// closed, so neither a late pipe write nor a signal changes the state or the output after it.
//
// In Node that whole stretch is synchronous: the close listener resolves the awaited promise,
// the continuation runs as a microtask before control returns to libuv, and it ends in
// process.exit (or an uncaught exception). A SIGINT or SIGTERM arriving then is only queued by
// libuv's signal watcher; its process.on handler would run on a later loop turn that never
// comes. A signal that lands before the close event is dispatched runs its handler first, and
// the branch then overwrites "interrupted", as it does here when interrupt takes the lock
// first.
func (r *run) settle(code *int, waitErr error) (any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.relayEnd()
	if waitErr != nil {
		return nil, waitErr
	}
	var exitCode any
	if code != nil {
		exitCode = float64(*code)
	}
	return exitCode, r.closeWith(exitCode, parseResult(jsstr.DecodeUTF8(r.output)))
}

// report prints the final line {lane, status, session_id, exit_code} and returns the exit
// code.
func (r *run) report(code any) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status, _ := r.state.Fields.Get("status")
	sessionID, _ := r.state.Fields.Get("session_id")
	line := jsjson.NewObject()
	line.Set("lane", r.launch.lane)
	line.Set("status", status)
	line.Set("session_id", sessionID)
	line.Set("exit_code", code)
	data, err := jsjson.Marshal(line)
	if err != nil {
		return 1, err
	}
	r.writeStdout(string(data) + "\n")
	if status == "finished" || status == "deferred" {
		return 0, nil
	}
	return 1, nil
}

// writeStdout writes the worker's own stdout. Like process.stdout.write, it does not report a
// failed write (a closed stdout kills the process with SIGPIPE, as it does Node).
func (r *run) writeStdout(text string) {
	if text != "" {
		fmt.Fprint(r.rt.Stdout, text)
	}
}

// bridgeStdout is the bridge's stdout listener: `output += chunk`. The JS decodes the stream
// (setEncoding("utf8")), so the bytes are kept here and decoded whole when the run settles.
type bridgeStdout struct{ r *run }

func (w bridgeStdout) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if !w.r.closed {
		w.r.output = append(w.r.output, p...)
	}
	return len(p), nil
}

// bridgeStderr is the bridge's stderr listener: each chunk is decoded and relayed (see relay).
type bridgeStderr struct{ r *run }

func (w bridgeStderr) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if !w.r.closed {
		w.r.relayDecoded(w.r.relay.decode(p))
	}
	return len(p), nil
}
