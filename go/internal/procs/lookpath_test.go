package procs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install writes an /bin/sh script that prints who it is, at dir/name with mode.
func install(t *testing.T, dir, name, says string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho "+says+"\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// run starts name in dir with only the given PATH and returns its trimmed stdout.
func run(t *testing.T, name, dir, path string) (string, error) {
	t.Helper()
	var out strings.Builder
	child, err := Start(Spec{Name: name, Dir: dir, Env: []string{"PATH=" + path}, Stdout: &out, Grace: testGrace})
	if err != nil {
		return "", err
	}
	if _, err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String()), nil
}

func wantStartError(t *testing.T, err error, message, code string) {
	t.Helper()
	var failure *StartError
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v (%T), want a *StartError", err, err)
	}
	if err.Error() != message || failure.Code != code {
		t.Errorf("err = %q (code %q), want %q (code %q)", err, failure.Code, message, code)
	}
}

func TestStartReportsMissingCommandAsNode(t *testing.T) {
	_, err := run(t, "nosuch", "", t.TempDir())
	wantStartError(t, err, "spawn nosuch ENOENT", "ENOENT")
}

func TestStartReportsNonExecutableFileAsEACCES(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, "x", "x", 0o644)
	_, err := run(t, "x", "", dir)
	wantStartError(t, err, "spawn x EACCES", "EACCES")
}

func TestStartReportsDirectoryAsEACCES(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, "x", "", dir)
	wantStartError(t, err, "spawn x EACCES", "EACCES")
}

func TestStartSearchesPastEACCES(t *testing.T) {
	denied, allowed := t.TempDir(), t.TempDir()
	install(t, denied, "x", "denied", 0o644)
	install(t, allowed, "x", "allowed", 0o755)
	got, err := run(t, "x", "", denied+":"+allowed)
	if err != nil || got != "allowed" {
		t.Errorf("ran %q (%v), want the later executable entry", got, err)
	}
}

func TestStartResolvesEmptyPathEntryInDir(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, "y", "in-dir", 0o755)
	for _, path := range []string{":" + t.TempDir(), t.TempDir() + ":", ""} {
		got, err := run(t, "y", dir, path)
		if err != nil || got != "in-dir" {
			t.Errorf("PATH=%q ran %q (%v), want the file in Dir", path, got, err)
		}
	}
}

func TestStartUsesNameWithSlashDirectly(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, "z", "direct", 0o755)
	empty := t.TempDir()
	if got, err := run(t, filepath.Join(dir, "z"), "", empty); err != nil || got != "direct" {
		t.Errorf("absolute name ran %q (%v)", got, err)
	}
	if got, err := run(t, "./z", dir, empty); err != nil || got != "direct" {
		t.Errorf("relative name ran %q (%v), want it resolved in Dir", got, err)
	}
	_, err := run(t, filepath.Join(dir, "nosuch"), "", empty)
	wantStartError(t, err, "spawn "+filepath.Join(dir, "nosuch")+" ENOENT", "ENOENT")
	install(t, dir, "plain", "x", 0o644)
	_, err = run(t, filepath.Join(dir, "plain"), "", empty)
	wantStartError(t, err, "spawn "+filepath.Join(dir, "plain")+" EACCES", "EACCES")
}

func TestStartWithoutPathSearchesTheDefaultPath(t *testing.T) {
	var out strings.Builder
	child, err := Start(Spec{Name: "sh", Args: []string{"-c", "echo ok"}, Env: []string{}, Stdout: &out, Grace: testGrace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Wait(); err != nil || strings.TrimSpace(out.String()) != "ok" {
		t.Errorf("sh from the default path said %q (%v)", out.String(), err)
	}
}
