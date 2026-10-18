package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/version"
)

func run(t *testing.T, command Command, args []string, stdin string) (code int, stdout, stderr string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(stdin), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var out, errOut bytes.Buffer
	code = command(args, Stdio{In: in, Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

func TestVersionPrintsTheReleaseNumber(t *testing.T) {
	code, stdout, _ := run(t, Version, nil, "")
	if code != 0 || !strings.HasPrefix(stdout, version.Number()) || !strings.HasSuffix(stdout, "\n") {
		t.Errorf("version: code %d, stdout %q", code, stdout)
	}
}

func TestJSONEcho(t *testing.T) {
	input := `{"b":[1,{}],"1":"x"}`
	cases := []struct {
		name     string
		args     []string
		stdin    string
		wantCode int
		wantOut  string
	}{
		{"compact by default", nil, input, 0, `{"1":"x","b":[1,{}]}` + "\n"},
		{"indent 0 is compact", []string{"--indent", "0"}, input, 0, `{"1":"x","b":[1,{}]}` + "\n"},
		{"indent 2", []string{"--indent", "2"}, input, 0, "{\n  \"1\": \"x\",\n  \"b\": [\n    1,\n    {}\n  ]\n}\n"},
		{"indent is capped at 10 like JSON.stringify", []string{"--indent", "12"}, `[1]`, 0, "[\n          1\n]\n"},
		{"bad indent", []string{"--indent", "two"}, input, 2, ""},
		{"invalid JSON", nil, `{"a":}`, 1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := run(t, JSONEcho, c.args, c.stdin)
			if code != c.wantCode || stdout != c.wantOut {
				t.Errorf("code %d stdout %q stderr %q; want code %d stdout %q", code, stdout, stderr, c.wantCode, c.wantOut)
			}
			if c.wantCode != 0 && stderr == "" {
				t.Error("failure without a message on stderr")
			}
		})
	}
}
