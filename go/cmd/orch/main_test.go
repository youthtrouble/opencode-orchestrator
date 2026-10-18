package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cli"
)

func TestRunDispatchesAndRejectsUnknownCommands(t *testing.T) {
	cases := []struct {
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{nil, 2, "", "usage: orch"},
		{[]string{"bogus"}, 2, "", `unknown command "bogus"`},
		{[]string{"version"}, 0, "0.", ""},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		code := run(c.args, cli.Stdio{Out: &out, Err: &errOut})
		if code != c.wantCode || !strings.Contains(out.String(), c.wantStdout) || !strings.Contains(errOut.String(), c.wantStderr) {
			t.Errorf("run(%v) = %d, stdout %q, stderr %q", c.args, code, out.String(), errOut.String())
		}
	}
}
