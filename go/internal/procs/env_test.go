package procs

import (
	"slices"
	"testing"
)

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
