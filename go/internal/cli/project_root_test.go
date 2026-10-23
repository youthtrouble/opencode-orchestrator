package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandFamiliesUseProjectAboveOrchestrator(t *testing.T) {
	registry, err := filepath.Abs("../../../orchestrator/models.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cwd", "explicit"} {
		for _, c := range []struct {
			name    string
			command Command
			args    []string
			flag    string
			file    string
			want    string
		}{
			{"mailbox", Mailbox, []string{"post", "--from", "test", "--subject", "root-message"}, "--root", "mailbox/messages.jsonl", "root-message"},
			{"session", Session, []string{"beat", "--door", "claude", "--now", "root-beat"}, "--cwd", "sessions/claude.json", "root-beat"},
			{"policy", Policy, []string{"set", "deploy=auto"}, "--cwd", "policy.json", `"deploy": "auto"`},
			{"manifest", Manifest, []string{"create", "--task", "root-test", "--author", "test", "--out", "manifest.json", "source.txt"}, "--root", "", `"file_count":1`},
			{"incident", Incident, []string{"record", "--summary", "root-incident"}, "--cwd", "", `"recorded":`},
			{"supervisor", Supervisor, []string{"stop"}, "--cwd", "supervisor.stop", ""},
			{"worker", Worker, []string{"status"}, "--cwd", "", "root-worker"},
			{"flip", Flip, []string{"status"}, "--cwd", "", "root-flip"},
		} {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				root := sessionProject(t)
				state := filepath.Join(root, ".orchestrator")
				t.Setenv("HOME", t.TempDir())
				t.Setenv("ORCH_REGISTRY", registry)
				t.Setenv("ORCHESTRATOR_PROJECT", "")
				for file, content := range map[string]string{
					"source.txt": "source", ".orchestrator/flip.json": `{"to":"root-flip"}`,
					".orchestrator/workers/root.json": `{"lane":"root-worker"}`,
				} {
					path := filepath.Join(root, file)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := append([]string(nil), c.args...)
				if mode == "cwd" {
					t.Chdir(state)
				} else {
					t.Chdir(t.TempDir())
					args = append(args, c.flag, filepath.Join(state, "briefs", "x"))
				}
				code, out, errOut := run(t, c.command, args, "")
				if code != 0 || !strings.Contains(out, c.want) && c.file == "" {
					t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
				}
				if c.file != "" {
					data, err := os.ReadFile(filepath.Join(state, c.file))
					if err != nil || !strings.Contains(string(data), c.want) {
						t.Fatalf("state %q: %s, %v", c.file, data, err)
					}
				}
				if _, err := os.Stat(filepath.Join(state, ".orchestrator")); !os.IsNotExist(err) {
					t.Fatalf("created nested state: %v", err)
				}
				if c.name == "incident" {
					data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local/state/opencode/orchestrator/incidents.jsonl"))
					if err != nil || !strings.Contains(string(data), `"project":"`+filepath.Base(root)+`"`) {
						t.Fatalf("incident project: %s, %v", data, err)
					}
				}
			})
		}
	}
}

func TestMailboxEnvironmentRootAndSessionHookUseProjectRoot(t *testing.T) {
	root := sessionProject(t)
	nested := filepath.Join(root, ".orchestrator", "briefs")
	t.Setenv("ORCHESTRATOR_PROJECT", nested)
	code, out, errOut := run(t, Mailbox, []string{"post", "--from", "test", "--subject", "environment-root"}, "")
	if code != 0 {
		t.Fatalf("mailbox: %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, ".orchestrator/mailbox/messages.jsonl")); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = run(t, Session, []string{"beat", "--door", "claude", "--hook"}, `{"cwd":"`+nested+`","hook_event_name":"UserPromptSubmit"}`)
	if code != 0 {
		t.Fatalf("hook: %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, ".orchestrator/sessions/claude.json")); err != nil {
		t.Fatal(err)
	}
}

func TestIncidentRequestProjectUsesProjectRoot(t *testing.T) {
	log := incidentSandbox(t)
	code, out, errOut := run(t, Incident, []string{"record", "--request", "-"}, `{"summary":"nested request","project":"/repo/.orchestrator/briefs"}`)
	if code != 0 {
		t.Fatalf("request: %d, %q, %q", code, out, errOut)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), `"project":"repo"`) {
		t.Fatalf("incident project: %s, %v", data, err)
	}
}
