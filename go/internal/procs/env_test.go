package procs

import (
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestWithPWD(t *testing.T) {
	t.Setenv("PWD", t.TempDir())
	cwd, err := syscall.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir string
		want      []string
	}{
		{"empty", "", []string{"FOO=bar", "PWD=old", "PWD=older"}},
		{"absolute", "/a/b", []string{"FOO=bar", "PWD=/a/b"}},
		{"relative", "child/../project", []string{"FOO=bar", "PWD=" + filepath.Join(cwd, "project")}},
		{"clean", "/a/./b/", []string{"FOO=bar", "PWD=/a/b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := make([]string, 3, 8)
			copy(env, []string{"FOO=bar", "PWD=old", "PWD=older"})
			before := slices.Clone(env[:cap(env)])
			got, err := WithPWD(env, tc.dir)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("WithPWD = %q, %v; want %q", got, err, tc.want)
			}
			if !slices.Equal(env[:cap(env)], before) {
				t.Error("WithPWD mutated its input backing array")
			}
		})
	}
}

func TestWithoutEnv(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"CLAUDECODE=1",
		"CLAUDECODE_X=keep",
		"CLAUDE_CODE_SESSION_ID=keep",
		"XCLAUDECODE=keep",
		"claudecode=keep",
		"CLAUDECODE=",
		"HOME=/h",
		"HOME=/h2",
	}
	want := []string{
		"PATH=/bin",
		"CLAUDECODE_X=keep",
		"CLAUDE_CODE_SESSION_ID=keep",
		"XCLAUDECODE=keep",
		"claudecode=keep",
		"HOME=/h",
		"HOME=/h2",
	}
	if got := WithoutEnv(env, "CLAUDECODE"); !slices.Equal(got, want) {
		t.Errorf("WithoutEnv = %q, want %q", got, want)
	}
	if env[1] != "CLAUDECODE=1" {
		t.Error("WithoutEnv modified its input")
	}
}
