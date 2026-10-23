package cli

import (
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// The run itself is tested in internal/bridge and against the JS in the parity bridges group;
// these cover the glue: argv decoding and the streams.
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

// PE-46: --help prints help before the prompt is read.
func TestOpencodeExecHelp(t *testing.T) {
	code, stdout, stderr := run(t, OpencodeExec, []string{"--help", "--prompt"}, "")
	if code != 0 || !strings.HasPrefix(stdout, "Run one headless opencode turn") || stderr != "" {
		t.Errorf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
}
