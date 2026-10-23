package bridge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// runBridgeEnv names the bridge the test binary acts as (`orch claude-exec` for "claude", and
// likewise "codex" and "opencode"), so the run flow, its exit code and its signal handling are
// tested in a process of their own. The post-run refresh relaunches that binary as `usage
// --refresh`, which then acts as fakeUsageRefresh.
const runBridgeEnv = "ORCH_BRIDGE_TEST_RUN"

var bridgeRuns = map[string]func(args []string, stdin *os.File, stdout, stderr io.Writer) int{
	"claude":   RunClaude,
	"codex":    RunCodex,
	"opencode": RunOpencode,
}

// refreshReportEnv names the file fakeUsageRefresh reports to; without it the fake exits at once.
const refreshReportEnv = "ORCH_BRIDGE_TEST_REFRESH_REPORT"

const envDumpEnv = "ORCH_BRIDGE_TEST_ENV_DUMP"

func TestMain(m *testing.M) {
	if report := os.Getenv(envDumpEnv); report != "" && filepath.Base(os.Args[0]) == "claude" {
		cwd, err := syscall.Getwd()
		if err != nil {
			os.Exit(1)
		}
		data := strings.Join(append(os.Environ(), "CWD="+cwd), "\n") + "\n"
		if err := os.WriteFile(report, []byte(data), 0o600); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if run, isBridge := bridgeRuns[os.Getenv(runBridgeEnv)]; isBridge {
		if len(os.Args) > 1 && os.Args[1] == "usage" {
			os.Exit(fakeUsageRefresh(os.Args[1:]))
		}
		os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestRunClaudeChildPWDIsTheProject(t *testing.T) {
	stale := t.TempDir()
	t.Setenv("PWD", stale)
	t.Setenv("CLAUDECODE", "1")
	sb := newClaudeSandbox(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(sb.bin, "claude")); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(sb.home, "child-env")
	cmd := sb.command("--prompt", "hello")
	cmd.Env = append(cmd.Env, "PWD="+stale, envDumpEnv+"="+report)
	// An eventless fake need not produce a successful bridge result; only its environment matters.
	_ = exitCode(t, cmd.Run())
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var pwd []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PWD=") {
			pwd = append(pwd, line)
		}
		if strings.HasPrefix(line, "CLAUDECODE=") {
			t.Errorf("nested Claude marker leaked: %s", line)
		}
	}
	if len(pwd) != 1 || pwd[0] != "PWD="+sb.project {
		t.Errorf("PWD entries = %q, want PWD=%s", pwd, sb.project)
	}
	if !strings.Contains(string(data), "CWD="+sb.project+"\n") {
		t.Errorf("physical cwd missing from dump: %s", data)
	}
}

// fakeUsageRefresh stands in for `orch usage --refresh`: it reports its argv, pid and process
// group (Setsid makes it the group's leader), then lingers (at most 30 s, so a failed test
// leaves nothing running for long) until killed.
func fakeUsageRefresh(args []string) int {
	report := os.Getenv(refreshReportEnv)
	if report == "" {
		return 0
	}
	line := fmt.Sprintf("%s|%d|%d\n", strings.Join(args, " "), os.Getpid(), syscall.Getpgrp())
	if os.WriteFile(report+".tmp", []byte(line), 0o600) != nil || os.Rename(report+".tmp", report) != nil {
		return 1
	}
	time.Sleep(30 * time.Second)
	return 0
}

// bridgeSandbox is a HOME, a project and a PATH holding only the fake CLI a test installs and
// the parity codex fake, for the bridge the test binary acts as. The registry is missing, so
// the budget gate proceeds without collecting usage.
type bridgeSandbox struct {
	bridge, home, project, bin string
}

func newClaudeSandbox(t *testing.T) bridgeSandbox {
	return newBridgeSandbox(t, "claude")
}

func newBridgeSandbox(t *testing.T, bridge string) bridgeSandbox {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb := bridgeSandbox{bridge: bridge, home: filepath.Join(root, "home"), project: filepath.Join(root, "project"), bin: filepath.Join(root, "bin")}
	for _, dir := range []string{sb.home, sb.project, sb.bin, filepath.Join(sb.home, "fake-log")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	codex, err := filepath.Abs(filepath.Join("..", "..", "parity", "fakes", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codex, filepath.Join(sb.bin, "codex")); err != nil {
		t.Fatal(err)
	}
	return sb
}

// fakeClaude installs a claude that records its environment, prints stdout and stderr, then
// runs ending (e.g. "exit 0", or "exec /bin/sleep 30" to stay alive until signalled).
func (sb bridgeSandbox) fakeClaude(t *testing.T, stdout, stderr, ending string) {
	t.Helper()
	sb.fakeCLI(t, "claude", stdout, stderr, ending)
}

// fakeCLI installs name as a fake that records its environment, prints stdout and stderr, then
// runs ending; it replaces the parity codex fake for name "codex".
func (sb bridgeSandbox) fakeCLI(t *testing.T, name, stdout, stderr, ending string) {
	t.Helper()
	write(t, filepath.Join(sb.home, "stdout"), stdout)
	write(t, filepath.Join(sb.home, "stderr"), stderr)
	script := "#!/bin/sh\n" +
		`printf '%s|%s\n' "${CLAUDECODE-unset}" "${CLAUDECODE_X-unset}" > "$HOME/env"` + "\n" +
		`/bin/cat "$HOME/stdout"` + "\n" +
		`/bin/cat "$HOME/stderr" >&2` + "\n" +
		ending + "\n"
	file := filepath.Join(sb.bin, name)
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (sb bridgeSandbox) command(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = sb.project
	cmd.Env = []string{
		runBridgeEnv + "=" + sb.bridge,
		"HOME=" + sb.home,
		"PATH=" + sb.bin,
		"ORCH_HOME=" + sb.home,
		"ORCH_REGISTRY=" + filepath.Join(sb.home, "missing-models.json"),
		"CLAUDECODE=1",
		"CLAUDECODE_X=kept",
	}
	return cmd
}

func (sb bridgeSandbox) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := sb.command(args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	return exitCode(t, cmd.Run()), out.String(), errOut.String()
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

func parseResult(t *testing.T, stdout string) *jsjson.Object {
	t.Helper()
	value, err := jsjson.Unmarshal([]byte(stdout))
	if err != nil {
		t.Fatalf("stdout is not one JSON value: %v\n%s", err, stdout)
	}
	return value.(*jsjson.Object)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claude", "stream-json", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (sb bridgeSandbox) incidents(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sb.home, ".local", "state", "opencode", "orchestrator", "incidents.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// doneLine is the "done" line up to the record path; the seconds depend on the machine's load.
var doneLine = regexp.MustCompile(`^\[claude\] done ok=true turns=2 in \d+s; record /`)

func TestRunClaudeNormalPath(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, readFixture(t, "ok.jsonl"), readFixture(t, "ok.stderr"), "exit 0")
	code, stdout, stderr := sb.run(t, "--prompt", "hello")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	result := parseResult(t, stdout)
	keys := strings.Join(result.Keys(), ",")
	if keys != "ok,harness,session_id,model,effort,cwd,started_at,final_message,usage,model_usage,cost_usd,num_turns,permission_denials,tools_used,errors,structured_output,exit_code,duration_ms,record" {
		t.Errorf("result keys %s", keys)
	}
	record := jsjson.Path(result, "record").(string)
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(record, filepath.Join(sb.project, ".orchestrator", "runs")+"/") ||
		!strings.Contains(string(data), `"prompt_sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",`) ||
		!strings.HasSuffix(string(data), "\"prompt_head\": \"hello\"\n}\n") {
		t.Errorf("record %s:\n%s", record, data)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "[claude] start model=claude-opus-5-5 mode=acceptEdits cwd="+sb.project) ||
		!doneLine.MatchString(lines[4]) || !strings.HasSuffix(lines[4], "s; record "+record) {
		t.Errorf("stderr:\n%s", stderr)
	}
	if env, _ := os.ReadFile(filepath.Join(sb.home, "env")); string(env) != "unset|kept\n" {
		t.Errorf("claude saw CLAUDECODE|CLAUDECODE_X = %q, want only CLAUDECODE removed", env)
	}
	if sb.incidents(t) != "" {
		t.Error("a successful run recorded an incident")
	}
}

// The post-run refresh is `orch usage --refresh`, started in a session of its own once the
// run record is written; orch exits while it still runs (PE-12).
func TestRunClaudeStartsADetachedUsageRefresh(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, readFixture(t, "ok.jsonl"), "", "exit 0")
	report := filepath.Join(sb.home, "refresh")
	cmd := sb.command("--quiet", "--prompt", "p")
	cmd.Env = append(cmd.Env, refreshReportEnv+"="+report)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	started := time.Now()
	if code := exitCode(t, cmd.Run()); code != 0 {
		t.Fatalf("exit %d", code)
	}
	// The fake refresh lingers 30 s: waiting for it, or for a pipe it held, would show here.
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("orch took %v; it must not wait for the refresh", elapsed)
	}
	parseResult(t, stdout.String())
	fields := strings.Split(strings.TrimSpace(waitForFile(t, report, 5*time.Second)), "|")
	if len(fields) != 3 || fields[0] != "usage --refresh" {
		t.Fatalf("refresh reported %q", fields)
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if fields[2] != fields[1] {
		t.Errorf("refresh process group %s, want its own group %s", fields[2], fields[1])
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("refresh is not running after orch exited (orch waited for it?): %v", err)
	}
}

func TestRunClaudeRefreshCanBeTurnedOff(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, readFixture(t, "ok.jsonl"), "", "exit 0")
	report := filepath.Join(sb.home, "refresh")
	cmd := sb.command("--quiet", "--prompt", "p")
	cmd.Env = append(cmd.Env, refreshReportEnv+"="+report, noPostRunRefreshEnv+"=1")
	if code := exitCode(t, cmd.Run()); code != 0 {
		t.Fatalf("exit %d", code)
	}
	time.Sleep(500 * time.Millisecond) // a refresh, had one started, reports well within this
	if _, err := os.Stat(report); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refresh ran with %s=1 (%v)", noPostRunRefreshEnv, err)
	}
}

// A bare --resume makes session_id true, which the run record throws on; the JS dies there,
// before its refresh, so orch starts none.
func TestRunClaudeStartsNoRefreshAfterARunRecordCrash(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, readFixture(t, "ok.jsonl"), "", "exit 0")
	assertNoRefresh(t, sb.home, sb.command("--quiet", "--prompt", "p", "--resume"))
}

// assertNoRefresh runs cmd, which must crash in its run record, and fails if it starts a
// post-run refresh.
func assertNoRefresh(t *testing.T, home string, cmd *exec.Cmd) {
	t.Helper()
	report := filepath.Join(home, "refresh")
	cmd.Env = append(cmd.Env, refreshReportEnv+"="+report)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if code := exitCode(t, cmd.Run()); code != 1 || !strings.HasPrefix(stderr.String(), "TypeError: ") {
		t.Fatalf("exit %d, stderr %q; want the run record's TypeError", code, stderr.String())
	}
	time.Sleep(500 * time.Millisecond) // a refresh, had one started, reports well within this
	if _, err := os.Stat(report); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refresh ran after the run record crashed (%v)", err)
	}
}

// waitForFile returns the content of path once it exists, failing after limit.
func waitForFile(t *testing.T, path string, limit time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within %v: %v", path, limit, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunClaudeFailedRunRecordsAnIncident(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, "", "warming up\nline 2\n\nfatal: x\n  ", "exit 3")
	code, stdout, stderr := sb.run(t, "--quiet", "--model", "haiku", "do", "it")
	if code != 1 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	result := parseResult(t, stdout)
	if got := jsjson.Path(result, "errors").([]any); len(got) != 1 || got[0] != "claude exited 3: line 2 |  | fatal: x" {
		t.Errorf("errors %q", got)
	}
	if jsjson.Path(result, "exit_code") != 3.0 || jsjson.Path(result, "ok") != false {
		t.Errorf("exit_code %v ok %v", jsjson.Path(result, "exit_code"), jsjson.Path(result, "ok"))
	}
	incidents := sb.incidents(t)
	for _, want := range []string{`"component":"bridge","kind":"crash","summary":"claude bridge failed: claude exited 3: line 2 |  | fatal: x"`, `"context":{"model":"haiku","exit_code":3},"project":"project"`} {
		if !strings.Contains(incidents, want) {
			t.Errorf("incident lacks %s:\n%s", want, incidents)
		}
	}
}

func TestRunClaudeUsageLimitPath(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, readFixture(t, "usage-limit.jsonl"), "", "exit 1")
	// The awaited refresh talks to the codex fake: initialize, then an error for account/read.
	for n, reply := range map[string]string{
		"1": `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"test"}}`,
		"3": `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"not logged in"}}`,
	} {
		if err := os.MkdirAll(filepath.Join(sb.home, "fake-codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(sb.home, "fake-codex", n), reply+"\n")
	}
	code, stdout, _ := sb.run(t, "--prompt", "p")
	if code != 75 {
		t.Fatalf("exit %d", code)
	}
	result := parseResult(t, stdout)
	keys := strings.Join(result.Keys()[16:], ",")
	if keys != "exit_code,duration_ms,reason,resets_at" || jsjson.Path(result, "reason") != "budget_red" {
		t.Errorf("appended keys %s, reason %v", keys, jsjson.Path(result, "reason"))
	}
	if errs := jsjson.Path(result, "errors").([]any); len(errs) != 1 || errs[0] != "usage_limit" {
		t.Errorf("errors %v", errs)
	}
	if _, err := os.Stat(filepath.Join(sb.project, ".orchestrator")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the usage-limit path wrote a run record")
	}
	if !strings.Contains(sb.incidents(t), `"kind":"limit","summary":"claude run ended on a session-limit message"`) {
		t.Errorf("incidents:\n%s", sb.incidents(t))
	}
}

// PE-45 over the usage-limit path: a claude that printed its limit message but did not exit is
// stopped at the cap, and the run is filed as the timeout, with its run record and exit 1, not
// as a deferred limit (exit 75, no record).
func TestRunClaudeCapStopAfterALimitMessageIsATimeout(t *testing.T) {
	sb := newClaudeSandbox(t)
	limit := `{"type":"result","is_error":true,"result":"You've hit your session limit"}` + "\n"
	sb.fakeClaude(t, limit, "", `trap 'exit 143' TERM
while :; do /bin/sleep 0.05; done`)
	code, stdout, _ := sb.run(t, "--quiet", "--timeout-min", "0.01", "--prompt", "p")
	if code != 1 {
		t.Fatalf("exit %d, stdout %s", code, stdout)
	}
	result := parseResult(t, stdout)
	if _, present := result.Get("reason"); present || jsjson.Path(result, "exit_code") != 143.0 {
		t.Errorf("result %s", stdout)
	}
	if _, err := os.Stat(jsjson.Path(result, "record").(string)); err != nil {
		t.Errorf("no run record: %v", err)
	}
	incidents := sb.incidents(t)
	if !strings.Contains(incidents, `"kind":"timeout","summary":"claude bridge timed out after 0.01 min"`) || strings.Count(incidents, "\n") != 1 {
		t.Errorf("want one timeout incident:\n%s", incidents)
	}
}

// start runs the bridge with a CLI that prints stdout and then sleeps, and returns once orch
// has logged ready, so the CLI is running and orch's signal handling is in place.
func (sb bridgeSandbox) start(t *testing.T, stdout, ready string, args ...string) (cmd *exec.Cmd, out *bytes.Buffer, stderr *orchStderr) {
	t.Helper()
	return sb.startEnding(t, "exec /bin/sleep 30", stdout, ready, args...)
}

// startEnding is start with a CLI that runs ending once it has printed stdout.
func (sb bridgeSandbox) startEnding(t *testing.T, ending, stdout, ready string, args ...string) (cmd *exec.Cmd, out *bytes.Buffer, stderr *orchStderr) {
	t.Helper()
	sb.fakeCLI(t, sb.bridge, stdout, "", ending)
	cmd = sb.command(args...)
	out, stderr = &bytes.Buffer{}, &orchStderr{}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stderr.String(), ready) {
		if time.Now().After(deadline) {
			t.Fatalf("orch did not log %q within 10s:\n%s", ready, stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cmd, out, stderr
}

// orchStderr is orch's stderr as os/exec copies it into a cmd.Stderr that is not a file: it
// may be read while the copy runs, and is complete once cmd.Wait, which joins the copy, has
// returned.
type orchStderr struct {
	lockedBuffer
}

func (s *orchStderr) String() string {
	return string(s.Bytes())
}

// waitWithin waits for cmd, failing if it takes longer than limit (the CLI's kill grace is 10 s,
// so a forwarded signal that reached it ends the run well before that).
func waitWithin(t *testing.T, cmd *exec.Cmd, limit time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(t, err)
	case <-time.After(limit):
		t.Fatalf("orch still running after %v", limit)
		return 0
	}
}

func TestRunClaudeForwardsSIGTERM(t *testing.T) {
	sb := newClaudeSandbox(t)
	init := `{"type":"system","subtype":"init","session_id":"s1","model":"m"}` + "\n"
	cmd, stdout, stderr := sb.start(t, init, "[claude] session s1 model=m\n", "--prompt", "p")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitWithin(t, cmd, 5*time.Second); code != 1 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	result := parseResult(t, stdout.String())
	if exit, present := result.Get("exit_code"); !present || exit != nil {
		t.Errorf("exit_code %v (present %v), want null", exit, present)
	}
	if errs := jsjson.Path(result, "errors").([]any); len(errs) != 1 || errs[0] != "claude exited null: " {
		t.Errorf("errors %q", errs)
	}
	if _, err := os.Stat(jsjson.Path(result, "record").(string)); err != nil {
		t.Errorf("no run record: %v", err)
	}
}

// PE-22: claude runs in a process group of its own, so the forwarded SIGTERM reaches a tool it
// started, and one that ignores SIGTERM is killed once claude has exited, before orch reports.
func TestRunClaudeSIGTERMLeavesNoToolOfClaudeRunning(t *testing.T) {
	sb := newClaudeSandbox(t)
	toolPid := filepath.Join(sb.home, "tool-pid")
	tool := `/bin/sh -c 'trap "" TERM; echo $$ > "$0.tmp"; /bin/mv "$0.tmp" "$0"; exec /bin/sleep 30' "` + toolPid + `" </dev/null >/dev/null 2>&1 &` + "\n"
	init := `{"type":"system","subtype":"init","session_id":"s1","model":"m"}` + "\n"
	cmd, stdout, stderr := sb.startEnding(t, tool+"exec /bin/sleep 30", init, "[claude] session s1 model=m\n", "--prompt", "p")
	fields := strings.Fields(waitForFile(t, toolPid, 5*time.Second)) // written once its trap is set
	if len(fields) != 1 {
		t.Fatalf("tool reported %q", fields)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitWithin(t, cmd, 5*time.Second); code != 1 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if exit, present := parseResult(t, stdout.String()).Get("exit_code"); !present || exit != nil {
		t.Errorf("exit_code %v (present %v), want null", exit, present)
	}
	// SIGKILLed before orch exited; init may take a moment to reap the orphan.
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("claude's tool %d still runs after orch exited", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunClaudeTimeoutSignalsClaude(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, "", "", "exec /bin/sleep 30")
	code, stdout, stderr := sb.run(t, "--timeout-min", "0", "--prompt", "p")
	lines := strings.Split(stderr, "\n")
	if code != 1 || len(lines) != 4 || lines[1] != "[claude] timeout; killing" || !strings.HasPrefix(lines[2], "[claude] done ok=false turns=null in ") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if exit, _ := parseResult(t, stdout).Get("exit_code"); exit != nil {
		t.Errorf("exit_code %v, want null", exit)
	}
}

// A null event kills the JS inside its line handler; orch stops claude, prints only the error
// line after the progress so far, and writes nothing else.
func TestRunClaudeNullEventCrashes(t *testing.T) {
	sb := newClaudeSandbox(t)
	sb.fakeClaude(t, "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\nnull\n", "", "exec /bin/sleep 30")
	start := time.Now()
	code, stdout, stderr := sb.run(t, "--prompt", "p")
	lines := strings.Split(stderr, "\n")
	if code != 1 || stdout != "" || len(lines) != 4 || lines[1] != "[claude] session s model=undefined" ||
		lines[2] != "TypeError: Cannot read properties of null (reading 'session_id')" {
		t.Fatalf("exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v: claude was not stopped", elapsed)
	}
	if _, err := os.Stat(filepath.Join(sb.project, ".orchestrator")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a crashed run wrote a run record")
	}
}

func TestRunClaudeSpawnFailure(t *testing.T) {
	sb := newClaudeSandbox(t) // no claude installed
	code, stdout, stderr := sb.run(t, "--prompt", "p")
	if code != 1 || stdout != "" || !strings.HasSuffix(stderr, "\nError: spawn claude ENOENT\n") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestRunClaudePromptErrors(t *testing.T) {
	sb := newClaudeSandbox(t)
	cases := []struct {
		args       []string
		wantStderr string
	}{
		{[]string{"--prompt"}, "no prompt: --prompt needs a value\n"},
		{[]string{"--prompt-file"}, "The \"paths[0]\" argument must be of type string. Received type boolean (true)\n"},
		{[]string{"--prompt-file", "absent"}, "ENOENT: no such file or directory, open '" + filepath.Join(sb.project, "absent") + "'\n"},
	}
	for _, c := range cases {
		code, stdout, stderr := sb.run(t, c.args...)
		if code != 2 || stdout != "" || stderr != c.wantStderr {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", c.args, code, stdout, stderr)
		}
	}
}
