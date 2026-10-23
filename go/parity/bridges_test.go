//go:build parity

package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const claudeFixtures = "../testdata/claude/stream-json"

// testBridges compares each `orch <bridge>-exec` with bin/<bridge>-exec.mjs, with the scripted
// fake standing in for the CLI (see parity/fakes).
func (h *harness) testBridges(t *testing.T) {
	h.testNestedProjectRoot(t, "bridges")
	t.Run("PE-42-artifact-root", func(t *testing.T) {
		for _, b := range []bridge{claudeBridge, codexBridge, opencodeBridge} {
			t.Run(b.name, func(t *testing.T) {
				// The frozen JS ignores this environment variable and writes in child.
				inv := h.bridgeInvocation(t, b, bridgeCase{args: []string{"--cwd", "child", "--prompt", "bounded", "--force"}, script: map[string]string{"exit": "0\n", "jsonl": ""}})
				inv.seed = seeds(inv.seed, seedProjectDir("child"))
				inv.env = append(inv.env, "ORCH_ARTIFACT_ROOT=$PROJECT/.orchestrator/reports")
				for _, runtime := range []string{"js", "orch"} {
					sb := h.newSandbox(t)
					inv.seed(t, sb)
					argv := inv.orch
					if runtime == "js" {
						argv = inv.js
					}
					o := h.run(t, sb, argv, inv.stdin, inv.env)
					// Without completion events claude/codex report failure even at exit 0;
					// all three still write a record, which is the behaviour under test.
					wantCode := 1
					if b.name == "opencode" {
						wantCode = 0
					}
					if o.exitCode != wantCode {
						t.Fatalf("%s exit %d: %s", runtime, o.exitCode, o.stderr)
					}
					root := sb.project
					if runtime == "js" {
						root = filepath.Join(root, "child")
					}
					files, err := filepath.Glob(filepath.Join(root, ".orchestrator", "runs", "*.json"))
					if err != nil || len(files) != 1 {
						t.Fatalf("%s records %v, %v", runtime, files, err)
					}
					if runtime == "orch" {
						if _, err := os.Stat(filepath.Join(sb.project, "child", ".orchestrator")); !os.IsNotExist(err) {
							t.Fatalf("child artifacts: %v", err)
						}
					}
				}
			})
		}
	})
	t.Run("claude", h.testClaudeExec)
	t.Run("codex", h.testCodexExec)
	t.Run("opencode", h.testOpencodeExec)
	t.Run("split-utf8", h.testBridgesSplitUTF8)
}

// bridge is what the harness needs to know of one bridge. Its name is the CLI it runs and
// names the script (<name>-exec.mjs), the subcommand, the run files and the fake's logs.
type bridge struct {
	name string
	// scriptDir is where, under $HOME, a case scripts the fake (see parity/fakes/<name>).
	scriptDir string
	// fixtures holds the captures a case may replay; "" when there are none yet.
	fixtures string
	// done matches the seconds in the "done" progress line; nil for a bridge without one.
	done *regexp.Regexp
	// fakeLogs are the fake's logs under fake-log/, dropped from a case whose CLI may be
	// signalled before it has written them.
	fakeLogs []string
}

var (
	claudeBridge = bridge{
		name:      "claude",
		scriptDir: "fake-claude",
		fixtures:  claudeFixtures,
		done:      regexp.MustCompile(`(?m)^(\[claude\] done ok=(?:true|false) turns=.* in )\d+(s; record )`),
		fakeLogs:  []string{"claude-argv.txt"},
	}
	codexBridge = bridge{
		name:      "codex",
		scriptDir: "fake-codex-exec",
		done:      regexp.MustCompile(`(?m)^(\[codex\] done ok=(?:true|false) in )\d+(s; record )`),
		fakeLogs:  []string{"codex-argv.txt", "codex-cwd.txt", "codex-env.txt", "codex-stdin.txt"},
	}
	opencodeBridge = bridge{
		name:      "opencode",
		scriptDir: "fake-opencode-run",
		fakeLogs:  []string{"opencode-argv.txt", "opencode-cwd.txt", "opencode-env.txt"},
	}
)

// progress is the prefix of the bridge's progress lines.
func (b bridge) progress() string { return "[" + b.name + "] " }

// bridgeCase is one bridge run. fixture names a capture in the bridge's fixtures that the fake
// replays with its recorded exit code; script adds or overrides fake files by extension
// (jsonl, stderr, stderr-head, exit, signal, sleep, and for codex nostdin). Unless usage names
// another cache fixture ("" is green, "none" seeds no cache), the usage cache is fresh, so the
// budget gate does not collect; only cases with codex set script the codex app-server fake.
type bridgeCase struct {
	name      string
	args      []string
	stdin     string
	fixture   string
	script    map[string]string
	usage     string
	codex     bool
	seed      seed
	env       []string
	crash     crashClass
	errorLine string
	normalize func(string) string
	// dropFakeLogs drops the fake's logs from the comparison, for a case whose CLI may be
	// signalled before it has written them.
	dropFakeLogs bool
	// help marks the --help case, whose JS header is compared as orch help (PE-19).
	help bool
}

// Fresh values, rewritten only in their exact place and format.
var (
	bridgeStartedAt = regexp.MustCompile(`("started_at": ?")` + isoMillis.String() + `(")`)
	bridgeDuration  = regexp.MustCompile(`("duration_ms": ?)\d+(,)`)
	runFileStamp    = regexp.MustCompile(`(^project/runs/|/\.orchestrator/runs/)\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z(-(?:claude|codex|opencode)-)`)
	// Node 26 warns when setTimeout clamps its delay; orch never prints Node warnings (PE-9).
	nodeTimerWarning = regexp.MustCompile(`(?m)^\(node:\d+\) Timeout(?:NaN|Negative|Overflow)Warning: .*\nTimeout duration was set to 1\.\n(?:\(Use ` + "`node --trace-warnings \\.\\.\\.`" + ` to show where the warning was created\)\n)?`)
)

// withoutNodeWarnings drops the timer warnings Node prints and orch does not (PE-9).
func withoutNodeWarnings(s string) string {
	return nodeTimerWarning.ReplaceAllString(s, "")
}

// normalize rewrites the fresh values of the bridge's runs.
func (b bridge) normalize(s string) string {
	s = bridgeStartedAt.ReplaceAllString(s, "${1}<iso>${2}")
	s = bridgeDuration.ReplaceAllString(s, "${1}<ms>${2}")
	s = runFileStamp.ReplaceAllString(s, "${1}<stamp>${2}")
	if b.done != nil {
		s = b.done.ReplaceAllString(s, "${1}<n>${2}")
	}
	return normalizeGeneratedAt(normalizeIncident(s))
}

// normalizeRunFileNames rewrites the started_at stamp in the names of run records and, with
// drop, leaves out the fake's logs.
func (b bridge) normalizeRunFileNames(drop bool) func(outcome, map[string]string) outcome {
	return func(o outcome, _ map[string]string) outcome {
		files := map[string]string{}
		for name, content := range o.files {
			if drop && slices.Contains(b.fakeLogs, strings.TrimPrefix(name, "fakelog/")) {
				continue
			}
			files[runFileStamp.ReplaceAllString(name, "${1}<stamp>${2}")] = content
		}
		o.files = files
		return o
	}
}

// noPostRunRefresh turns off orch's detached post-run usage refresh (PE-12) in every bridge
// case: it would write usage.json into the sandbox after the case's files are collected, and
// the JS refresh never completes. The JS ignores the variable.
const noPostRunRefresh = "ORCH_NO_POST_RUN_REFRESH=1"

func (h *harness) bridgeInvocation(t *testing.T, b bridge, c bridgeCase) invocation {
	t.Helper()
	caches := readCacheFixtures(t)
	var seedUsage seed = func(*testing.T, sandbox) {}
	switch c.usage {
	case "none":
	case "":
		green, _ := caches.Get("green")
		seedUsage = seedCache(green, time.Now())
	case "fable-red":
		seedUsage = seedCache(fableRed(t, caches), time.Now())
	case "anthropic-red":
		seedUsage = seedCache(anthropicRed(t, caches), time.Now())
	default:
		fixture, ok := caches.Get(c.usage)
		if !ok {
			t.Fatalf("no cache fixture %q", c.usage)
		}
		seedUsage = seedCache(fixture, time.Now())
	}
	all := []seed{seedUsage, seedFile("fake-log/.keep", ""), h.seedFake(t, b, c)}
	if c.codex {
		all = append(all, codexAnswers(t))
	}
	if c.seed != nil {
		all = append(all, c.seed)
	}
	normalize := b.normalize
	if c.normalize != nil {
		normalize = func(s string) string { return c.normalize(b.normalize(s)) }
	}
	return invocation{
		js:              append([]string{h.node, filepath.Join(h.orchJS, "bin", b.name+"-exec.mjs")}, c.args...),
		orch:            append([]string{h.orchBin, b.name + "-exec"}, c.args...),
		stdin:           []byte(c.stdin),
		env:             append([]string{noPostRunRefresh}, c.env...),
		seed:            seeds(all...),
		normalize:       normalize,
		crash:           c.crash,
		errorLine:       c.errorLine,
		progress:        b.progress(),
		projectPaths:    true,
		normalizeWrites: b.normalizeRunFileNames(c.dropFakeLogs),
		jsHelp:          c.help,
	}
}

// seedFake scripts the bridge's fake for the case, if it has a fixture or a script.
func (h *harness) seedFake(t *testing.T, b bridge, c bridgeCase) seed {
	t.Helper()
	files := map[string]string{}
	if c.fixture != "" {
		for _, ext := range []string{"jsonl", "stderr"} {
			data, err := os.ReadFile(filepath.Join(b.fixtures, c.fixture+"."+ext))
			if err != nil {
				t.Fatal(err)
			}
			files[ext] = string(data)
		}
		files["exit"] = fmt.Sprintf("%v\n", jsjson.Path(readFixtureCases(t, b.fixtures), c.fixture, "exit_code"))
	}
	for ext, content := range c.script {
		files[ext] = content
	}
	if len(files) == 0 {
		return func(*testing.T, sandbox) {}
	}
	return func(t *testing.T, sb sandbox) {
		writeSandboxFile(t, sb, b.scriptDir+"/case", "c\n")
		for ext, content := range files {
			writeSandboxFile(t, sb, b.scriptDir+"/c."+ext, content)
		}
	}
}

// readFixtureCases reads the cases.json beside a bridge's captures.
func readFixtureCases(t *testing.T, fixtures string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return cases
}

// fableRed is the green cache with Fable's weekly window critical: Anthropic is green overall
// and red for frontier (fable) models only.
func fableRed(t *testing.T, caches *jsjson.Object) *jsjson.Object {
	t.Helper()
	green, _ := caches.Get("green")
	data, err := jsjson.Marshal(green)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	fable := jsjson.Path(copied, "claude", "weekly_by_model", "fable").(*jsjson.Object)
	fable.Set("pct", 99.0)
	fable.Set("severity", "critical")
	return copied.(*jsjson.Object)
}

// anthropicRed is the green cache with the red cache's Claude reading: Anthropic is red,
// OpenAI green.
func anthropicRed(t *testing.T, caches *jsjson.Object) *jsjson.Object {
	t.Helper()
	green, _ := caches.Get("green")
	red, _ := caches.Get("red")
	copied := jsjson.Spread(green)
	copied.Set("claude", jsjson.Path(red, "claude"))
	return copied
}

// pathWithout is a PATH of the fakes except name, then the harness's node and sqlite3 links, so
// a lookup of name fails. It never holds a real claude, codex or opencode.
func (h *harness) pathWithout(t *testing.T, name string, before ...string) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(h.fakesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != name {
			if err := os.Symlink(filepath.Join(h.fakesDir, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	search := append(append([]string{}, before...), dir, h.toolsDir)
	for _, entry := range search[len(before):] {
		for _, cli := range []string{"claude", "codex", "opencode"} {
			if _, err := os.Stat(filepath.Join(entry, cli)); err == nil && entry != dir {
				t.Fatalf("%s holds a %s; refusing to run a spawn-error case that could reach it", entry, cli)
			}
		}
	}
	return strings.Join(search, ":")
}

func (h *harness) testClaudeExec(t *testing.T) {
	ok := []string{"--model", "claude-haiku-5-5", "--permission-mode", "dontAsk", "--allowed-tools", "Read", "--max-turns", "3", "--prompt", "Read README.md and reply with its first heading only."}
	nonExecutable := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonExecutable, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const (
		pathArgBoolean = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received type boolean (true)`
		nullEvent      = "TypeError: Cannot read properties of null (reading 'session_id')"
		nullBlock      = "TypeError: Cannot read properties of null (reading 'type')"
	)
	init := `{"type":"system","subtype":"init","session_id":"s1","model":"m"}` + "\n"
	cases := []bridgeCase{
		// Every captured and synthetic fixture.
		{name: "fixture/ok", args: ok, fixture: "ok"},
		{name: "fixture/bash-readonly-allowed", args: ok, fixture: "bash-readonly-allowed"},
		{name: "fixture/permission-denied", args: ok, fixture: "permission-denied"},
		{name: "fixture/max-turns", args: ok, fixture: "max-turns"},
		{name: "fixture/structured-output", args: ok, fixture: "structured-output"},
		{name: "fixture/bad-model", args: ok, fixture: "bad-model"},
		{name: "fixture/long-line", args: ok, fixture: "long-line"},
		{name: "fixture/success-is-error", args: ok, fixture: "success-is-error"},
		{name: "fixture/usage-limit", args: ok, fixture: "usage-limit", codex: true},
		// A file where the state directory belongs: the gate proceeds without usage, the refresh
		// throws (no resets_at) and the incident log cannot be written.
		{name: "usage-limit/refresh-fails", args: ok, fixture: "usage-limit", usage: "none", seed: seeds(seedFile(".local/state/opencode/orchestrator", "not a directory"), seedCodex(map[int]string{
			1: `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"parity"}}`,
			3: `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"not logged in"}}`,
		}))},

		// Every flag, and how each argument is passed.
		{name: "flags/all", fixture: "ok", seed: seeds(seedProjectFile("schema.json", "{\"type\":\"object\"}\n\xff\ufeff")), args: []string{
			"--model", "claude-sonnet-5-5", "--effort", "high", "--permission-mode", "plan", "--allowed-tools", "Read,Edit,Bash(go *)",
			"--disallowed-tools", "WebFetch", "--max-turns", "07", "--max-budget-usd", "1.50", "--agent", "reviewer",
			"--append-system-prompt", "be brief\nplease", "--json-schema", "schema.json", "--add-dir", "../x/./y", "--timeout-min", "5",
			"--force", "--prompt", "multi\nline \u00e9 prompt", "ignored positional"}},
		{name: "flags/resume", args: []string{"--resume", "abcdef1234", "--model", "haiku", "--prompt", "p"}, fixture: "ok"},
		{name: "flags/empty-values", args: []string{"--model", "", "--effort", "", "--permission-mode", "", "--resume", "", "--max-turns", "", "--cwd", "", "--prompt", "p"}, fixture: "ok"},
		{name: "flags/cwd", args: []string{"--cwd", "sub/../sub/", "--prompt", "p"}, fixture: "max-turns", seed: seedProjectFile("sub/.keep", "")},
		{name: "flags/unknown-keys-are-flags", args: []string{"--frobnicate", "x", "--prompt", "p"}, fixture: "ok"},
		{name: "quiet", args: append([]string{"--quiet"}, ok...), fixture: "max-turns"},
		{name: "help", args: []string{"--prompt", "p", "--help"}, help: true},

		// Prompt sources.
		{name: "prompt/positional", args: []string{"fix", "the", "bug"}, fixture: "ok"},
		{name: "prompt/file", args: []string{"--prompt-file", "brief.md", "--prompt", "loses"}, fixture: "ok", seed: seedProjectFile("brief.md", "from file \xff\n"+strings.Repeat("ab", 300))},
		{name: "prompt/stdin", stdin: "from stdin\n", fixture: "ok"},
		{name: "prompt/empty-stdin", fixture: "ok"},
		{name: "prompt/file-missing", args: []string{"--prompt-file", "absent.md"}},
		{name: "prompt/file-is-directory", args: []string{"--prompt-file", "."}},
		{name: "prompt/file-unreadable", args: []string{"--prompt-file", "secret.md"}, seed: seedUnreadable("secret.md")},
		{name: "prompt/file-bare", args: []string{"--prompt-file"}},

		// The budget gate.
		{name: "gate/red-refuses", args: []string{"--prompt", "p"}, usage: "red"},
		{name: "gate/red-forced", args: []string{"--force", "--prompt", "p"}, usage: "red", fixture: "ok"},
		{name: "gate/fable-red-refuses-fable", args: []string{"--model", "claude-FABLE-5-1", "--prompt", "p"}, usage: "fable-red"},
		{name: "gate/fable-red-allows-opus", args: []string{"--prompt", "p"}, usage: "fable-red", fixture: "ok"},
		{name: "gate/unknown-usage", args: []string{"--prompt", "p"}, usage: "unknown", fixture: "ok"},
		{name: "gate/codex-spawn-fails", args: []string{"--prompt", "p"}, usage: "none", env: []string{"PATH=" + h.pathWithout(t, "codex")}, crash: crashC1, errorLine: "Error: spawn codex ENOENT"},

		// How claude ends.
		{name: "end/killed-by-signal", args: ok, fixture: "ok", script: map[string]string{"signal": "TERM\n"}},
		{name: "end/stderr-tail", args: ok, script: map[string]string{"stderr": "\u00a0first\nsecond\r\nthird\n\nfifth \n\t\n", "exit": "2\n"}},
		{name: "end/no-output", args: ok, script: map[string]string{"exit": "0\n"}},
		{name: "PE-9/timeout-0", args: []string{"--timeout-min", "0", "--prompt", "p"}, script: map[string]string{"sleep": "30\n"}, dropFakeLogs: true, normalize: withoutNodeWarnings},
		{name: "PE-9/timeout-negative-warns-in-node", args: []string{"--timeout-min", "-1", "--prompt", "p"}, script: map[string]string{"sleep": "30\n"}, dropFakeLogs: true, normalize: withoutNodeWarnings},
		{name: "end/limit-in-errors-is-a-limit-incident", args: ok, script: map[string]string{"jsonl": `{"type":"result","is_error":true,"subtype":"rate_limited"}` + "\n", "exit": "1\n"}},

		// A trailing value key without a value is the JS value true.
		{name: "bare/model", args: []string{"--prompt", "p", "--model"}, fixture: "ok"},
		{name: "bare/effort", args: []string{"--prompt", "p", "--effort"}, fixture: "max-turns"},
		{name: "bare/permission-mode", args: []string{"--prompt", "p", "--permission-mode"}, fixture: "ok"},
		{name: "bare/allowed-tools", args: []string{"--prompt", "p", "--allowed-tools"}, fixture: "ok"},
		{name: "bare/disallowed-tools", args: []string{"--prompt", "p", "--disallowed-tools"}, fixture: "ok"},
		{name: "bare/agent", args: []string{"--prompt", "p", "--agent"}, fixture: "ok"},
		{name: "bare/append-system-prompt", args: []string{"--prompt", "p", "--append-system-prompt"}, fixture: "ok"},
		{name: "bare/max-turns", args: []string{"--prompt", "p", "--max-turns"}, fixture: "ok"},
		{name: "bare/max-budget-usd", args: []string{"--prompt", "p", "--max-budget-usd"}, fixture: "ok"},
		{name: "bare/timeout-min", args: []string{"--prompt", "p", "--timeout-min"}, fixture: "ok"},
		{name: "bare/resume-on-the-limit-path", args: []string{"--prompt", "p", "--resume"}, fixture: "usage-limit", codex: true},
		{name: "bare/resume-crashes-in-the-run-record", args: []string{"--prompt", "p", "--resume"}, fixture: "ok", crash: crashC2},
		{name: "bare/cwd", args: []string{"--prompt", "p", "--cwd"}, crash: crashC1, errorLine: pathArgBoolean},
		{name: "bare/cwd-before-the-gate", args: []string{"--prompt", "p", "--cwd"}, usage: "red", crash: crashC1, errorLine: pathArgBoolean},
		{name: "bare/json-schema", args: []string{"--prompt", "p", "--json-schema"}, crash: crashC1, errorLine: pathArgBoolean},
		{name: "bare/json-schema-after-the-gate", args: []string{"--prompt", "p", "--json-schema"}, usage: "red"},
		{name: "bare/add-dir", args: []string{"--prompt", "p", "--add-dir"}, crash: crashC1, errorLine: pathArgBoolean},

		// Other crashes.
		{name: "crash/json-schema-missing", args: []string{"--json-schema", "absent.json", "--prompt", "p"}, crash: crashC1, errorLine: "Error: ENOENT: no such file or directory, open '$PROJECT/absent.json'"},
		{name: "crash/spawn-enoent", args: []string{"--prompt", "p"}, env: []string{"PATH=" + h.pathWithout(t, "claude")}, crash: crashC1, errorLine: "Error: spawn claude ENOENT"},
		{name: "crash/spawn-eacces", args: []string{"--prompt", "p"}, env: []string{"PATH=" + h.pathWithout(t, "claude", nonExecutable)}, crash: crashC1, errorLine: "Error: spawn claude EACCES"},
		{name: "crash/cwd-missing", args: []string{"--cwd", "absent", "--prompt", "p"}, crash: crashC1, errorLine: "Error: spawn claude ENOENT"},
		{name: "crash/null-byte-in-the-prompt", stdin: "a\x00b", fixture: "ok", crash: crashC1, errorLine: `TypeError [ERR_INVALID_ARG_VALUE]: The argument 'args[11]' must be a string without null bytes. Received 'a\x00b'`},
		{name: "crash/null-event", args: ok, script: map[string]string{"jsonl": init + "null\n" + init, "exit": "0\n"}, crash: crashC1, errorLine: nullEvent},
		{name: "crash/null-block", args: ok, script: map[string]string{"jsonl": init + `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"},null]}}` + "\n", "exit": "0\n"}, crash: crashC1, errorLine: nullBlock},
		{name: "crash/content-not-iterable", args: ok, script: map[string]string{"jsonl": init + `{"type":"assistant","message":{"content":5}}` + "\n", "exit": "0\n"}, crash: crashC2},
		{name: "crash/text-not-a-string", args: ok, script: map[string]string{"jsonl": `{"type":"assistant","message":{"content":[{"type":"text","text":7}]}}` + "\n", "exit": "0\n"}, crash: crashC2},
		{name: "crash/result-not-a-string", args: ok, script: map[string]string{"jsonl": `{"type":"result","is_error":true,"result":{"a":1}}` + "\n", "exit": "1\n"}, crash: crashC2},

		// Event shapes that do not crash.
		// readline also ends lines on raw U+2028 and U+2029 (JSON.stringify leaves them raw), so
		// both events split into fragments that do not parse: no message line, and no result.
		{name: "events/raw-line-separators", args: ok, script: map[string]string{"exit": "0\n", "jsonl": init +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"a` + "\u2028" + `b"}]}}` + "\n" +
			`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"x` + "\u2029" + `y"}` + "\n"}},
		{name: "events/odd-shapes", args: ok, script: map[string]string{"exit": "0\n", "jsonl": strings.Join([]string{
			"not json", "5", `"s"`, "[1]", `{"session_id":0}`, `  {"type":"system","subtype":"init","session_id":["a","b"],"model":{"x":1}}  `,
			`{"type":"assistant","message":{"content":"text"}}`, `{"type":"assistant","message":"m"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":""},5,"s",[1],{"type":"thinking"},{"type":"text","text":"a\nb\rc` + strings.Repeat("\U0001F600", 80) + `"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":5},{"type":"tool_use","name":null},{"type":"tool_use","name":"__proto__"},{"type":"tool_use"},{"type":"tool_use","name":{}},{"type":"tool_use","name":"toString"},{"type":"tool_use","name":"toString"},{"type":"tool_use","name":"constructor"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + strings.Repeat("x", 120) + `"}},{"type":"tool_use","name":"Read","input":{"command":"","file_path":{"a":1}}},{"type":"tool_use","name":"T","input":"s"}]}}`,
			`{"type":"result","is_error":true,"subtype":"error_a","num_turns":1,"total_cost_usd":0}`,
			`{"type":"result","is_error":1,"subtype":"success","result":"` + strings.Repeat("\u00e9", 210) + `"}`,
			`{"type":"result","is_error":true,"result":[1,null],"usage":0,"modelUsage":{"m":1},"num_turns":false,"permission_denials":null,"structured_output":false}`,
		}, "\n") + "\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.comparePair(t, h.bridgeInvocation(t, claudeBridge, c))
		})
	}
	t.Run("PE-8/bare-prompt", func(t *testing.T) { h.testBarePrompt(t) })
	t.Run("PE-1/cuts-split-surrogates", func(t *testing.T) { h.testClaudeSplitSurrogates(t) })
}

// seedUnreadable writes a project file nobody may read.
func seedUnreadable(rel string) seed {
	return func(t *testing.T, sb sandbox) {
		writeSeedFile(t, filepath.Join(sb.project, rel), "secret")
		if err := os.Chmod(filepath.Join(sb.project, rel), 0o000); err != nil {
			t.Fatal(err)
		}
	}
}

// testBarePrompt asserts PE-8: a trailing bare --prompt makes the JS run claude on the prompt
// "true" (and then crash hashing it), where orch refuses before the gate and never starts
// claude.
func (h *harness) testBarePrompt(t *testing.T) {
	js, orch := h.runPair(t, h.bridgeInvocation(t, claudeBridge, bridgeCase{args: []string{"--prompt"}, fixture: "ok"}))
	if js.exitCode != 1 || !strings.HasSuffix(js.files["fakelog/claude-argv.txt"], "\n--\ntrue\n") || !strings.Contains(js.stderr, "TypeError [ERR_INVALID_ARG_TYPE]: The \"data\" argument") {
		t.Errorf("js: exit %d, argv log %q, stderr:\n%s", js.exitCode, js.files["fakelog/claude-argv.txt"], js.stderr)
	}
	if _, started := orch.files["fakelog/claude-argv.txt"]; orch.exitCode != 2 || orch.stdout != "" || orch.stderr != "no prompt: --prompt needs a value\n" || started {
		t.Errorf("orch: exit %d, stdout %q, stderr %q, claude started %v", orch.exitCode, orch.stdout, orch.stderr, started)
	}
}

func TestCrashAfterProgressComparator(t *testing.T) {
	const expected = "TypeError: Cannot read properties of null (reading 'session_id')"
	js := outcome{exitCode: 1, stderr: "[claude] start\n[claude] session s\nfile:///x.mjs:61\n  if (ev.session_id\n\n" + expected + "\n    at x\n\nNode.js v26\n"}
	good := outcome{exitCode: 1, stderr: "[claude] start\n[claude] session s\n" + expected + "\n"}
	if err := crashAfterProgressError(crashC1, expected, "[claude] ", js, good); err != nil {
		t.Fatal(err)
	}
	for _, stderr := range []string{
		expected + "\n",                                                 // progress missing
		"[claude] start\n" + expected + "\n",                            // JS printed progress orch did not
		"[claude] start\n[claude] other\n" + expected + "\n",            // different progress
		"[claude] start\nnoise\n[claude] session s\n" + expected + "\n", // non-progress line
		"[claude] start\n[claude] session s\nError: other\n",
	} {
		bad := good
		bad.stderr = stderr
		if crashAfterProgressError(crashC1, expected, "[claude] ", js, bad) == nil {
			t.Errorf("accepted stderr %q", stderr)
		}
	}
}

func TestClaudeExecNormalizer(t *testing.T) {
	in := `{"started_at":"2026-10-19T04:11:53.274Z","duration_ms":306,"record":"$PROJECT/.orchestrator/runs/2026-10-19T04-11-53-274Z-claude-00000000.json"}` + "\n" +
		"[claude] done ok=false turns=null in 12s; record $PROJECT/.orchestrator/runs/2026-10-19T04-11-53-274Z-claude-run.json\n" +
		"(node:123) TimeoutNegativeWarning: -60000 is a negative number.\nTimeout duration was set to 1.\n(Use `node --trace-warnings ...` to show where the warning was created)\n" +
		`  "started_at": "2026-10-19T04:11:53.274Z",` + "\n" + `  "duration_ms": 7,` + "\n"
	want := `{"started_at":"<iso>","duration_ms":<ms>,"record":"$PROJECT/.orchestrator/runs/<stamp>-claude-00000000.json"}` + "\n" +
		"[claude] done ok=false turns=null in <n>s; record $PROJECT/.orchestrator/runs/<stamp>-claude-run.json\n" +
		"(node:123) TimeoutNegativeWarning: -60000 is a negative number.\nTimeout duration was set to 1.\n(Use `node --trace-warnings ...` to show where the warning was created)\n" +
		`  "started_at": "<iso>",` + "\n" + `  "duration_ms": <ms>,` + "\n"
	if got := claudeBridge.normalize(in); got != want {
		t.Errorf("claudeBridge.normalize\n got %q\nwant %q", got, want)
	}
	if got := nodeTimerWarning.ReplaceAllString(want, ""); strings.Contains(got, "Warning") {
		t.Errorf("warning not stripped: %q", got)
	}
}

// testClaudeSplitSurrogates asserts PE-1 for claude-exec's three UTF-16 cuts that reach JSON:
// the 200-unit error text, the 160-unit incident summary and the 400-unit prompt_head, each
// cut inside an emoji. The JS writes the lone high surrogate as "\ud83d", orch U+FFFD; with
// that escape replaced, stdout, the run record and the incident are identical.
func (h *harness) testClaudeSplitSurrogates(t *testing.T) {
	const emoji, lone = "\U0001F600", `\ud83d`
	cutError := strings.Repeat("b", 199) + lone + `"`
	events := `{"type":"result","is_error":true,"subtype":"` + strings.Repeat("c", 159) + emoji + `"}` + "\n" +
		`{"type":"result","is_error":true,"subtype":"success","result":"` + strings.Repeat("b", 199) + emoji + `"}` + "\n"
	prompt := strings.Repeat("p", 399) + emoji
	js, orch := h.runPair(t, h.bridgeInvocation(t, claudeBridge, bridgeCase{args: []string{"--prompt", prompt}, script: map[string]string{"jsonl": events, "exit": "0\n"}}))
	observe := func(where, got string, wants ...string) string {
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("PE-1 not observed: js %s lacks %q:\n%s", where, want, got)
			}
		}
		return strings.ReplaceAll(got, lone, "\ufffd")
	}
	js.stdout = observe("stdout", js.stdout, cutError)
	const incidents = "state/incidents.jsonl"
	js.files[incidents] = observe(incidents, js.files[incidents], `"summary":"claude bridge failed: `+strings.Repeat("c", 159)+lone+`"`, cutError)
	record := runRecordFile(t, js.files)
	js.files[record] = observe(record, js.files[record], cutError, `"prompt_head": "`+strings.Repeat("p", 399)+lone+`"`)
	compareOutcomes(t, js, orch)
}

// runRecordFile is the name of the one run record among files.
func runRecordFile(t *testing.T, files map[string]string) string {
	t.Helper()
	var records []string
	for name := range files {
		if strings.HasPrefix(name, "project/runs/") {
			records = append(records, name)
		}
	}
	if len(records) != 1 {
		t.Fatalf("want one run record, have %q", records)
	}
	return records[0]
}
