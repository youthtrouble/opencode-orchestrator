package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/ownlock"
)

func TestStatusPrintsAnyParsedValueOrNotStarted(t *testing.T) {
	for content, want := range map[string]string{
		"":                         `{"status":"not_started"}`,
		`null`:                     `null`,
		`5`:                        `5`,
		`"x"`:                      `"x"`,
		`[]`:                       `[]`,
		`{"b":1,"a":2}`:            `{"b":1,"a":2}`,
		`{bad`:                     `{"status":"not_started"}`,
		"\ufeff{}":                 `{"status":"not_started"}`,
		`{"lanes":{"1":{},"a":1}}`: `{"lanes":{"1":{},"a":1}}`,
	} {
		f := newFixture(t)
		if content != "" {
			f.write("supervisor-state.json", content)
		}
		if got := compact(t, f.sup.Status()); got != want {
			t.Errorf("status over %q = %s, want %s", content, got, want)
		}
	}
	f := newFixture(t)
	f.mkdir("supervisor-state.json")
	if got := compact(t, f.sup.Status()); got != `{"status":"not_started"}` {
		t.Errorf("status over a directory = %s", got)
	}
}

func TestStopCreatesTheEmptyStopFile(t *testing.T) {
	f := newFixture(t)
	line, err := f.sup.Stop()
	if err != nil || line != "stop requested; workers are unaffected" || f.read("supervisor.stop") != "" {
		t.Fatalf("Stop() = %q, %v", line, err)
	}
	g := newFixture(t)
	g.mkdir("supervisor.stop")
	_, err = g.sup.Stop()
	wantError(t, err, "EISDIR: illegal operation on a directory, open '"+g.path("supervisor.stop")+"'")
}

func TestAck(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 5_000_000, time.UTC)
	const stamp = `"at":"2026-10-19T12:00:00.005Z"`
	for _, tc := range []struct {
		name, state, acks string
		event, by         any
		wantLine          string
		wantRefused       bool
		wantAcks          string // compact; "" means not written
	}{
		{"a known id", `{"events":{"e1":{"status":"queued"}}}`, "", "e1", "coordinator", "acknowledged (receipt, not acceptance)", false, `{"e1":{"by":"coordinator",` + stamp + `}}`},
		{"an unknown id", `{"events":{}}`, "", "e2", "coordinator", "unknown event id", true, ""},
		{"no --event", `{"events":{"e1":1}}`, "", nil, "coordinator", "unknown event id", true, ""},
		{"a bare --event reads key true", `{"events":{"true":1}}`, "", true, true, "acknowledged (receipt, not acceptance)", false, `{"true":{"by":true,` + stamp + `}}`},
		{"a falsy stored event", `{"events":{"e1":0}}`, "", "e1", "x", "unknown event id", true, ""},
		{"an existing ack keeps its position", `{"events":{"e1":1}}`, `{"e1":{"by":"old"},"e0":1}`, "e1", "X", "acknowledged (receipt, not acceptance)", false, `{"e1":{"by":"X",` + stamp + `},"e0":1}`},
		{"constructor is not an event", `{"events":{}}`, "", "constructor", "coordinator", "unknown event id", true, ""},
		{"toString is not an event", `{"events":{}}`, "", "toString", "coordinator", "unknown event id", true, ""},
		{"__proto__ is not inherited", `{"events":{}}`, `{"a":1}`, "__proto__", "coordinator", "unknown event id", true, `{"a":1}`},
		{"an own constructor event", `{"events":{"constructor":1}}`, "", "constructor", "c", "acknowledged (receipt, not acceptance)", false, `{"constructor":{"by":"c",` + stamp + `}}`},
		{"__proto__ without an own ack key sets the prototype", `{"events":{"__proto__":1}}`, `{"a":1}`, "__proto__", "coordinator", "acknowledged (receipt, not acceptance)", false, `{"a":1}`},
		{"__proto__ with an own ack key", `{"events":{"__proto__":1}}`, `{"__proto__":{"x":1}}`, "__proto__", "c", "acknowledged (receipt, not acceptance)", false, `{"__proto__":{"by":"c",` + stamp + `}}`},
		{"PE-16: events a string read as {}", `{"events":"ab"}`, "", "length", "c", "unknown event id", true, ""},
		{"PE-16: array acks written as an object", `{"events":{"0":1}}`, `[1]`, "0", "c", "acknowledged (receipt, not acceptance)", false, `{"0":{"by":"c",` + stamp + `}}`},
		{"a primitive state has no events", `5`, "", "e1", "c", "unknown event id", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("supervisor-state.json", tc.state)
			if tc.acks != "" {
				f.write("supervisor-acks.json", tc.acks)
			}
			got, err := f.sup.Ack(tc.event, tc.by, at)
			if err != nil || got.Line != tc.wantLine || got.Refused != tc.wantRefused {
				t.Fatalf("Ack = %+v, %v", got, err)
			}
			if tc.wantAcks == "" {
				if tc.acks == "" && nodefs.Exists(f.path("supervisor-acks.json")) {
					t.Error("acks written on a refusal")
				}
				return
			}
			if got := compactFile(t, f.read("supervisor-acks.json")); got != tc.wantAcks {
				t.Errorf("acks = %s, want %s", got, tc.wantAcks)
			}
		})
	}
}

func TestAckConcurrentGoroutines(t *testing.T) {
	f := newFixture(t)
	events := jsjson.NewObject()
	const count = 16
	for i := range count {
		events.Set(fmt.Sprint(i), true)
	}
	f.write("supervisor-state.json", `{"events":`+compact(t, events)+`}`)
	start := make(chan struct{})
	errors := make(chan error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := f.sup.Ack(fmt.Sprint(i), "c", f.now)
			if err == nil && result.Refused {
				err = fmt.Errorf("ack %d refused: %s", i, result.Line)
			}
			errors <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	acks := readJSON(f.sup.AcksPath, emptyObject)
	for i := range count {
		if jsjson.Path(acks, fmt.Sprint(i), "by") != "c" {
			t.Errorf("missing receipt %d: %s", i, compact(t, acks))
		}
	}
}

// The FIFO stops the first Ack at its data read, while it holds the transaction.
// The second helper observes that holder through Probe before attempting its Ack.
// No ownlock-private hooks, lock-file descriptors, sleeps or live services are used.
func TestAckConcurrentProcesses(t *testing.T) {
	f := newFixture(t)
	f.write("supervisor-state.json", `{"events":{"first":1,"second":1}}`)
	if err := syscall.Mkfifo(f.sup.AcksPath, 0o600); err != nil {
		t.Fatal(err)
	}
	startHelper := func(id string) (*exec.Cmd, *bufio.Reader) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestAckProcessHelper$")
		cmd.Env = append(os.Environ(), "ORCH_ACK_HELPER_ROOT="+f.root, "ORCH_ACK_HELPER_ID="+id)
		cmd.Stderr = os.Stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill() // Only this test's helper, never a service.
				_ = cmd.Wait()
			}
		})
		return cmd, bufio.NewReader(stdout)
	}
	first, _ := startHelper("first")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	writer, err := openAckFIFO(ctx, f.sup.AcksPath)
	if err != nil {
		t.Fatalf("first Ack did not reach its data read: %v", err)
	}
	defer writer.Close()
	second, output := startHelper("second")
	observed := make(chan string, 1)
	go func() {
		line, _ := output.ReadString('\n')
		observed <- line
	}()
	select {
	case line := <-observed:
		if line != "contended\n" {
			t.Fatalf("second helper: %q, want contended", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second helper did not observe contention")
	}
	if _, err := writer.WriteString(`{}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []*exec.Cmd{first, second} {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Ack helper did not finish")
		}
	}
	acks := readJSON(f.sup.AcksPath, emptyObject)
	for _, id := range []string{"first", "second"} {
		if jsjson.Path(acks, id, "by") != id {
			t.Errorf("missing %s receipt: %s", id, compact(t, acks))
		}
	}
}

// openAckFIFO keeps the normal blocking-open handshake. On cancellation a
// temporary reader releases open(2), then both descriptors and the goroutine are
// joined before returning. The fixture must keep the FIFO pathname in place
// while the writer is pending; Ack cannot rename it until that handshake succeeds.
func openAckFIFO(ctx context.Context, path string) (*os.File, error) {
	type result struct {
		file *os.File
		err  error
	}
	opened := make(chan result)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		opened <- result{file, err}
	}()
	defer func() { <-joined }()
	select {
	case opened := <-opened:
		return opened.file, opened.err
	case <-ctx.Done():
		// Nonblocking applies only to this cancellation reader, never the
		// writer whose blocking open establishes the Ack read boundary.
		reader, unblockErr := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		opened := <-opened
		var closeReaderErr, closeWriterErr error
		if reader != nil {
			closeReaderErr = reader.Close()
		}
		if opened.file != nil {
			closeWriterErr = opened.file.Close()
		}
		return nil, errors.Join(ctx.Err(), unblockErr, opened.err, closeReaderErr, closeWriterErr)
	}
}

func TestAckFIFOOpenJoinsAfterHelperEarlyFailure(t *testing.T) {
	f := newFixture(t)
	f.mkdir("")
	if err := syscall.Mkfifo(f.sup.AcksPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		writer, err := openAckFIFO(ctx, f.sup.AcksPath)
		if writer != nil {
			err = errors.Join(err, writer.Close(), errors.New("writer opened without a reader"))
		}
		finished <- err
	}()
	// This owned helper exits before it can open the FIFO for reading. Its
	// exit, not a timer or polling loop, triggers cancellation of the writer.
	cmd := exec.Command(os.Args[0], "-test.run=^TestAckProcessHelper$")
	cmd.Env = append(os.Environ(), "ORCH_ACK_HELPER_ROOT="+f.root, "ORCH_ACK_HELPER_ID=exit-before-read")
	err := cmd.Run()
	cancel()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Errorf("early-failure helper exit = %v, want 23", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FIFO open = %v, want context cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not unblock and join the FIFO open goroutine")
	}
}

func TestAckProcessHelper(t *testing.T) {
	root := os.Getenv("ORCH_ACK_HELPER_ROOT")
	if root == "" {
		return
	}
	p := NewProject(root)
	id := os.Getenv("ORCH_ACK_HELPER_ID")
	if id == "exit-before-read" {
		os.Exit(23)
	}
	if id == "second" {
		held, pid, err := ownlock.Probe(p.AcksPath + ".lock")
		if err != nil || !held || pid == os.Getpid() {
			t.Fatalf("first Ack must hold the separate lock while reading data: held=%v pid=%d err=%v", held, pid, err)
		}
		fmt.Println("contended")
	}
	result, err := p.Ack(id, id, time.Unix(t0, 0))
	if err != nil || result.Refused {
		t.Fatalf("Ack(%s) = %+v, %v", id, result, err)
	}
}

func TestAckCrashes(t *testing.T) {
	for _, tc := range []struct{ name, state, acks, want string }{
		{"state null", `null`, "", "Cannot read properties of null (reading 'events')"},
		{"acks null", `{"events":{"e1":1}}`, `null`, "Cannot set properties of null (setting 'e1')"},
		{"acks a number", `{"events":{"e1":1}}`, `5`, "Cannot create property 'e1' on number '5'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("supervisor-state.json", tc.state)
			if tc.acks != "" {
				f.write("supervisor-acks.json", tc.acks)
			}
			_, err := f.sup.Ack("e1", "c", time.Now())
			wantError(t, err, tc.want)
		})
	}
	f := newFixture(t)
	f.write("supervisor-state.json", `{"events":{"e1":1}}`)
	f.mkdir("supervisor-acks.json")
	_, err := f.sup.Ack("e1", "c", time.Now())
	acks := f.path("supervisor-acks.json")
	wantRenameOntoDirError(t, err, acks)
}

// The supervisor's files hold events and lane state: owner-only under a permissive umask (PE-21).
func TestWriteJSONIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), ".orchestrator", "supervisor-acks.json")
	if err := writeJSON(path, jsjson.NewObject()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode %v, want 0600", got)
	}
}
