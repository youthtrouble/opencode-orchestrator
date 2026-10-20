//go:build parity

package parity

import (
	"regexp"
	"strings"
	"testing"
)

// privateTempSuffix matches the suffix orch's private writes give their temp files (PE-21).
var privateTempSuffix = regexp.MustCompile(`\.tmp-[0-9]+`)

// assertPE21FailedRename asserts PE-21 for a write of file (an outcome key) whose rename
// failed: the JS leaves the fixed `<file>.tmp` holding the content it tried to write (it must
// contain written), and orch leaves no temp at all. It then drops the JS leftover and spells
// orch's temp names as the JS's, so the caller can compare the rest.
func assertPE21FailedRename(t *testing.T, js, orch *outcome, file, written string) {
	t.Helper()
	leftover := file + ".tmp"
	if content, ok := js.files[leftover]; !ok || !strings.Contains(content, written) {
		t.Errorf("js: %s = %q (present %v), want the leftover holding %q", leftover, content, ok, written)
	}
	for name := range orch.files {
		if strings.HasPrefix(name, leftover) {
			t.Errorf("orch left %s", name)
		}
	}
	delete(js.files, leftover)
	orch.stdout = privateTempSuffix.ReplaceAllString(orch.stdout, ".tmp")
	orch.stderr = privateTempSuffix.ReplaceAllString(orch.stderr, ".tmp")
}
