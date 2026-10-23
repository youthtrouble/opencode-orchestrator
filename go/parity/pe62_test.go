//go:build parity

package parity

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// launchOptionsBlock is the PE-62 key in a worker state file as orch writes it: indent 2,
// directly after `model`, holding `{}` or one string per line.
var launchOptionsBlock = regexp.MustCompile(`(?m)^(  "model": [^\n]*,\n)  "launch_options": (\{\}|\{\n(?:    "[^\n]*\n)*  \}),\n`)

// withPE62 asserts PE-62 on one case: every worker state orch wrote holds launch_options,
// directly after model, exactly as want (compact JSON in key order), and the frozen JS states
// hold none; then it removes the orch key so the rest of the files are compared as they are.
func withPE62(t *testing.T, js, orch *outcome, want string) {
	t.Helper()
	found := 0
	for name, content := range orch.files {
		if !isWorkerState(name) || !strings.Contains(content, `"launch_options"`) {
			continue
		}
		m := launchOptionsBlock.FindStringSubmatch(content)
		if m == nil {
			t.Errorf("PE-62: orch %s holds launch_options, but not directly after model:\n%s", name, content)
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(m[2])); err != nil || compact.String() != want {
			t.Errorf("PE-62: orch %s launch_options %s (%v), want %s", name, compact.String(), err, want)
		}
		orch.files[name] = strings.Replace(content, m[0], m[1], 1)
		found++
	}
	if found == 0 {
		t.Errorf("PE-62: orch wrote no worker state with launch_options %s", want)
	}
	for name, content := range js.files {
		if isWorkerState(name) && strings.Contains(content, `"launch_options"`) {
			t.Errorf("PE-62: the frozen JS %s holds launch_options:\n%s", name, content)
		}
	}
}

func isWorkerState(name string) bool {
	return strings.HasPrefix(name, "project/workers/") && strings.HasSuffix(name, ".json")
}
