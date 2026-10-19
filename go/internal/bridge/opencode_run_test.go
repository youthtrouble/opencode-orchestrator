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

func TestRunOpencodeForwardsSIGTERM(t *testing.T) {
	sb := newBridgeSandbox(t, "opencode")
	cmd, stdout, stderr := sb.start(t, `{"part":{"type":"text","text":"hi"}}`+"\n", "[opencode] message: hi\n", "--prompt", "p")
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
