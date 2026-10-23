package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlipBareValueOptionsAreOneLineUsageErrors(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"active", "from", "reason", "question", "resume", "to", "brief"} {
		t.Run(key, func(t *testing.T) {
			code, out, errOut := run(t, Flip, []string{"gate", "--cwd", root, "--" + key}, "")
			if code != 2 || out != "" || errOut != "--"+key+" requires a value\n" {
				t.Fatalf("code %d, out %q, err %q", code, out, errOut)
			}
		})
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("usage wrote state: %v, %v", entries, err)
	}
}

func TestFlipCLIUnreadableStateFailsClosed(t *testing.T) {
	for _, kind := range []string{"corrupt", "mode-0000"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".orchestrator", "flip.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			content := "{"
			if kind == "mode-0000" {
				content = `{}`
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "mode-0000" {
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
				if _, err := os.ReadFile(path); err == nil {
					t.Skip("process can read mode-0000 files")
				}
			}
			code, out, errOut := run(t, Flip, []string{"decide", "--active", "opencode", "--cwd", root}, "")
			if code != 0 || !strings.HasPrefix(out, `{"action":"gate","reason":"flip state unreadable:`) || errOut != "" {
				t.Fatalf("decide %d, %q, %q", code, out, errOut)
			}
			for _, args := range [][]string{{"gate"}, {"gate", "--clear"}, {"pause"}, {"run", "--to", "claude"}} {
				code, out, errOut := run(t, Flip, append(args, "--cwd", root), "")
				if code != 1 || out != "" || !strings.Contains(errOut, path) || strings.Count(errOut, "\n") != 1 {
					t.Fatalf("%v: %d, %q, %q", args, code, out, errOut)
				}
			}
			if kind == "mode-0000" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != content {
				t.Fatalf("changed state %q, %v", data, err)
			}
		})
	}
}
