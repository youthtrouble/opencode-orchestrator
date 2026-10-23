package bridge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

func TestRunOpencodeNormalPath(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	events := `{"sessionID":"ses_0123456789","part":{"type":"tool","tool":"bash"}}` + "\n" +
		`{"part":{"type":"text","text":"answer"}}` + "\n"
	sb.fakeCLI(t, "opencode", events, "", logArgvAndStdin)
	code, stdout, stderr := sb.run(t, "--agent", "coder", "--", "do", "it")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	result := parseResult(t, stdout)
	if keys := strings.Join(result.Keys(), ","); keys != "ok,harness,session_id,agent,cwd,started_at,final_message,errors,exit_code,duration_ms,record" {
		t.Errorf("result keys %s", keys)
	}
	if record := jsjson.Path(result, "record").(string); !strings.HasSuffix(record, "-opencode-ses_0123.json") || jsjson.Path(result, "final_message") != "answer" {
		t.Errorf("record %s, final_message %v", record, jsjson.Path(result, "final_message"))
	}
	if got := readHomeFile(t, sb, "argv"); got != "run\n--format\njson\n--auto\n--agent\ncoder\ndo it\n" {
		t.Errorf("argv %q", got)
	}
	if got := readHomeFile(t, sb, "stdin"); got != "" {
		t.Errorf("opencode read %q on stdin, want /dev/null", got)
	}
	if env := readHomeFile(t, sb, "env"); env != "1|kept\n" {
		t.Errorf("opencode saw CLAUDECODE|CLAUDECODE_X = %q, want both inherited", env)
	}
	if stderr != "[opencode] tool: bash\n[opencode] message: answer\n" {
		t.Errorf("stderr %q: there is no start, done or timeout line", stderr)
	}
}

// The CLI's last process, not its shell, prints the event orch waits for. orch forwards the
// SIGTERM as SIGINT (PE-65), and bash survives a SIGINT that arrives while it waits for a
// command that then exits normally (wait-and-cooperative-exit): a fake whose shell printed the
// event could go on to its final command, which the one forwarded signal never reaches.
func TestRunOpencodeForwardsSIGTERM(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(sb.home, "opencode-signal")); err != nil {
		t.Fatal(err)
	}
	cmd, stdout, stderr := sb.startEnding(t, `exec "$HOME/opencode-signal"`, "", "[opencode] message: hi\n", "--prompt", "p")
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
	if errs := jsjson.Path(result, "errors").([]any); len(errs) != 1 || errs[0] != "" {
		t.Errorf("errors %q, want the bare (empty) stderr tail", errs)
	}
	if _, err := os.Stat(jsjson.Path(result, "record").(string)); err != nil {
		t.Errorf("no run record: %v", err)
	}
}

// fakeOpencodeSignal retains the runtime's default SIGINT exit and empty stderr. Like the
// other signal fakes, it exits within 30 s even if failed-test cleanup kills only orch.
func fakeOpencodeSignal() int {
	if _, err := os.Stdout.WriteString(`{"part":{"type":"text","text":"hi"}}` + "\n"); err != nil {
		return 1
	}
	time.Sleep(30 * time.Second)
	return 0
}

// opencode-exec.mjs has no post-run refresh, so orch starts none (PE-12 is claude and codex
// only).
func TestRunOpencodeStartsNoUsageRefresh(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	sb.fakeCLI(t, "opencode", "", "", "exit 0")
	report := filepath.Join(sb.home, "refresh")
	cmd := sb.command("--quiet", "--prompt", "p")
	cmd.Env = append(cmd.Env, refreshReportEnv+"="+report)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if code := exitCode(t, cmd.Run()); code != 0 {
		t.Fatalf("exit %d", code)
	}
	parseResult(t, stdout.String())
	time.Sleep(500 * time.Millisecond) // a refresh, had one started, reports well within this
	if _, err := os.Stat(report); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("opencode-exec started a refresh (%v)", err)
	}
}

// A prompt holding NUL fails as Node's spawn does, before opencode starts.
func TestRunOpencodeRejectsANullByteInThePrompt(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	sb.fakeCLI(t, "opencode", "", "", logArgvAndStdin)
	cmd := sb.command()
	cmd.Stdin = strings.NewReader("a\x00b")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := exitCode(t, cmd.Run())
	const want = "TypeError [ERR_INVALID_ARG_VALUE]: The argument 'args[6]' must be a string without null bytes. Received 'a\\x00b'\n"
	if code != 1 || stdout.String() != "" || stderr.String() != want {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(sb.home, "argv")); !errors.Is(err, os.ErrNotExist) {
		t.Error("opencode was started")
	}
}

// PE-44: a usage limit opencode reports as an error event is filed as kind limit with its text,
// not as a crash with the empty stderr tail.
func TestRunOpencodeUsageLimitEventIsALimitIncident(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	limit := `{"type":"error","timestamp":1,"sessionID":"ses_1","error":{"type":"unknown","message":"ChatGPT usage limit reached"}}` + "\n"
	sb.fakeCLI(t, "opencode", limit, "", "exit 1")
	code, stdout, stderr := sb.run(t, "--model", "openai/gpt-6.1-sol", "--prompt", "p")
	if code != 1 || stderr != "[opencode] error: ChatGPT usage limit reached\n" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if errs := jsjson.Path(parseResult(t, stdout), "errors").([]any); len(errs) != 1 || errs[0] != "ChatGPT usage limit reached" {
		t.Errorf("errors %q", errs)
	}
	incidents := sb.incidents(t)
	for _, want := range []string{
		`"kind":"limit","summary":"opencode bridge failed: ChatGPT usage limit reached"`,
		`"context":{"model":"openai/gpt-6.1-sol","exit_code":1}`, // PE-47
	} {
		if !strings.Contains(incidents, want) {
			t.Errorf("incident lacks %s:\n%s", want, incidents)
		}
	}
}

// PE-46: --help prints help and runs nothing; --effort takes a value that is not the prompt.
func TestRunOpencodeHelpAndEffort(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	sb.fakeCLI(t, "opencode", "", "", logArgvAndStdin)
	code, stdout, stderr := sb.run(t, "--prompt", "p", "--help")
	if code != 0 || !strings.HasPrefix(stdout, "Run one headless opencode turn") || stderr != "" {
		t.Errorf("--help: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(sb.home, "argv")); !errors.Is(err, os.ErrNotExist) {
		t.Error("--help started opencode")
	}
	if code, _, stderr := sb.run(t, "--effort", "high", "--prompt", "p"); code != 0 {
		t.Fatalf("--effort: exit %d, stderr %q", code, stderr)
	}
	if got := readHomeFile(t, sb, "argv"); got != "run\n--format\njson\n--auto\n--agent\nconductor\np\n" {
		t.Errorf("argv %q", got)
	}
}

// fakeSignalReporter installs the bridge's CLI as one that writes the name of the first of
// SIGINT or SIGTERM it receives to $HOME/signal and exits, and returns the file it touches once
// both traps are set.
func (sb bridgeSandbox) fakeSignalReporter(t *testing.T) (trapped string) {
	t.Helper()
	sb.fakeCLI(t, sb.bridge, "", "", `trap 'echo INT > "$HOME/signal"; exit 130' INT
trap 'echo TERM > "$HOME/signal"; exit 143' TERM
: > "$HOME/trapped"
while :; do /bin/sleep 0.05; done`)
	return filepath.Join(sb.home, "trapped")
}

// PE-65: opencode is stopped with SIGINT, at the cap and for a forwarded SIGTERM, because
// SIGTERM leaves its service session running; claude keeps SIGTERM (PE-11). A cap stop is a
// timeout incident (PE-45). The test waits on the bridge's own exit, never on a sleep.
func TestBridgeStopSignals(t *testing.T) {
	cases := []struct {
		bridge, stop, want string
	}{
		{"opencode", "cap", "INT"},
		{"opencode", "sigterm", "INT"},
		{"claude", "cap", "TERM"},
		{"claude", "sigterm", "TERM"},
	}
	for _, c := range cases {
		t.Run(c.bridge+"/"+c.stop, func(t *testing.T) {
			sb := newBridgeSandbox(t, c.bridge)
			trapped := sb.fakeSignalReporter(t)
			args := []string{"--quiet", "--prompt", "p"}
			if c.stop == "cap" {
				args = append(args, "--timeout-min", "0.01")
			}
			cmd := sb.command(args...)
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			waitForFile(t, trapped, 10*time.Second)
			if c.stop == "sigterm" {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			if code := waitWithin(t, cmd, 10*time.Second); code != 1 {
				t.Fatalf("exit %d", code)
			}
			if got := strings.TrimSpace(readHomeFile(t, sb, "signal")); got != c.want {
				t.Errorf("%s received %s, want %s", c.bridge, got, c.want)
			}
			parseResult(t, stdout.String())
			incidents := sb.incidents(t)
			timedOut := strings.Contains(incidents, `"kind":"timeout","summary":"`+c.bridge+` bridge timed out after 0.01 min"`)
			if timedOut != (c.stop == "cap") || strings.Count(incidents, "\n") != 1 {
				t.Errorf("want a timeout incident only for the cap:\n%s", incidents)
			}
		})
	}
}
