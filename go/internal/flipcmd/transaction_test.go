package flipcmd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

func TestUnreadableFlipFailsClosedAndWritersLeaveBytes(t *testing.T) {
	for _, kind := range []string{"corrupt", "BOM", "mode-0000", "directory"} {
		t.Run(kind, func(t *testing.T) {
			p := newTestProject(t)
			content, code := `{"to":`, "INVALID_JSON"
			switch kind {
			case "BOM":
				content = "\xef\xbb\xbf{}"
			case "mode-0000":
				content, code = `{}`, "EACCES"
			case "directory":
				code = "EISDIR"
			}
			writeFile(t, p.FlipPath, content)
			if kind == "mode-0000" {
				if err := os.Chmod(p.FlipPath, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(p.FlipPath, 0600) })
				if _, err := os.ReadFile(p.FlipPath); err == nil {
					t.Skip("process can read mode-0000 files")
				}
			}
			if kind == "directory" {
				if err := os.Remove(p.FlipPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.FlipPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			decision, err := p.Decide(opts("decide", "--active", "opencode"), func() (*usage.Routing, error) {
				t.Fatal("routing called on unreadable flip state")
				return nil, nil
			})
			wantReason := "flip state unreadable: " + code + "; fix or remove .orchestrator/flip.json"
			if err != nil || decision.Action != flip.ActionGate || decision.Reason != wantReason {
				t.Fatalf("decide = %+v, %v", decision, err)
			}
			writers := map[string]func() error{
				"gate":  func() error { _, err := p.Gate(opts("gate")); return err },
				"clear": func() error { _, _, err := p.ClearGate(true); return err },
				"pause": func() error { _, err := p.Pause(opts("pause")); return err },
				"run": func() error {
					_, _, err := p.Run(opts("run", "--to", "claude"), func([]string) (*int, string, string) { t.Fatal("launched unreadable state"); return nil, "", "" })
					return err
				},
				"update": func() error {
					return p.Update(func(Record) (any, error) { t.Fatal("callback called on unreadable state"); return nil, nil })
				},
			}
			for name, write := range writers {
				err := write()
				var stateErr *StateError
				if !errors.As(err, &stateErr) || stateErr.Code != code || stateErr.Path != p.FlipPath {
					t.Errorf("%s = %v", name, err)
				}
			}
			if kind == "directory" {
				return
			}
			if kind == "mode-0000" {
				info, err := os.Stat(p.FlipPath)
				if err != nil || info.Mode().Perm() != 0 {
					t.Fatalf("file mode changed: %v, %v", info, err)
				}
				if err := os.Chmod(p.FlipPath, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := readFile(t, p.FlipPath); got != content {
				t.Errorf("bytes changed to %q", got)
			}
		})
	}
}

func TestRunUnlessGatedMakesNoWritesAndDoesNotLaunch(t *testing.T) {
	p := newGitProject(t)
	const gated = `{"awaiting_human":{"reason":"human decision"},"paused":{"door":"claude"}}`
	writeFile(t, p.FlipPath, gated)
	answer, code, err := p.Run(opts("run", "--to", "claude", "--unless-gated"), func([]string) (*int, string, string) { t.Fatal("launched past gate"); return nil, "", "" })
	if err != nil || code != 1 || compact(t, answer) != `{"flipped":false,"reason":"awaiting human"}` {
		t.Fatalf("run = %v, %d, %v", answer, code, err)
	}
	if got := readFile(t, p.FlipPath); got != gated {
		t.Errorf("gate changed: %s", got)
	}
	for _, path := range []string{p.briefPath(), filepath.Join(p.Root, ".git", "info", "exclude")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unexpected write to %s: %v", path, err)
		}
	}
	if p.messages(t) != "" {
		t.Error("posted a handoff past gate")
	}
}

func TestRunLaunchesOutsideLockAndPendingUpdateKeepsANewGate(t *testing.T) {
	p := newGitProject(t)
	_, code, err := p.Run(opts("run", "--to", "claude"), func([]string) (*int, string, string) {
		held, _, err := ownlock.Probe(p.FlipPath + ".lock")
		if err != nil || held {
			t.Fatalf("launcher under lock: held=%v, err=%v", held, err)
		}
		if _, err := p.Gate(opts("gate", "--reason", "new human gate")); err != nil {
			t.Fatal(err)
		}
		return exitStatus(3), "", "lane locked"
	})
	if err != nil || code != 0 {
		t.Fatalf("run = %d, %v", code, err)
	}
	record, err := p.Read()
	if err != nil || jsjson.Path(record.Value, "awaiting_human", "reason") != "new human gate" || jsjson.Path(record.Value, "pending_launch", "to") != "claude" {
		t.Fatalf("state = %+v, %v", record, err)
	}
}

func TestPendingUpdateRefusesStateCorruptedDuringLaunch(t *testing.T) {
	p := newGitProject(t)
	_, _, err := p.Run(opts("run", "--to", "claude"), func([]string) (*int, string, string) {
		writeFile(t, p.FlipPath, "{")
		return exitStatus(3), "", "lane locked"
	})
	var stateErr *StateError
	if !errors.As(err, &stateErr) || readFile(t, p.FlipPath) != "{" {
		t.Fatalf("pending write = %v", err)
	}
}

// Each helper waits at its clock read, inside its real Gate/ClearGate/Pause transaction.
// The parent observes ownership with Probe and releases the callbacks in acquisition order.
func TestFlipWriterProcess(t *testing.T) {
	root := os.Getenv("ORCH_FLIP_WRITER_ROOT")
	if root == "" {
		return
	}
	operation := os.Getenv("ORCH_FLIP_WRITER_OPERATION")
	p := NewProject(root)
	input := bufio.NewReader(os.Stdin)
	fmt.Println("ready")
	if _, err := input.ReadByte(); err != nil {
		t.Fatal(err)
	}
	first := true
	p.Now = func() time.Time {
		if first {
			first = false
			fmt.Println("acquired")
			if _, err := input.ReadByte(); err != nil {
				t.Fatal(err)
			}
		}
		return now
	}
	switch operation {
	case "gate":
		_, err := p.Gate(opts("gate"))
		if err != nil {
			t.Fatal(err)
		}
	case "clear":
		_, _, err := p.ClearGate(true)
		if err != nil {
			t.Fatal(err)
		}
	case "pause":
		_, err := p.Pause(opts("pause"))
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper operation %q", operation)
	}
}

func TestGateClearPauseProcessesReflectASerialOrder(t *testing.T) {
	p := newTestProject(t)
	const seed = `{"awaiting_human":{"door":"claude"},"z":1}`
	writeFile(t, p.FlipPath, seed)
	type child struct {
		command *exec.Cmd
		input   io.WriteCloser
		output  *bufio.Scanner
		stderr  bytes.Buffer
	}
	children := map[string]*child{}
	acquired := make(chan string, 3)
	for _, operation := range []string{"gate", "clear", "pause"} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFlipWriterProcess$")
		cmd.Env = append(os.Environ(), "ORCH_FLIP_WRITER_ROOT="+p.Root, "ORCH_FLIP_WRITER_OPERATION="+operation, "GORACE=atexit_sleep_ms=0")
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		c := &child{command: cmd, input: in, output: bufio.NewScanner(out)}
		cmd.Stderr = &c.stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = in.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		if !c.output.Scan() || c.output.Text() != "ready" {
			t.Fatalf("helper %s not ready: %s", operation, c.stderr.String())
		}
		children[operation] = c
	}
	for operation, c := range children {
		if _, err := c.input.Write([]byte{'s'}); err != nil {
			t.Fatal(err)
		}
		go func(operation string, c *child) {
			if c.output.Scan() && c.output.Text() == "acquired" {
				acquired <- operation
			} else {
				acquired <- "failed:" + operation
			}
		}(operation, c)
	}
	var order []string
	for range children {
		operation := <-acquired
		c, ok := children[operation]
		if !ok {
			t.Fatal(operation)
		}
		held, pid, err := ownlock.Probe(p.FlipPath + ".lock")
		if err != nil || !held || pid != c.command.Process.Pid {
			t.Fatalf("ownership of %s = %v, %d, %v", operation, held, pid, err)
		}
		order = append(order, operation)
		if _, err := c.input.Write([]byte{'r'}); err != nil {
			t.Fatal(err)
		}
	}
	for operation, c := range children {
		if err := c.command.Wait(); err != nil {
			t.Fatalf("%s: %v: %s", operation, err, c.stderr.String())
		}
	}
	expected := newTestProject(t)
	writeFile(t, expected.FlipPath, seed)
	for _, operation := range order {
		var err error
		switch operation {
		case "gate":
			_, err = expected.Gate(opts("gate"))
		case "clear":
			_, _, err = expected.ClearGate(true)
		case "pause":
			_, err = expected.Pause(opts("pause"))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := readFile(t, p.FlipPath), readFile(t, expected.FlipPath); got != want {
		t.Errorf("order %s: state %s, want %s", strings.Join(order, " -> "), got, want)
	}
}

func TestUpdateReadOnlyAndCallbackFailureLeaveStateUntouched(t *testing.T) {
	p := newTestProject(t)
	const original = `{"awaiting_human":{"reason":"keep me"}}`
	writeFile(t, p.FlipPath, original)
	failure := errors.New("callback failed")
	for _, callbackErr := range []error{ErrNoUpdate, failure} {
		err := p.Update(func(prev Record) (any, error) {
			prev.Value.(*jsjson.Object).Delete("awaiting_human")
			return prev.Value, callbackErr
		})
		if callbackErr == ErrNoUpdate && err != nil || callbackErr == failure && !errors.Is(err, failure) {
			t.Fatalf("update error = %v for %v", err, callbackErr)
		}
		if got := readFile(t, p.FlipPath); got != original {
			t.Fatalf("state changed: %s", got)
		}
		held, _, err := ownlock.Probe(p.FlipPath + ".lock")
		if err != nil || held {
			t.Fatalf("transaction not released: %v, %v", held, err)
		}
	}
}
