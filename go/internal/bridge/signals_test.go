package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

// Each trap acknowledgement precedes the next signal: no sleeps, polling or coalesced
// signals can make a dropped second forward look successful. Only the owned fake is signalled.
func TestForwardedSignalsDoNotClaimBridgeStop(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode"} {
		for _, userSignal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(harness+"/"+userSignal.String(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "fake")
				write(t, path, `#!/bin/bash
trap 'echo INT' INT
trap 'echo TERM' TERM
echo ready
while :; do
  read -r command || continue
  if [ "$command" = exit ]; then exit 0; fi
done
`)
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
				input, control, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				defer control.Close()
				output := make(chan string, 8)
				child, err := procs.Start(procs.Spec{Name: path, Stdin: input, Stdout: &procs.Lines{OnLine: func(line string) { output <- line }}, Grace: childGrace, OwnGroup: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { child.Signal(syscall.SIGKILL) })
				expect := func(want string) {
					t.Helper()
					select {
					case got := <-output:
						if got != want {
							t.Fatalf("got %q, want %q", got, want)
						}
					case <-time.After(3 * time.Second):
						t.Fatalf("no %s acknowledgement", want)
					}
				}
				expect("ready")
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
					expect(want)
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
				expect(capWant)
				// A cap must not swallow an explicit later user/supervisor signal either.
				signals <- userSignal
				expect(want)
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
