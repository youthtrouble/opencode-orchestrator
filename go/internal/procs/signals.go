package procs

import (
	"os"
	"os/signal"
	"syscall"
)

// terminatingSignals are the signals that end orch when nothing handles them. While a RunSync
// child runs, they are relayed to its process group instead.
var terminatingSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// catchTerminatingSignals starts delivering the terminating signals to a new channel. One that
// orch ignores (a nohup'd orch's SIGHUP, the SIGINT of a background job) is left out, since
// Notify would stop ignoring it.
func catchTerminatingSignals() chan os.Signal {
	caught := make(chan os.Signal, len(terminatingSignals))
	for _, sig := range terminatingSignals {
		if !signal.Ignored(sig) {
			signal.Notify(caught, sig)
		}
	}
	return caught
}

// relaySignals passes each caught signal on to the child (Child.Signal, so the whole group of
// an OwnGroup child, and SIGKILL one Grace later) until release is called, which must be once
// the child has been reaped. release stops the catching and returns the first signal caught,
// nil when none was; one caught after the reap still counts, though it reaches no child.
func relaySignals(caught chan os.Signal, child *Child) (release func() os.Signal) {
	first := make(chan os.Signal, 1)
	go func() {
		var earliest os.Signal
		for sig := range caught {
			if earliest == nil {
				earliest = sig
			}
			child.Signal(sig.(syscall.Signal))
		}
		first <- earliest
	}()
	return func() os.Signal {
		stopCatching(caught)
		return <-first
	}
}

// firstCaught stops the catching of a run that never started a child and returns the first
// signal caught, nil when none was.
func firstCaught(caught chan os.Signal) os.Signal {
	stopCatching(caught)
	return <-caught // nil once the closed channel is empty
}

// stopCatching ends the delivery to caught and closes it, so what it still buffers can be read
// to the end. Closing is safe: once Stop returns, nothing is delivered to it.
func stopCatching(caught chan os.Signal) {
	signal.Stop(caught)
	close(caught)
}

// raise sends sig, when there is one, to orch itself. With no other Notify for it in the
// process, the Go runtime then ends orch by that signal before raise returns, as node ends on
// a Ctrl-C; a handler registered elsewhere receives it instead, and orch goes on.
func raise(sig os.Signal) {
	if sig != nil {
		signalThisThread(sig.(syscall.Signal))
	}
}
