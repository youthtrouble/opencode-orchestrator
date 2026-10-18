package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// maxIndent is JSON.stringify's cap on a numeric space argument.
const maxIndent = 10

// JSONEcho implements the hidden `orch json-echo [--indent N]` used by the parity harness:
// stdin is parsed and re-serialized exactly like
// `console.log(JSON.stringify(JSON.parse(stdin), null, N))`. Exit 0 on success, 1 when stdin
// is not JSON, 2 on a bad --indent.
func JSONEcho(args []string, stdio Stdio) int {
	opts, _ := cliargs.ParseArgs(args, []string{"indent"}, nil)
	indent, err := parseIndent(opts)
	if err != nil {
		fmt.Fprintln(stdio.Err, err)
		return 2
	}
	input, err := io.ReadAll(stdio.In)
	if err != nil {
		fmt.Fprintln(stdio.Err, "json-echo: read stdin:", err)
		return 1
	}
	value, err := jsjson.Unmarshal(input)
	if err != nil {
		fmt.Fprintln(stdio.Err, "json-echo:", err)
		return 1
	}
	output, err := jsjson.MarshalIndent(value, strings.Repeat(" ", indent))
	if err != nil {
		fmt.Fprintln(stdio.Err, "json-echo:", err)
		return 1
	}
	fmt.Fprintf(stdio.Out, "%s\n", output)
	return 0
}

func parseIndent(opts cliargs.Opts) (int, error) {
	if !opts.Truthy("indent") {
		return 0, nil
	}
	raw, _ := opts.Value("indent")
	indent, err := strconv.Atoi(raw)
	if err != nil || indent < 0 {
		return 0, fmt.Errorf("json-echo: --indent needs a whole number, got %q", raw)
	}
	return min(indent, maxIndent), nil
}
