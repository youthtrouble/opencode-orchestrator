//go:build parity

package parity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PE-42: --root owns the lane independently of --cwd. All processes use the real orch
// binary with the harness fake first on PATH; neither a live model nor supervisor is used.
func (h *harness) testWorkerProjectAndChildRoots(t *testing.T) {
	for _, detached := range []bool{false, true} {
		mode := "foreground"
		if detached {
			mode = "detached"
		}
		for _, scenario := range []string{"explicit-root", "relative-from-child", "root-default-cwd", "legacy-child", "legacy-orchestrator", "empty-root"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				sb := h.newSandbox(t)
				h.seedWorkerRootCase(t, sb)
				child := filepath.Join(sb.project, "child")
				brief := ".orchestrator/briefs/b.md"
				invoker, project, cwd := sb.project, sb.project, child
				selectors := []string{"--root", sb.project, "--cwd", child}
				switch scenario {
				case "relative-from-child":
					invoker = child
					selectors = []string{"--root", "..", "--cwd", "."}
					// Relative selectors use the process's physical cwd, not its PWD alias.
					var err error
					project, err = filepath.EvalSymlinks(sb.project)
					if err != nil {
						t.Fatal(err)
					}
					cwd = filepath.Join(project, "child")
				case "root-default-cwd":
					selectors, cwd = []string{"--root", sb.project}, sb.project
				case "legacy-child", "empty-root":
					selectors, project = []string{"--cwd", child}, child
					if scenario == "empty-root" {
						selectors = append(selectors, "--root", "")
					}
					seedProjectFile("child/"+brief, "nested brief\n")(t, sb)
				case "legacy-orchestrator":
					selectors, cwd = []string{"--cwd", filepath.Join(sb.project, ".orchestrator", "briefs")}, sb.project
				}
				args := append([]string{h.orchBin, "worker", "start", "--brief", brief, "--harness", "codex", "--lane", "root-test"}, selectors...)
				if detached {
					args = append(args, "--detach")
				}
				code, out, errOut := h.runWorkerRootCommand(t, sb, invoker, args)
				if code != 0 {
					t.Fatalf("start exit %d: out %s, err %s", code, out, errOut)
				}
				if detached {
					var launched launch
					if err := json.Unmarshal([]byte(out), &launched); err != nil || launched.RunnerPid < 2 {
						t.Fatalf("invalid launch %s: %v", out, err)
					}
					settle(t, launched.RunnerPid)
					if filepath.Dir(launched.LogPath) != filepath.Join(project, ".orchestrator", "logs") {
						t.Fatalf("log outside owning project: %s", launched.LogPath)
					}
				}
				stateFile := filepath.Join(project, ".orchestrator", "workers", "root-test.json")
				state := readWorkerRootJSON(t, stateFile)
				if state["status"] != "finished" {
					t.Fatalf("unfinished state: %v", state)
				}
				if _, err := os.Stat(strings.TrimSuffix(stateFile, ".json") + ".lock"); !os.IsNotExist(err) {
					t.Fatalf("lane lock not released: %v", err)
				}
				records, err := filepath.Glob(filepath.Join(project, ".orchestrator", "runs", "*.json"))
				if err != nil || len(records) != 1 {
					t.Fatalf("records %v: %v", records, err)
				}
				record := readWorkerRootJSON(t, records[0])
				if record["cwd"] != cwd {
					t.Fatalf("record cwd %v; want %s", record["cwd"], cwd)
				}
				if got := record["artifact_root"]; project != cwd && got != project || project == cwd && got != nil {
					t.Fatalf("artifact_root %v; project %s, cwd %s", got, project, cwd)
				}
				physical, err := filepath.EvalSymlinks(cwd)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(sb.home, "fake-log", "codex-cwd.txt"))
				if err != nil || string(data) != physical+"\n" {
					t.Fatalf("fake cwd %q, %v; want %s", data, err, physical)
				}
				if project != child {
					if _, err := os.Stat(filepath.Join(child, ".orchestrator")); !os.IsNotExist(err) {
						t.Fatalf("unexpected child artifacts: %v", err)
					}
				}
				if scenario == "legacy-orchestrator" {
					if _, err := os.Stat(filepath.Join(sb.project, ".orchestrator", "briefs", ".orchestrator")); !os.IsNotExist(err) {
						t.Fatalf("nested orchestrator artifacts: %v", err)
					}
				}
				// Exercise project selection for both read/stop commands after the lane exits;
				// stop cannot signal any process because the lane no longer holds its lock.
				for _, command := range []string{"status", "stop"} {
					args := []string{h.orchBin, "worker", command, "--root", project, "--cwd", child, "--lane", "root-test"}
					code, out, errOut := h.runWorkerRootCommand(t, sb, invoker, args)
					if code != 0 || !strings.Contains(out, `"lane":"root-test"`) && !strings.Contains(out, `"lane": "root-test"`) {
						t.Fatalf("%s exit %d: out %s, err %s", command, code, out, errOut)
					}
				}
			})
		}
		// The oracle has no --root: cwd remains the owning child project and cannot find
		// the parent-relative brief. Pin that refusal rather than normalizing it away.
		t.Run(mode+"/frozen-js-has-no-root", func(t *testing.T) {
			sb := h.newSandbox(t)
			h.seedWorkerRootCase(t, sb)
			args := []string{h.node, filepath.Join(h.orchJS, "bin", "worker.mjs"), "start", "--root", sb.project, "--cwd", filepath.Join(sb.project, "child"), "--brief", ".orchestrator/briefs/b.md", "--harness", "codex"}
			if detached {
				args = append(args, "--detach")
			}
			code, out, errOut := h.runWorkerRootCommand(t, sb, sb.project, args)
			if code != 2 || out != "" || errOut != "brief must be an existing .md file\n" {
				t.Fatalf("frozen JS exit %d: out %q, err %q", code, out, errOut)
			}
		})
	}
}

func (h *harness) seedWorkerRootCase(t *testing.T, sb sandbox) {
	t.Helper()
	inv := h.bridgeInvocation(t, codexBridge, bridgeCase{script: map[string]string{
		"jsonl": "{\"type\":\"turn.completed\",\"usage\":{}}\n", "exit": "0\n",
	}})
	seeds(inv.seed, seedProjectDir("child"), seedProjectFile(".orchestrator/briefs/b.md", "parent brief\n"))(t, sb)
}

func (h *harness) runWorkerRootCommand(t *testing.T, sb sandbox, dir string, argv []string) (int, string, string) {
	t.Helper()
	cmd := h.command(sb, argv, nil, []string{noPostRunRefresh})
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := exitCode(t, argv[0], cmd.Run())
	return code, out.String(), errOut.String()
}

func readWorkerRootJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
