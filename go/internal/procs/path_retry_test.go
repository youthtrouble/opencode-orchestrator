package procs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// runPathCandidate exercises each entry point with deterministic reaping. Detached.start
// is shared by Start and StartReaped; waiting here avoids polling a fire-and-forget child.
func runPathCandidate(t *testing.T, entryPoint, name string) (int, string, string, error) {
	t.Helper()
	switch entryPoint {
	case "Start":
		var stdout, stderr strings.Builder
		child, err := Start(Spec{
			Name: name, Stdin: strings.NewReader("input\n"),
			Stdout: &stdout, Stderr: &stderr, Grace: testGrace,
		})
		if err != nil {
			return 0, "", "", err
		}
		code, err := child.Wait()
		if code == nil {
			t.Fatalf("child exited without a status: %v", err)
		}
		return *code, stdout.String(), stderr.String(), err
	case "Detached":
		output, err := os.Create(filepath.Join(t.TempDir(), "output"))
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		cmd, err := (Detached{Name: name, Output: output}).start()
		if err != nil {
			return 0, "", "", err
		}
		if err := waitError(cmd.Wait()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(output.Name())
		return cmd.ProcessState.ExitCode(), string(data), "", err
	case "RunSync":
		result := RunSync(SyncSpec{Name: name, Grace: testGrace})
		if result.Err != nil {
			return 0, "", "", result.Err
		}
		if result.Status == nil {
			t.Fatal("child exited without a status")
		}
		return *result.Status, result.Stdout, result.Stderr, nil
	default:
		t.Fatalf("unknown entry point %q", entryPoint)
		return 0, "", "", nil
	}
}

func TestPATHExecTimeFallback(t *testing.T) {
	for _, entryPoint := range []string{"Start", "Detached", "RunSync"} {
		t.Run(entryPoint, func(t *testing.T) {
			first, second := t.TempDir(), t.TempDir()
			t.Setenv("PATH", first+":"+second)
			missingInterpreter := "#!" + filepath.Join(t.TempDir(), "missing-interpreter") + "\n"
			for _, tc := range []struct {
				name, firstBody, secondBody, wantCode string
				firstMode, secondMode                 os.FileMode
			}{
				{"missing-interpreter-then-working", missingInterpreter, "#!/bin/sh\necho later\n", "", 0o755, 0o755},
				{"denied-then-missing-interpreter", "#!/bin/sh\n", missingInterpreter, "EACCES", 0o644, 0o755},
				{"both-missing-interpreter", missingInterpreter, missingInterpreter, "ENOENT", 0o755, 0o755},
				{"missing-interpreter-then-denied", missingInterpreter, "#!/bin/sh\n", "EACCES", 0o755, 0o644},
			} {
				t.Run(tc.name, func(t *testing.T) {
					// Remove before rewriting: WriteFile retains an existing file's mode.
					for _, dir := range []string{first, second} {
						if err := os.Remove(filepath.Join(dir, "tool")); err != nil && !errors.Is(err, os.ErrNotExist) {
							t.Fatal(err)
						}
					}
					writeExecutable(t, filepath.Join(first, "tool"), tc.firstBody, tc.firstMode)
					writeExecutable(t, filepath.Join(second, "tool"), tc.secondBody, tc.secondMode)
					code, out, _, err := runPathCandidate(t, entryPoint, "tool")
					if tc.wantCode != "" {
						wantStartError(t, err, "spawn tool "+tc.wantCode, tc.wantCode)
					} else if err != nil || code != 0 || out != "later\n" {
						t.Fatalf("result = (%d, %q, %v), want later executable", code, out, err)
					}
				})
			}
		})
	}
}

func TestPATHDoesNotRetryAStartedChild(t *testing.T) {
	for _, entryPoint := range []string{"Start", "Detached", "RunSync"} {
		t.Run(entryPoint, func(t *testing.T) {
			first, second := t.TempDir(), t.TempDir()
			t.Setenv("PATH", first+":"+second)
			marker := filepath.Join(t.TempDir(), "retried")
			writeExecutable(t, filepath.Join(first, "tool"), "#!/bin/sh\necho first\nexit 7\n", 0o755)
			writeExecutable(t, filepath.Join(second, "tool"), "#!/bin/sh\necho retried > '"+marker+"'\n", 0o755)
			code, out, _, err := runPathCandidate(t, entryPoint, "tool")
			if err != nil || code != 7 || out != "first\n" {
				t.Fatalf("result = (%d, %q, %v), want first child's exit 7", code, out, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("later candidate ran: marker stat = %v", err)
			}
		})
	}
}

func TestPATHFallbackRetainsAllPipes(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("PATH", first+":"+second)
	writeExecutable(t, filepath.Join(first, "tool"), "#!/nonexistent/interp\n", 0o755)
	writeExecutable(t, filepath.Join(second, "tool"), "#!/bin/sh\nread line\necho \"out:$line\"\necho err >&2\n", 0o755)
	code, out, stderr, err := runPathCandidate(t, "Start", "tool")
	if err != nil || code != 0 || out != "out:input\n" || stderr != "err\n" {
		t.Fatalf("result = (%d, %q, %q, %v), want intact stdin/stdout/stderr", code, out, stderr, err)
	}
}

func TestPATHStopsOnOtherExecErrors(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("PATH", first+":"+second)
	writeExecutable(t, filepath.Join(first, "tool"), "not an executable format\n", 0o755)
	install(t, second, "tool", "later", 0o755)
	_, err := run(t, "tool", "", first+":"+second)
	if !errors.Is(err, syscall.ENOEXEC) {
		t.Fatalf("err = %v, want ENOEXEC without retry", err)
	}
}

func TestPATHStopsOnSymlinkLoop(t *testing.T) {
	for _, entryPoint := range []string{"Start", "Detached", "RunSync"} {
		t.Run(entryPoint, func(t *testing.T) {
			first, second := t.TempDir(), t.TempDir()
			t.Setenv("PATH", first+":"+second)
			if err := os.Symlink("tool", filepath.Join(first, "tool")); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "retried")
			writeExecutable(t, filepath.Join(second, "tool"), "#!/bin/sh\necho retried > '"+marker+"'\n", 0o755)
			_, _, _, err := runPathCandidate(t, entryPoint, "tool")
			var failure *StartError
			if !errors.As(err, &failure) || !errors.Is(err, syscall.ELOOP) || failure.Code != "" {
				t.Fatalf("err = %v, want *StartError wrapping ELOOP without an event code", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("later candidate ran: marker stat = %v", err)
			}
		})
	}
}

func TestPATHSearchSkipsENOTDIRButDirectNameKeepsIt(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	plain := filepath.Join(first, "plain")
	writeExecutable(t, plain, "plain file", 0o644)
	install(t, second, "tool", "later", 0o755)
	if got, err := run(t, "tool", "", plain+":"+second); err != nil || got != "later" {
		t.Fatalf("result = (%q, %v), want later executable after ENOTDIR", got, err)
	}
	_, err := run(t, plain+"/tool", "", second)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("direct name err = %v, want ENOTDIR", err)
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = "/dev/fd"
	case "linux":
		dir = "/proc/self/fd"
	default:
		t.Skip("open fd enumeration requires darwin or linux")
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Read names only: statting /dev/fd entries can race the enumeration descriptor's close.
	entries, err := f.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestPATHFailedAttemptsDoNotLeakFDs(t *testing.T) {
	for _, entryPoint := range []string{"Start", "Detached", "RunSync"} {
		t.Run(entryPoint, func(t *testing.T) {
			first, second := t.TempDir(), t.TempDir()
			t.Setenv("PATH", first+":"+second)
			for _, dir := range []string{first, second} {
				writeExecutable(t, filepath.Join(dir, "broken"), "#!/nonexistent/interp\n", 0o755)
			}
			writeExecutable(t, filepath.Join(first, "tool"), "#!/nonexistent/interp\n", 0o755)
			install(t, second, "tool", "later", 0o755)
			// Initialize the runtime's poller/signal machinery before taking the baseline.
			if _, _, _, err := runPathCandidate(t, entryPoint, "tool"); err != nil {
				t.Fatal(err)
			}
			before := countOpenFDs(t)
			for range 5 {
				_, _, _, err := runPathCandidate(t, entryPoint, "broken")
				wantStartError(t, err, "spawn broken ENOENT", "ENOENT")
				if _, _, _, err := runPathCandidate(t, entryPoint, "tool"); err != nil {
					t.Fatal(err)
				}
			}
			if after := countOpenFDs(t); after != before {
				t.Fatalf("open fds before = %d, after = %d", before, after)
			}
		})
	}
}
