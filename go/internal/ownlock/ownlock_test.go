package ownlock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Helpers communicate exclusively over pipes; deadlines only fail stuck tests.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("OWNLOCK_HELPER") != "1" {
		return
	}
	path := os.Getenv("OWNLOCK_PATH")
	switch os.Getenv("OWNLOCK_MODE") {
	case "hold":
		lock, err := Acquire(path, os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("locked")
		if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || line != "release\n" {
			t.Fatalf("release: %q %v", line, err)
		}
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
	case "try", "late":
		if os.Getenv("OWNLOCK_MODE") == "late" {
			beforeLock = func(string) {
				beforeLock = nil
				fmt.Println("opened")
				if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || line != "lock\n" {
					t.Fatalf("lock: %q %v", line, err)
				}
			}
		}
		lock, err := Acquire(path, os.Getpid())
		if errors.Is(err, ErrHeld) {
			fmt.Println("held")
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("acquired")
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
	case "increment":
		onContention = func(string) { fmt.Println("contended") }
		for i := 0; i < 8; i++ {
			if err := increment(path); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatal("unknown helper mode")
	}
}

type child struct {
	cmd   *exec.Cmd
	input io.WriteCloser
	lines chan string
	done  chan error
	log   *strings.Builder
}

func startChild(t *testing.T, path, mode string) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "OWNLOCK_HELPER=1", "OWNLOCK_PATH="+path, "OWNLOCK_MODE="+mode)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, input: input, lines: make(chan string, 100), done: make(chan error, 1), log: new(strings.Builder)}
	cmd.Stderr = c.log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			c.lines <- scanner.Text()
		}
		close(c.lines)
		c.done <- errors.Join(scanner.Err(), cmd.Wait())
	}()
	t.Cleanup(func() {
		_ = input.Close()
		// Only this test's own child may be signalled.
		_ = cmd.Process.Kill()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("helper cleanup timed out")
		}
	})
	return c
}

func (c *child) line(t *testing.T, want string) {
	t.Helper()
	select {
	case got, ok := <-c.lines:
		if !ok || got != want {
			t.Fatalf("helper line %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper output timed out")
	}
}

func (c *child) wait(t *testing.T, killed bool) {
	t.Helper()
	select {
	case err := <-c.done:
		// Keep the result available for cleanup, without another Wait call.
		c.done <- err
		if (err != nil) != killed {
			t.Fatalf("helper exit: %v; stderr: %s", err, c.log)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper exit timed out")
	}
}

func TestCrossProcessOwnership(t *testing.T) {
	for _, killed := range []bool{false, true} {
		t.Run(strconv.FormatBool(killed), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner")
			c := startChild(t, path, "hold")
			c.line(t, "locked")
			_, err := Acquire(path, os.Getpid())
			var held *HeldError
			if !errors.Is(err, ErrHeld) || !errors.As(err, &held) || held.PID != c.cmd.Process.Pid {
				t.Fatalf("Acquire: %v, want holder %d", err, c.cmd.Process.Pid)
			}
			assertProbe(t, path, true, c.cmd.Process.Pid)
			if killed {
				if err := c.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := io.WriteString(c.input, "release\n"); err != nil {
				t.Fatal(err)
			}
			c.wait(t, killed)
			assertProbe(t, path, false, 0)
			if killed {
				text, err := os.ReadFile(path)
				if err != nil || string(text) != strconv.Itoa(c.cmd.Process.Pid) {
					t.Fatalf("stale metadata: %q %v", text, err)
				}
			}
			lock := acquire(t, path)
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func acquire(t *testing.T, path string) *Lock {
	t.Helper()
	lock, err := Acquire(path, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	return lock
}

func assertProbe(t *testing.T, path string, wantHeld bool, wantPID int) {
	t.Helper()
	held, pid, err := Probe(path)
	if err != nil || held != wantHeld || pid != wantPID {
		t.Fatalf("Probe = %v, %d, %v; want %v, %d", held, pid, err, wantHeld, wantPID)
	}
}

func TestSameProcessAliasesPreserveOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	acquire(t, path)
	alias := path + "-alias"
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, alias, filepath.Join(filepath.Dir(path), ".", "owner")} {
		if _, err := Acquire(name, os.Getpid()); !errors.Is(err, ErrHeld) {
			t.Fatalf("second Acquire(%q): %v", name, err)
		}
		assertProbe(t, name, true, os.Getpid())
		c := startChild(t, name, "try")
		c.line(t, "held")
		c.wait(t, false)
	}
}

func TestRejectNonregularFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, symlink, fifo} {
		if _, err := Acquire(path, os.Getpid()); err == nil {
			t.Errorf("Acquire(%q) succeeded", path)
		}
		if _, _, err := Probe(path); err == nil {
			t.Errorf("Probe(%q) succeeded", path)
		}
		if err := Transaction(path, func() error { t.Error("callback ran"); return nil }); err == nil {
			t.Errorf("Transaction(%q) succeeded", path)
		}
	}
	text, err := os.ReadFile(target)
	if err != nil || string(text) != "untouched" {
		t.Fatalf("target: %q %v", text, err)
	}
}

func TestInodeReplacementRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	var old *os.File
	afterOpen = func(name string) {
		afterOpen = nil
		var err error
		old, err = os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		afterOpen = nil
		if old != nil {
			_ = old.Close()
		}
	})
	lock := acquire(t, path)
	info, err := old.Stat()
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(path)
	if err != nil || os.SameFile(info, current) {
		t.Fatalf("did not switch away from orphan: %v", err)
	}
	if lock.id == (identity{uint64(info.Sys().(*syscall.Stat_t).Dev), uint64(info.Sys().(*syscall.Stat_t).Ino)}) {
		t.Fatal("returned ownership of orphan")
	}
	c := startChild(t, path, "try")
	c.line(t, "held")
	c.wait(t, false)
}

func increment(path string) error {
	return Transaction(path, func() error {
		dataPath := path + ".data"
		text, err := os.ReadFile(dataPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		value := 0
		if len(text) > 0 {
			value, err = strconv.Atoi(string(text))
			if err != nil {
				return err
			}
		}
		return os.WriteFile(dataPath, []byte(strconv.Itoa(value+1)), 0o600)
	})
}

func TestTransactionsAcrossProcessesAndGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter.lock")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- Transaction(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("transaction callback did not start")
	}
	children := []*child{startChild(t, path, "increment"), startChild(t, path, "increment")}
	for _, c := range children {
		c.line(t, "contended")
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("transaction did not finish")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if err := increment(path); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(10 * time.Second):
		t.Fatal("counter goroutines did not finish")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, c := range children {
		c.wait(t, false)
	}
	text, err := os.ReadFile(path + ".data")
	if err != nil || string(text) != "80" {
		t.Fatalf("counter: %q %v, want 80", text, err)
	}
	assertProbe(t, path, false, 0)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("transaction removed lock file:", err)
	}
}

func TestTransactionSharesAcquireOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	lock := acquire(t, path)
	contended, finished := make(chan struct{}), make(chan error, 1)
	onContention = func(string) { close(contended) }
	t.Cleanup(func() { onContention = nil })
	go func() { finished <- Transaction(path, func() error { return nil }) }()
	select {
	case <-contended:
	case <-time.After(10 * time.Second):
		t.Fatal("transaction did not contend")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("transaction did not finish")
	}
}

func TestTransactionReleasesAfterErrorAndPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transaction")
	want := errors.New("callback failed")
	if err := Transaction(path, func() error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() != "panic" {
				t.Error("panic not propagated")
			}
		}()
		_ = Transaction(path, func() error { panic("panic") })
	}()
	acquire(t, path)
}

func TestOpenRacingToOwnedAliasDoesNotCloseOwnersInode(t *testing.T) {
	for _, operation := range []string{"acquire", "probe", "transaction"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner")
			lock := acquire(t, path)
			alias := path + "-alias"
			if err := os.WriteFile(alias, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			beforeOpen = func(name string) {
				beforeOpen = nil
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(path, name); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { beforeOpen = nil })
			var finished chan error
			switch operation {
			case "acquire":
				if _, err := Acquire(alias, os.Getpid()); !errors.Is(err, ErrHeld) {
					t.Fatal(err)
				}
			case "probe":
				assertProbe(t, alias, true, os.Getpid())
			case "transaction":
				contended := make(chan struct{})
				finished = make(chan error, 1)
				onContention = func(string) { close(contended) }
				t.Cleanup(func() { onContention = nil })
				go func() { finished <- Transaction(alias, func() error { return nil }) }()
				select {
				case <-contended:
				case <-time.After(10 * time.Second):
					t.Fatal("alias transaction did not contend")
				}
			}
			c := startChild(t, path, "try")
			c.line(t, "held")
			c.wait(t, false)
			if finished != nil {
				if err := lock.Release(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-finished:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("alias transaction did not finish")
				}
			}
		})
	}
}

func TestFailedWriteReleasesKernelLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	original := writeOwner
	want := errors.New("injected metadata write failure")
	writeOwner = func(*os.File, int) error { return want }
	_, err := Acquire(path, os.Getpid())
	writeOwner = original
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	assertProbe(t, path, false, 0)
	c := startChild(t, path, "try")
	c.line(t, "acquired")
	c.wait(t, false)
}

func TestReleaseDoesNotUnlinkReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	lock := acquire(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	c := startChild(t, path, "hold")
	c.line(t, "locked")
	if err := lock.Release(); err == nil {
		t.Fatal("Release did not report replacement")
	}
	assertProbe(t, path, true, c.cmd.Process.Pid)
	if _, err := io.WriteString(c.input, "release\n"); err != nil {
		t.Fatal(err)
	}
	c.wait(t, false)
}

func TestRetryLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	attempts := 0
	afterOpen = func(name string) {
		attempts++
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterOpen = nil })
	if _, err := Acquire(path, os.Getpid()); err == nil || attempts != 8 {
		t.Fatalf("Acquire: %v, attempts %d", err, attempts)
	}
	attempts = 0
	if err := Transaction(path, func() error { t.Error("callback ran on orphan"); return nil }); err == nil || attempts != 8 {
		t.Fatalf("Transaction: %v, attempts %d", err, attempts)
	}
}

func TestLateLockerCannotOwnUnlinkedInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	oldOwner := acquire(t, path)
	late := startChild(t, path, "late")
	late.line(t, "opened")
	if err := oldOwner.Release(); err != nil {
		t.Fatal(err)
	}
	newOwner := startChild(t, path, "hold")
	newOwner.line(t, "locked")
	if _, err := io.WriteString(late.input, "lock\n"); err != nil {
		t.Fatal(err)
	}
	late.line(t, "held")
	late.wait(t, false)
	assertProbe(t, path, true, newOwner.cmd.Process.Pid)
	if _, err := io.WriteString(newOwner.input, "release\n"); err != nil {
		t.Fatal(err)
	}
	newOwner.wait(t, false)
}

func TestProbeAndAcquireWhileTransactionWaitsOnForeignOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner")
	c := startChild(t, path, "hold")
	c.line(t, "locked")
	contended, finished := make(chan struct{}), make(chan error, 1)
	onContention = func(string) { close(contended) }
	t.Cleanup(func() { onContention = nil })
	go func() { finished <- Transaction(path, func() error { return nil }) }()
	select {
	case <-contended:
	case <-time.After(10 * time.Second):
		t.Fatal("transaction did not contend")
	}
	assertProbe(t, path, true, c.cmd.Process.Pid)
	_, err := Acquire(path, os.Getpid())
	var held *HeldError
	if !errors.As(err, &held) || held.PID != c.cmd.Process.Pid {
		t.Fatalf("pending owner: %v", err)
	}
	if _, err := io.WriteString(c.input, "release\n"); err != nil {
		t.Fatal(err)
	}
	c.wait(t, false)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("transaction did not finish")
	}
}

func TestReleaseAnchorsRelativePathAcrossChdir(t *testing.T) {
	originalDir, otherDir := t.TempDir(), t.TempDir()
	t.Chdir(originalDir)
	lock := acquire(t, "owner")
	t.Chdir(otherDir)
	if err := os.WriteFile("owner", []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(originalDir, "owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original path remains: %v", err)
	}
	text, err := os.ReadFile("owner")
	if err != nil || string(text) != "unrelated" {
		t.Fatalf("other directory's file changed: %q %v", text, err)
	}
}

func TestProbeAcrossPendingTransactionPublication(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transaction")
			alias := path + "-holder"
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, alias); err != nil {
				t.Fatal(err)
			}
			c := startChild(t, alias, "hold")
			c.line(t, "locked")
			waiting, allowKernel, kernelFinished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			publish, probeWaiting, releaseCallback := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var publishOnce, kernelOnce, callbackOnce, probeOnce sync.Once
			originalWait := waitForLock
			waitForLock = func(f *os.File) error {
				close(waiting)
				<-allowKernel
				var err error
				if fail {
					err = syscall.EDEADLK
				} else {
					err = originalWait(f)
				}
				close(kernelFinished)
				<-publish
				return err
			}
			beforeProbeWait = func() { probeOnce.Do(func() { close(probeWaiting) }) }
			finished := make(chan error, 1)
			t.Cleanup(func() {
				kernelOnce.Do(func() { close(allowKernel) })
				publishOnce.Do(func() { close(publish) })
				callbackOnce.Do(func() { close(releaseCallback) })
				select {
				case <-finished:
				case <-time.After(10 * time.Second):
					t.Error("transaction cleanup timed out")
				}
				waitForLock = originalWait
				beforeProbeWait = nil
			})
			go func() {
				finished <- Transaction(path, func() error {
					if fail {
						return errors.New("callback ran after failed lock syscall")
					}
					<-releaseCallback
					return nil
				})
			}()
			awaitEvent(t, waiting, "blocking syscall")
			if _, err := io.WriteString(c.input, "release\n"); err != nil {
				t.Fatal(err)
			}
			c.wait(t, false)
			kernelOnce.Do(func() { close(allowKernel) })
			awaitEvent(t, kernelFinished, "kernel result")
			probeResult := make(chan error, 1)
			go func() {
				held, pid, err := Probe(path)
				if err == nil && (held != !fail || (!fail && pid != os.Getpid()) || (fail && pid != 0)) {
					err = fmt.Errorf("Probe = %v, %d; fail=%v", held, pid, fail)
				}
				probeResult <- err
			}()
			awaitEvent(t, probeWaiting, "Probe waiting for publication")
			// Even with no foreign holder visible, Acquire must not turn a
			// pending local reservation into a second successful owner.
			if _, err := Acquire(path, os.Getpid()); !errors.Is(err, ErrHeld) {
				t.Fatalf("pending Acquire: %v", err)
			}
			publishOnce.Do(func() { close(publish) })
			select {
			case err := <-probeResult:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Probe did not finish")
			}
			callbackOnce.Do(func() { close(releaseCallback) })
			select {
			case err := <-finished:
				finished <- err
				if (fail && !errors.Is(err, syscall.EDEADLK)) || (!fail && err != nil) {
					t.Fatalf("Transaction: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Transaction did not finish")
			}
		})
	}
}

func awaitEvent(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
