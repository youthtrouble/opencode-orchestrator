//go:build parity

package parity

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PE-41: the frozen JS treats cwd literally and nests project state. These cases pin
// that old behaviour and assert the corrected Go behaviour, without normalizing it away.
func (h *harness) testNestedProjectRoot(t *testing.T, group string) {
	t.Run("PE-41-nested-cwd", func(t *testing.T) {
		if group == "bridges" {
			for _, b := range []bridge{claudeBridge, codexBridge, opencodeBridge} {
				t.Run(b.name, func(t *testing.T) { h.testNestedBridgeRoot(t, b) })
			}
			return
		}
		for _, runtime := range []string{"js", "orch"} {
			t.Run(runtime, func(t *testing.T) {
				sb := h.newSandbox(t)
				seeds(newFlipCaches(t).green, kickoffFeedsFail(),
					seedProjectFile("source.txt", "root-source"),
					seedProjectFile(".orchestrator/policy.json", `{"openai_credits":"on"}`),
					seedProjectFile(".orchestrator/workers/root.json", `{"lane":"root-worker"}`),
					seedProjectFile(".orchestrator/supervisor-state.json", `{"status":"root-supervisor"}`),
					seedProjectFile(".orchestrator/STATE.md", "## Next action\nroot-next-action\n"),
				)(t, sb)
				args := map[string][]string{
					"mailbox":  {"post", "--from", "test", "--subject", "root-message"},
					"session":  {"beat", "--door", "claude", "--now", "root-beat"},
					"policy":   {"set", "deploy=auto"},
					"manifest": {"create", "--task", "root-test", "--author", "test", "--out", filepath.Join(sb.home, "manifest.json"), "source.txt"},
					"incident": {"record", "--summary", "root-incident"},
					"worker":   {"status"}, "supervisor": {"status"},
					"flip":  {"decide", "--active", "opencode"},
					"watch": {}, "kickoff": {"--brief", "source.txt", "--dry-run"},
					"usage": {"--json"},
				}[group]
				var argv []string
				if runtime == "orch" {
					argv = append([]string{h.orchBin, group}, args...)
					if group == "usage" {
						argv = []string{h.orchBin, "routing", "--json"}
					}
				} else {
					argv = append([]string{h.node, filepath.Join(h.orchJS, "bin", group+".mjs")}, args...)
					if group == "usage" {
						argv = []string{h.node, "--input-type=module", "-e", `const {getRouting}=await import(process.argv[1]+"/lib/usage.mjs");console.log(JSON.stringify(await getRouting(),null,2));`, h.orchJS}
					}
				}
				if group == "flip" {
					seedProjectFile(".orchestrator/.orchestrator/flip.json", `{"awaiting_human":{"reason":"stale-nested-gate"}}`)(t, sb)
				}
				o := h.runInsideOrchestrator(t, sb, argv)
				wantCode := 0
				if runtime == "js" && (group == "worker" || group == "supervisor" || group == "watch" || group == "kickoff") {
					wantCode = 2
				}
				if runtime == "js" && group == "manifest" {
					wantCode = 1
				}
				if o.exitCode != wantCode {
					t.Fatalf("exit %d, want %d; out %s; err %s", o.exitCode, wantCode, o.stdout, o.stderr)
				}
				statePrefix := "project/"
				if runtime == "js" {
					statePrefix += ".orchestrator/"
				}
				writes := map[string]string{"mailbox": "mailbox/messages.jsonl", "session": "sessions/claude.json", "policy": "policy.json"}
				if file := writes[group]; file != "" {
					if _, ok := o.files[statePrefix+file]; !ok {
						t.Fatalf("missing %s%s: %v", statePrefix, file, o.files)
					}
					if runtime == "orch" {
						for name := range o.files {
							if strings.HasPrefix(name, "project/.orchestrator/") {
								t.Fatalf("nested state: %s", name)
							}
						}
					}
				}
				want := map[string]string{"worker": "root-worker", "supervisor": "root-supervisor", "watch": "root-next-action", "kickoff": "prompt:", "manifest": `"file_count":1`, "flip": `"action":"stay"`, "usage": `"mode": "on"`}[group]
				if runtime == "js" && group == "flip" {
					want = `"action":"gate"`
				}
				if runtime == "js" && group == "usage" {
					want = `"mode": "off"`
				}
				if want != "" && (runtime == "orch" || wantCode == 0) && !strings.Contains(o.stdout, want) {
					t.Fatalf("out %q lacks %q", o.stdout, want)
				}
				if group == "incident" {
					wantProject := filepath.Base(sb.project)
					if runtime == "js" {
						wantProject = ".orchestrator"
					}
					if !strings.Contains(o.files["state/incidents.jsonl"], `"project":"`+wantProject+`"`) {
						t.Fatalf("incident: %s", o.files["state/incidents.jsonl"])
					}
				}
			})
		}
	})
}

func (h *harness) runInsideOrchestrator(t *testing.T, sb sandbox, argv []string) outcome {
	t.Helper()
	cmd := h.command(sb, argv, nil, []string{noPostRunRefresh})
	cmd.Dir = filepath.Join(sb.project, ".orchestrator")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := exitCode(t, argv[0], cmd.Run())
	return outcome{exitCode: code, stdout: out.String(), stderr: errOut.String(), files: sb.files(t)}
}

func (h *harness) testNestedBridgeRoot(t *testing.T, b bridge) {
	for _, runtime := range []string{"js", "orch"} {
		t.Run(runtime, func(t *testing.T) {
			sb := h.newSandbox(t)
			inv := h.bridgeInvocation(t, b, bridgeCase{args: []string{"--prompt", "root-test", "--force"}, script: map[string]string{"exit": "0\n", "jsonl": ""}})
			inv.seed(t, sb)
			seedProjectDir(".orchestrator")(t, sb)
			argv := inv.orch
			if runtime == "js" {
				argv = inv.js
			}
			o := h.runInsideOrchestrator(t, sb, argv)
			prefix := "project/runs/"
			if runtime == "js" {
				prefix = "project/.orchestrator/runs/"
			}
			found := false
			for file := range o.files {
				if strings.HasPrefix(file, prefix) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no record under %s: exit %d, out %s, err %s; files %v", prefix, o.exitCode, o.stdout, o.stderr, o.files)
			}
			if runtime == "orch" {
				if _, err := os.Stat(filepath.Join(sb.project, ".orchestrator", ".orchestrator")); !os.IsNotExist(err) {
					t.Fatalf("nested state: %v", err)
				}
			}
		})
	}
}
