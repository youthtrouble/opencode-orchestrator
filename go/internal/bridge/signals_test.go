package bridge

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// The fake is the test binary (fakeSignalAck). Each acknowledgement precedes the next signal: no
// sleeps, polling or coalesced signals can make a dropped second forward look successful. Only the
// owned fake is signalled.
func TestForwardedSignalsDoNotClaimBridgeStop(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode"} {
		for _, userSignal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(harness+"/"+userSignal.String(), func(t *testing.T) {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				input, control, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				defer control.Close()
				output := make(chan string, 8)
				child, err := procs.Start(procs.Spec{Name: executable, Env: append(os.Environ(), fakeSignalAckEnv+"=1"), Stdin: input, Stdout: &procs.Lines{OnLine: func(line string) { output <- line }}, Grace: childGrace, OwnGroup: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { child.Signal(syscall.SIGKILL) })
				expect := func(step, want string) {
					t.Helper()
					select {
					case got := <-output:
						if got != want {
							t.Fatalf("got %q, want %q", got, want)
						}
					case <-time.After(3 * time.Second):
						t.Fatalf("no %s acknowledgement (%s)", want, step)
					}
				}
				expect("ready", "ready")
				stop := syscall.SIGTERM
				if harness == "opencode" {
					stop = syscall.SIGINT
				}
				thread := newJSThread(&logger{w: &bytes.Buffer{}}, nil)
				signals := make(chan os.Signal)
				closed, forwarded := make(chan struct{}), make(chan struct{})
				go func() { defer close(forwarded); forwardSignals(child, stop, signals, thread, closed) }()
				defer func() { close(closed); <-forwarded }()
				want := "INT"
				if userSignal == syscall.SIGTERM && stop == syscall.SIGTERM {
					want = "TERM"
				}
				for i := 0; i < 2; i++ {
					signals <- userSignal
					expect(fmt.Sprintf("forward %d", i+1), want)
				}
				// Prior user signals must not suppress the cap or claim its incident cause.
				if !thread.stopAtTimeout("") {
					t.Fatal("forward suppressed cap")
				}
				thread.mu.Lock()
				thread.signalLocked(child, "cap", stop)
				thread.mu.Unlock()
				capWant := "TERM"
				if stop == syscall.SIGINT {
					capWant = "INT"
				}
				expect("cap", capWant)
				// A cap must not swallow an explicit later user/supervisor signal either.
				signals <- userSignal
				expect("forward after cap", want)
				if !thread.stoppedAtTimeout() {
					t.Fatal("forward replaced cap cause")
				}
				if _, err := control.WriteString("exit\n"); err != nil {
					t.Fatal(err)
				}
				code, err := child.Wait()
				if err != nil || code == nil || *code != 0 {
					t.Fatalf("fake wait %v, %v", code, err)
				}
			})
		}
	}
}

// fakeSignalAckEnv makes the test binary play fakeSignalAck, the child of
// TestForwardedSignalsDoNotClaimBridgeStop.
const fakeSignalAckEnv = "ORCH_BRIDGE_TEST_FAKE_SIGNAL_ACK"

// fakeSignalAckFailed is the fake's exit when its stdin ends or fails before `exit`.
const fakeSignalAckFailed = 3

// fakeSignalAck catches SIGINT and SIGTERM before it prints `ready`, and the Go runtime queues a
// caught signal whatever the main goroutine is doing, so each signal that arrives at any instant
// after `ready` is acknowledged on stdout as "INT" or "TERM". A shell fake cannot promise that: a
// trapped signal that lands just before the shell blocks in read(2) waits for a read that never
// returns. The line `exit` on stdin exits 0.
func fakeSignalAck() int {
	// The test sends a signal only after the previous one is acknowledged, so at most one is ever
	// pending; a buffer of 2 leaves room to spare and no signal is dropped.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	commands := make(chan string)
	var readErr error
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			commands <- scanner.Text()
		}
		readErr = scanner.Err()
		close(commands)
	}()
	if _, err := fmt.Println("ready"); err != nil {
		return fakeSignalAckFailure(err)
	}
	for {
		select {
		case received := <-signals:
			acknowledgement := "INT"
			if received == syscall.SIGTERM {
				acknowledgement = "TERM"
			}
			if _, err := fmt.Println(acknowledgement); err != nil {
				return fakeSignalAckFailure(err)
			}
		case command, open := <-commands:
			switch {
			case !open && readErr != nil:
				return fakeSignalAckFailure(readErr)
			case !open:
				return fakeSignalAckFailure(errors.New("stdin closed before exit"))
			case command == "exit":
				return 0
			}
		}
	}
}

func fakeSignalAckFailure(err error) int {
	fmt.Fprintln(os.Stderr, "fake signal ack:", err)
	return fakeSignalAckFailed
}
