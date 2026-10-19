package cli

import (
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// The run itself is tested in internal/bridge and against the JS in the parity bridges group;
// these cover the glue: argv decoding and the streams.
func TestClaudeExecGlue(t *testing.T) {
	replaced, err := nodefs.Resolve("\ufffd")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"help on stdout", []string{"--prompt", "\xff", "--help"}, 0, cmdref.ClaudeExecHelp + "\n", ""},
		{"bare prompt", []string{"--prompt"}, 2, "", "no prompt: --prompt needs a value\n"},
		{"malformed argv is decoded before use", []string{"--prompt-file", "\xff"}, 2, "", "ENOENT: no such file or directory, open '" + replaced + "'\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := run(t, ClaudeExec, c.args, "")
			if code != c.wantCode || stdout != c.wantStdout || stderr != c.wantStderr {
				t.Errorf("code %d stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}
