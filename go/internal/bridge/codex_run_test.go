package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// logArgvAndStdin is a fake CLI ending that records its argv, one argument per line, and what
// it reads on stdin, then exits 0.
const logArgvAndStdin = `printf '%s\n' "$@" > "$HOME/argv"; /bin/cat > "$HOME/stdin"; exit 0`

func readHomeFile(t *testing.T, sb bridgeSandbox, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sb.home, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunCodexNormalPath(t *testing.T) {
	sb := newBridgeSandbox(t, "codex")
	events := `{"type":"thread.started","thread_id":"0123456789"}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1}}` + "\n"
	sb.fakeCLI(t, "codex", events, "", logArgvAndStdin)
	const prompt = "multi\nline é prompt"
	code, stdout, stderr := sb.run(t, "--sandbox", "read-only", "--prompt", prompt)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	result := parseResult(t, stdout)
	if keys := strings.Join(result.Keys(), ","); keys != "ok,harness,thread_id,model,effort,sandbox,cwd,started_at,final_message,usage,files_changed,commands,errors,exit_code,duration_ms,record" {
		t.Errorf("result keys %s", keys)
	}
	record := jsjson.Path(result, "record").(string)
	if !strings.HasSuffix(record, "-codex-01234567.json") {
		t.Errorf("record %s", record)
	}
	if got := readHomeFile(t, sb, "stdin"); got != prompt {
		t.Errorf("codex read %q on stdin, want the prompt", got)
	}
	if got, want := readHomeFile(t, sb, "argv"), strings.Join([]string{"exec", "--json", "--skip-git-repo-check", "-C", sb.project, "-s", "read-only", "-c", `model_reasoning_effort="high"`, "-m", "gpt-6.1-sol", "-", ""}, "\n"); got != want {
		t.Errorf("argv\n got %q\nwant %q", got, want)
	}
	if env := readHomeFile(t, sb, "env"); env != "1|kept\n" {
		t.Errorf("codex saw CLAUDECODE|CLAUDECODE_X = %q, want both inherited", env)
	}
	done := regexp.MustCompile(`^\[codex\] done ok=true in \d+s; record ` + regexp.QuoteMeta(record) + `$`)
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 4 || lines[0] != "[codex] start model=gpt-6.1-sol effort=high sandbox=read-only cwd="+sb.project ||
		lines[1] != "[codex] thread 0123456789" || lines[2] != "[codex] message: done" || !done.MatchString(lines[3]) {
		t.Errorf("stderr:\n%s", stderr)
	}
	if sb.incidents(t) != "" {
		t.Error("a successful run recorded an incident")
	}
}

// PE-13: a codex that exits without reading its prompt (its own argv rejection, say) is a
// failed run like any other, even for a prompt far beyond the pipe buffer. The CLI error is
// synthetic: since H18 the bridge no longer sends this pair.
func TestRunCodexReportsAnUnreadPrompt(t *testing.T) {
	sb := newBridgeSandbox(t, "codex")
	sb.fakeCLI(t, "codex", "", "error: the argument '--approve-for-me' cannot be used with '--sandbox <MODE>'\n", "exit 2")
	write(t, filepath.Join(sb.project, "brief.md"), strings.Repeat("p", 1<<20))
	code, stdout, stderr := sb.run(t, "--quiet", "--prompt-file", "brief.md")
	if code != 1 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	result := parseResult(t, stdout)
	if errs := jsjson.Path(result, "errors").([]any); len(errs) != 1 || errs[0] != "codex exited 2: error: the argument '--approve-for-me' cannot be used with '--sandbox <MODE>'" {
		t.Errorf("errors %q", errs)
	}
	if _, err := os.Stat(jsjson.Path(result, "record").(string)); err != nil {
		t.Errorf("no run record: %v", err)
	}
	if !strings.Contains(sb.incidents(t), `"kind":"crash","summary":"codex bridge failed: codex exited 2: error: the argument`) {
		t.Errorf("incidents:\n%s", sb.incidents(t))
	}
}

func TestRunCodexForwardsSIGTERM(t *testing.T) {
	sb := newBridgeSandbox(t, "codex")
	cmd, stdout, stderr := sb.start(t, `{"type":"thread.started","thread_id":"t1"}`+"\n", "[codex] thread t1\n", "--prompt", "p")
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
	if errs := jsjson.Path(result, "errors").([]any); len(errs) != 1 || errs[0] != "codex exited null: " {
		t.Errorf("errors %q", errs)
	}
	if _, err := os.Stat(jsjson.Path(result, "record").(string)); err != nil {
		t.Errorf("no run record: %v", err)
	}
}

func TestRunCodexStartsADetachedUsageRefresh(t *testing.T) {
	sb := newBridgeSandbox(t, "codex")
	sb.fakeCLI(t, "codex", `{"type":"turn.completed"}`+"\n", "", "exit 0")
	report := filepath.Join(sb.home, "refresh")
	cmd := sb.command("--quiet", "--prompt", "p")
	cmd.Env = append(cmd.Env, refreshReportEnv+"="+report)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if code := exitCode(t, cmd.Run()); code != 0 {
		t.Fatalf("exit %d", code)
	}
	parseResult(t, stdout.String())
	fields := strings.Split(strings.TrimSpace(waitForFile(t, report, 5*time.Second)), "|")
	if len(fields) == 3 {
		killReported(t, fields[1])
	}
	if len(fields) != 3 || fields[0] != "usage --refresh" || fields[2] != fields[1] {
		t.Errorf("refresh reported %q, want usage --refresh in a group of its own", fields)
	}
}

// A bare --resume without a thread.started leaves thread_id true, which the run record throws
// on before the JS starts its refresh.
func TestRunCodexStartsNoRefreshAfterARunRecordCrash(t *testing.T) {
	sb := newBridgeSandbox(t, "codex")
	sb.fakeCLI(t, "codex", `{"type":"turn.completed"}`+"\n", "", "exit 0")
	assertNoRefresh(t, sb.home, sb.command("--quiet", "--prompt", "p", "--resume"))
}

// killReported kills the reported refresh once the test ends; it lingers otherwise.
func killReported(t *testing.T, pid string) {
	t.Helper()
	n, err := strconv.Atoi(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(n, syscall.SIGKILL) })
}
