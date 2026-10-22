//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// doctorAnswer seeds a canned answer of a parity fake (see the fakes' header): cli's output for
// argv joined by "-".
func doctorAnswer(cli, argv, out string) seed {
	return seedFile(filepath.Join("fake-answers", cli, argv), out)
}

// doctorLog seeds opencode's log, which doctor scans for plugin errors.
func doctorLog(content string) seed {
	return seedFile(".local/share/opencode/log/opencode.log", content)
}

const doctorAgentsLoaded = `[{"id":"conductor"},{"id":"architect-oai"},{"id":"coder-oai"},{"id":"reviewer-oai"}]`

// testDoctor compares `orch doctor` with bin/doctor.mjs over the fakes' canned answers. Each
// case points the JS at orch with ORCH_BIN, so both print the same `orch binary` row; it asserts
// PE-18 (orch's self-test row and its Go unit-test row) and compares the rest.
func (h *harness) testDoctor(t *testing.T) {
	green, _ := readCacheFixtures(t).Get("green")
	pathEnv := "PATH=" + h.pathWithoutGoOrOrch(t)
	orchBinEnv := "ORCH_BIN=" + h.orchBin
	healthyWithoutLog := seeds(
		doctorAnswer("opencode", "--version", "1.2.3"),
		doctorAnswer("codex", "--version", "codex-cli 0.9.0\nsecond line"),
		doctorAnswer("claude", "--version", "2.0.0 (Claude Code)"),
		doctorAnswer("codex", "login-status", "Logged in using ChatGPT"),
		doctorAnswer("claude", "auth-status", `{"loggedIn": true, "authMethod": "claude.ai"}`),
		doctorAnswer("opencode", "auth-list", "OpenAI oauth\nAnthropic api"),
		doctorAnswer("opencode", "plugin-list", "orchestrator.ts"),
		doctorAnswer("opencode", "debug-agents", doctorAgentsLoaded),
		seedFile(".config/opencode/opencode.jsonc", "{}"),
		seedCache(green, time.Now()),
		codexAnswers(t),
	)
	healthy := seeds(healthyWithoutLog, doctorLog("INFO all good\n"))
	var logLines []string
	for i := 0; i < 500; i++ {
		logLines = append(logLines, "INFO ok")
	}
	long := "ERROR orchestrator " + strings.Repeat("a", 100)
	logLines[50] = "ERROR orchestrator outside the last 400"
	logLines[200], logLines[300], logLines[400] = "ERROR orchestrator first", long, long
	logLines[499] = "ERROR orchestrator " + strings.Repeat("a", 36) + "😀" // straddles the 300 cut (PE-1)
	cases := []struct {
		name string
		seed seed
	}{
		{"all-ok", healthy},
		{"nothing-scripted", nil},
		{"claude-auth-text", seeds(healthy, doctorAnswer("claude", "auth-status", "Logged in as someone"))},
		{"claude-auth-unclear-cut", seeds(healthy, doctorAnswer("claude", "auth-status", strings.Repeat("é", 200)))},
		{"auth-list-openai-only-plugin-missing", seeds(healthy, doctorAnswer("opencode", "auth-list", "openai"), doctorAnswer("opencode", "plugin-list", "nothing here"))},
		{"agents-missing-id", seeds(healthy, doctorAnswer("opencode", "debug-agents", `[{"id":"conductor"},7,{"id":["a","b"]},{}]`))},
		{"agents-not-json", seeds(healthy, doctorAnswer("opencode", "debug-agents", "not json "+strings.Repeat("x", 300)))},
		{"agents-null-element", seeds(healthy, doctorAnswer("opencode", "debug-agents", `[{"id":"conductor"},null]`))},
		{"log-errors-cut", seeds(healthy, doctorLog(strings.Join(logLines, "\n")))},
		{"log-is-a-directory", seeds(healthyWithoutLog, func(t *testing.T, sb sandbox) {
			mustMkdir(t, filepath.Join(sb.home, ".local", "share", "opencode", "log", "opencode.log"))
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inv := invocation{
				js:        []string{h.node, filepath.Join(h.orchJS, "bin", "doctor.mjs")},
				orch:      []string{h.orchBin, "doctor"},
				seed:      c.seed,
				env:       []string{pathEnv, orchBinEnv},
				normalize: normalizeGeneratedAt,
			}
			js, orch := h.runPair(t, inv)
			js.stdout = maskJSUnitTests(t, js.stdout)
			orch.stdout = maskOrchRows(t, orch.stdout, filepath.Dir(h.orchJS), orchSelfTestRow)
			compareOutcomes(t, js, orch)
		})
	}
	t.Run("registry-missing", func(t *testing.T) {
		t.Parallel()
		h.testDoctorRegistryMissing(t, healthy, pathEnv, orchBinEnv)
	})
	t.Run("no-binary", func(t *testing.T) {
		t.Parallel()
		h.testDoctorNoBinary(t, healthy, pathEnv)
	})
	t.Run("log-errors-redacted", func(t *testing.T) {
		t.Parallel()
		h.testDoctorLogRedacted(t, healthyWithoutLog, pathEnv, orchBinEnv)
	})
}

// testDoctorLogRedacted seeds a plugin error quoting a credential, the sandbox home and an
// email: both runtimes print the row redacted as an incident is (S3/R10), and identically.
func (h *harness) testDoctorLogRedacted(t *testing.T, healthyWithoutLog seed, pathEnv, orchBinEnv string) {
	apiKey := "sk-" + "ant-" + strings.Repeat("x", 24)
	inv := invocation{
		js:   []string{h.node, filepath.Join(h.orchJS, "bin", "doctor.mjs")},
		orch: []string{h.orchBin, "doctor"},
		seed: seeds(healthyWithoutLog, func(t *testing.T, sb sandbox) {
			doctorLog("ERROR orchestrator auth "+apiKey+" in "+sb.home+"/p for me@example.com\n")(t, sb)
		}),
		env:       []string{pathEnv, orchBinEnv},
		normalize: normalizeGeneratedAt,
	}
	js, orch := h.runPair(t, inv)
	js.stdout = maskJSUnitTests(t, js.stdout)
	orch.stdout = maskOrchRows(t, orch.stdout, filepath.Dir(h.orchJS), orchSelfTestRow)
	const row = "WARN plugin errors in log  —  ERROR orchestrator auth <token> in ~/p for <email>\n"
	if !strings.Contains(js.stdout, row) {
		t.Errorf("js stdout lacks %q:\n%s", row, js.stdout)
	}
	compareOutcomes(t, js, orch)
}

const jsNoBinaryRow = `WARN orch binary  —  not found (runtime js only; see README "Go runtime")` + "\n"

// testDoctorNoBinary runs the JS without ORCH_BIN, with no ~/.local/bin/orch in the sandbox HOME
// and no orch on pathEnv (see pathWithoutGoOrOrch), so it warns; orch is always its own binary
// (PE-18), so its OK row is masked against that warning.
func (h *harness) testDoctorNoBinary(t *testing.T, healthy seed, pathEnv string) {
	inv := invocation{
		js:        []string{h.node, filepath.Join(h.orchJS, "bin", "doctor.mjs")},
		orch:      []string{h.orchBin, "doctor"},
		seed:      healthy,
		env:       []string{pathEnv},
		normalize: normalizeGeneratedAt,
	}
	js, orch := h.runPair(t, inv)
	js.stdout = maskJSUnitTests(t, js.stdout)
	if !strings.Contains(js.stdout, jsNoBinaryRow) {
		t.Errorf("js: no %q in %q", jsNoBinaryRow, js.stdout)
	}
	js.stdout = strings.Replace(js.stdout, jsNoBinaryRow, "<orch binary>\n", 1)
	orch.stdout = maskOrchRows(t, orch.stdout, filepath.Dir(h.orchJS), orchSelfTestRow)
	orch.stdout = orchBinaryRow.ReplaceAllString(orch.stdout, "<orch binary>\n")
	compareOutcomes(t, js, orch)
}

// testDoctorRegistryMissing runs doctor over a copied JS tree without models.json (the JS reads
// <ROOT>/models.json whatever ORCH_REGISTRY says). Both print Node's ENOENT line as the
// usage/routing failure.
func (h *harness) testDoctorRegistryMissing(t *testing.T, healthy seed, pathEnv, orchBinEnv string) {
	root := h.jsTreeWithRegistry(t, registryMissing)
	inv := invocation{
		js:        []string{h.node, filepath.Join(root, "bin", "doctor.mjs")},
		orch:      []string{h.orchBin, "doctor"},
		seed:      healthy,
		env:       []string{pathEnv, orchBinEnv, "ORCH_HOME=" + root, "ORCH_REGISTRY=" + filepath.Join(root, "models.json")},
		normalize: normalizeGeneratedAt,
	}
	js, orch := h.runPair(t, inv)
	// PE-18: the JS unit tests need models.json and fail; orch's self-test fails on the same read.
	if !jsUnitTestsFailRow.MatchString(js.stdout) {
		t.Errorf("js: no failing unit tests row in %q", js.stdout)
	}
	js.stdout = jsUnitTestsFailRow.ReplaceAllString(js.stdout, "<unit tests>")
	enoent := "ENOENT: no such file or directory, open '" + filepath.Join(root, "models.json") + "'"
	orch.stdout = maskOrchRows(t, orch.stdout, filepath.Dir(root), "FAIL orch self-test  —  routing: "+enoent+"\n")
	if want := "FAIL usage/routing  —  " + enoent + "\n"; !strings.Contains(js.stdout, want) {
		t.Errorf("js: no %q in %q", want, js.stdout)
	}
	compareOutcomes(t, js, orch)
}

// pathWithoutGoOrOrch is the harness PATH, asserted to resolve neither a go toolchain nor an
// orch. It holds only the fakes and links to node and sqlite3 (S4/R10), so this is a cheap
// guard: with a go, orch's unit tests row would run `go test` under the sandbox HOME, its
// result depending on the machine; with an orch, the JS would find it and print its OK row.
func (h *harness) pathWithoutGoOrOrch(t *testing.T) string {
	t.Helper()
	for _, dir := range filepath.SplitList(h.path) {
		for _, name := range []string{"go", "gofmt", "orch"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				t.Fatalf("harness PATH directory %s holds %s", dir, name)
			}
		}
	}
	return h.path
}

var (
	jsUnitTestsRow     = regexp.MustCompile(`(?m)^OK   unit tests  —  pass \d+$`)
	jsUnitTestsFailRow = regexp.MustCompile(`(?m)^FAIL unit tests  —  fail [1-9]\d*$`)
	orchBinaryRow      = regexp.MustCompile(`(?m)^OK   orch binary  —  \S.* at (/\S*)\n`)
	orchSelfTestRow    = "OK   orch self-test  —  json, routing, executable\n"
)

// maskJSUnitTests requires the JS `node --test` row to pass and masks it (PE-18).
func maskJSUnitTests(t *testing.T, stdout string) string {
	t.Helper()
	if !jsUnitTestsRow.MatchString(stdout) {
		t.Errorf("js: no passing unit tests row in %q", stdout)
	}
	return jsUnitTestsRow.ReplaceAllString(stdout, "<unit tests>")
}

// maskOrchRows requires orch's rows (its binary, which compareOutcomes then compares, PE-18's
// `orch on PATH` warning, there being no orch on pathWithoutGoOrOrch, and its selfTestRow and Go
// unit-test row not run, there being no go either), removes the PATH row and selfTestRow and
// masks the unit-test row.
func maskOrchRows(t *testing.T, stdout, repo, selfTestRow string) string {
	t.Helper()
	binary := orchBinaryRow.FindStringSubmatch(stdout)
	if binary == nil {
		t.Errorf("orch: no orch binary row in %q", stdout)
		return stdout
	}
	notOnPath := `WARN orch on PATH  —  not on PATH; agent commands "orch ..." will fail; add ` + filepath.Dir(binary[1]) + " to PATH\n"
	notRun := "WARN unit tests  —  not run (no go toolchain or module at " + filepath.Join(repo, "go") + ")"
	for _, want := range []string{notOnPath, selfTestRow, notRun} {
		if !strings.Contains(stdout, want) {
			t.Errorf("orch: no %q in %q", want, stdout)
		}
	}
	stdout = strings.Replace(stdout, notOnPath, "", 1)
	stdout = strings.Replace(stdout, selfTestRow, "", 1)
	return strings.Replace(stdout, notRun, "<unit tests>", 1)
}
