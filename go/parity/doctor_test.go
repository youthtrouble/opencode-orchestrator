//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	pathEnv := "PATH=" + h.pathWithoutGo(t)
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
}

const jsNoBinaryRow = `WARN orch binary  —  not found (runtime js only; see README "Go runtime")` + "\n"

// testDoctorNoBinary runs the JS without ORCH_BIN, with no ~/.local/bin/orch in the sandbox HOME
// and no orch on the harness PATH (fakes, node's and sqlite3's directories), so it warns; orch is
// always its own binary (PE-18), so its OK row is masked against that warning.
func (h *harness) testDoctorNoBinary(t *testing.T, healthy seed, pathEnv string) {
	for _, dir := range filepath.SplitList(strings.TrimPrefix(pathEnv, "PATH=")) {
		if _, err := os.Stat(filepath.Join(dir, "orch")); err == nil {
			t.Fatalf("harness PATH directory %s holds an orch; the JS would find it", dir)
		}
	}
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

// pathWithoutGo is the harness PATH with every directory that holds a go toolchain binary
// (a system go on Linux CI, Homebrew's bin on macOS) replaced by a temporary directory of
// symlinks to all of its other entries, so the fakes and both runtimes keep the same system
// tools but cannot find go. With a go, orch's unit tests row would run `go test` in the
// checkout under the sandbox HOME, its result, and whether a toolchain is downloaded into a
// read-only module cache, depending on the machine.
func (h *harness) pathWithoutGo(t *testing.T) string {
	t.Helper()
	var search []string
	for _, dir := range strings.Split(h.path, ":") {
		if holdsGoToolchain(dir) {
			dir = linkAllExceptGo(t, dir)
		}
		search = append(search, dir)
	}
	return strings.Join(search, ":")
}

var goToolchainBinaries = []string{"go", "gofmt"}

func holdsGoToolchain(dir string) bool {
	for _, name := range goToolchainBinaries {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// linkAllExceptGo returns a fresh directory of symlinks to every entry of dir but the go
// toolchain binaries.
func linkAllExceptGo(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	links := t.TempDir()
	for _, entry := range entries {
		if slices.Contains(goToolchainBinaries, entry.Name()) {
			continue
		}
		if err := os.Symlink(filepath.Join(dir, entry.Name()), filepath.Join(links, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	return links
}

var (
	jsUnitTestsRow     = regexp.MustCompile(`(?m)^OK   unit tests  —  pass \d+$`)
	jsUnitTestsFailRow = regexp.MustCompile(`(?m)^FAIL unit tests  —  fail [1-9]\d*$`)
	orchBinaryRow      = regexp.MustCompile(`(?m)^OK   orch binary  —  \S.* at /\S*\n`)
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

// maskOrchRows requires orch's rows (its binary, which compareOutcomes then compares, and PE-18's
// selfTestRow and Go unit-test row not run, there being no go on pathWithoutGo), removes
// selfTestRow and masks the unit-test row.
func maskOrchRows(t *testing.T, stdout, repo, selfTestRow string) string {
	t.Helper()
	notRun := "WARN unit tests  —  not run (no go toolchain or module at " + filepath.Join(repo, "go") + ")"
	for _, want := range []string{selfTestRow, notRun} {
		if !strings.Contains(stdout, want) {
			t.Errorf("orch: no %q in %q", want, stdout)
		}
	}
	if !orchBinaryRow.MatchString(stdout) {
		t.Errorf("orch: no orch binary row in %q", stdout)
	}
	stdout = strings.Replace(stdout, selfTestRow, "", 1)
	return strings.Replace(stdout, notRun, "<unit tests>", 1)
}
