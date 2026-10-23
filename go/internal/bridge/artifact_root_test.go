package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

func TestArtifactRootPrecedence(t *testing.T) {
	t.Setenv("ORCH_ARTIFACT_ROOT", "/parent/.orchestrator/reports")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "/parent"},
		{[]string{"--artifact-root", "/flag/.orchestrator/runs"}, "/flag"},
	} {
		opts, _ := cliargs.ParseArgs(tc.args, []string{"artifact-root"}, nil)
		got, err := artifactRoot(opts, "/child")
		if err != nil || got != tc.want {
			t.Fatalf("%v: root %q, error %v; want %q", tc.args, got, err, tc.want)
		}
	}
	t.Setenv("ORCH_ARTIFACT_ROOT", "")
	opts, _ := cliargs.ParseArgs(nil, []string{"artifact-root"}, nil)
	if got, err := artifactRoot(opts, "/child"); err != nil || got != "/child" {
		t.Fatalf("default: %q, %v", got, err)
	}
	opts, _ = cliargs.ParseArgs([]string{"--artifact-root"}, []string{"artifact-root"}, nil)
	if _, err := artifactRoot(opts, "/child"); err != errPathArgBoolean {
		t.Fatalf("bare flag: %v", err)
	}
}

func TestBridgeArtifactsStayInParentAndChildCWDIsUnchanged(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode"} {
		for _, source := range []string{"flag", "env", "flag-wins", "same-root"} {
			t.Run(harness+"/"+source, func(t *testing.T) {
				sb := newBridgeSandbox(t, harness)
				child := filepath.Join(sb.project, "child")
				if err := os.Mkdir(child, 0o755); err != nil {
					t.Fatal(err)
				}
				// A failing fake also exercises the failure-incident project attribution.
				sb.fakeCLI(t, harness, "", "fake failure", `pwd -P > "$HOME/child-cwd"; exit 1`)
				root := sb.project
				args := []string{"--cwd", child, "--prompt", "bounded task", "--force", "--quiet"}
				if source == "same-root" {
					root = child
				}
				if source != "env" {
					args = append(args, "--artifact-root", filepath.Join(root, ".orchestrator", "reports"))
				}
				cmd := sb.command(args...)
				cmd.Env = append(cmd.Env, noPostRunRefreshEnv+"=1")
				if source == "env" || source == "flag-wins" {
					envRoot := root
					if source == "flag-wins" {
						envRoot = filepath.Join(sb.home, "wrong")
					}
					cmd.Env = append(cmd.Env, "ORCH_ARTIFACT_ROOT="+envRoot)
				}
				var out, errOut bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &errOut
				if code := exitCode(t, cmd.Run()); code != 1 {
					t.Fatalf("exit %d: %s", code, errOut.String())
				}
				result := parseResult(t, out.String())
				file := jsjson.Path(result, "record").(string)
				if filepath.Dir(file) != filepath.Join(root, ".orchestrator", "runs") {
					t.Fatalf("record escaped artifact root: %s", file)
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				record := parseResult(t, string(data))
				if got, present := record.Get("artifact_root"); present != (root != child) || present && got != root {
					t.Errorf("artifact_root = %v (present %v)", got, present)
				}
				if jsjson.Path(record, "cwd") != child || readHomeFile(t, sb, "child-cwd") != child+"\n" {
					t.Error("child cwd changed")
				}
				if root != child {
					if _, err := os.Stat(filepath.Join(child, ".orchestrator")); !os.IsNotExist(err) {
						t.Fatalf("child artifacts created: %v", err)
					}
				}
				if !strings.Contains(sb.incidents(t), `"project":"`+filepath.Base(root)+`"`) {
					t.Errorf("incident not attributed to artifact project: %s", sb.incidents(t))
				}
			})
		}
	}
}
