package cli

import (
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// The run itself is tested in internal/bridge and against the JS in the parity bridges group;
// these cover the glue: argv decoding and the streams. opencode-exec has no --help.
func TestOpencodeExecGlue(t *testing.T) {
	replaced, err := nodefs.Resolve("�")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{"help is ignored", []string{"--help", "--prompt"}, 2, "no prompt: --prompt needs a value\n"},
		{"bare prompt", []string{"--prompt"}, 2, "no prompt: --prompt needs a value\n"},
		{"malformed argv is decoded before use", []string{"--prompt-file", "\xff"}, 2, "ENOENT: no such file or directory, open '" + replaced + "'\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := run(t, OpencodeExec, c.args, "")
			if code != c.wantCode || stdout != "" || stderr != c.wantStderr {
				t.Errorf("code %d stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}
