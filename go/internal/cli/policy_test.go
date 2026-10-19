package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// policySandbox works in a temp project with the checkout's registry, so no test reads the
// installed models.json or writes outside temp directories.
func policySandbox(t *testing.T) {
	t.Helper()
	registry, err := filepath.Abs("../../../orchestrator/models.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_REGISTRY", registry)
	t.Chdir(t.TempDir())
}

func TestPolicySetAndShow(t *testing.T) {
	policySandbox(t)
	code, stdout, stderr := run(t, Policy, []string{"set", "deploy=auto", "--cwd", "proj"}, "")
	if code != 0 || stderr != "" || !containsLine(stdout, `  "deploy": "auto",`) || !containsLine(stdout, `  "force_push": "never",`) {
		t.Fatalf("set: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	data, err := os.ReadFile("proj/.orchestrator/policy.json")
	if err != nil || string(data) != "{\n  \"deploy\": \"auto\"\n}\n" {
		t.Errorf("policy.json %q, %v", data, err)
	}
	if code, show, _ := run(t, Policy, []string{"show", "--cwd", "proj"}, ""); code != 0 || show != stdout {
		t.Errorf("show: code %d stdout %q, want the set output", code, show)
	}
}

func TestPolicyRefusalsAndUsage(t *testing.T) {
	policySandbox(t)
	cases := []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"set", "deploy=never"}, 2, "refusing deploy=never\n"},
		{[]string{"set", "force_push=auto"}, 2, "force_push is always never\n"},
		{[]string{"frob"}, 2, "commands: show | set key=value | unattended\n"},
		{[]string{"--cwd", "x", "show"}, 2, "commands: show | set key=value | unattended\n"},
		{[]string{"show", "--cwd"}, 1, `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined` + "\n"},
	}
	for _, c := range cases {
		if code, stdout, stderr := run(t, Policy, c.args, ""); code != c.code || stdout != "" || stderr != c.stderr {
			t.Errorf("%q: code %d stdout %q stderr %q", c.args, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(".orchestrator"); !os.IsNotExist(err) {
		t.Error("a refused set wrote the policy file")
	}
}

func containsLine(text, line string) bool {
	return slices.Contains(strings.Split(text, "\n"), line)
}

// TestPolicyRegistryFailures pins what Node v26.5.1 prints when loadRegistry() or its
// `.autonomy` fails, and that set has written the override by then, as the JS has.
func TestPolicyRegistryFailures(t *testing.T) {
	policySandbox(t)
	registry := filepath.Join(t.TempDir(), "models.json")
	t.Setenv("ORCH_REGISTRY", registry)
	cases := []struct {
		name, content, stderr string
	}{
		{"missing", "", "Error: ENOENT: no such file or directory, open '" + registry + "'\n"},
		{"null", "null", "TypeError: Cannot read properties of null (reading 'autonomy')\n"},
		{"unparsable", "{", "SyntaxError: "},
	}
	for _, c := range cases {
		os.Remove(registry)
		if c.content != "" {
			if err := os.WriteFile(registry, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"show", "--cwd", c.name}, {"set", "deploy=auto", "--cwd", c.name}} {
			code, stdout, stderr := run(t, Policy, args, "")
			if code != 1 || stdout != "" || !strings.HasPrefix(stderr, c.stderr) {
				t.Errorf("%s %q: code %d stdout %q stderr %q, want %q", c.name, args, code, stdout, stderr, c.stderr)
			}
		}
		if data, err := os.ReadFile(filepath.Join(c.name, ".orchestrator", "policy.json")); err != nil || string(data) != "{\n  \"deploy\": \"auto\"\n}\n" {
			t.Errorf("%s: set did not write before failing: %q, %v", c.name, data, err)
		}
	}
	if err := os.WriteFile(registry, []byte(`[1]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := run(t, Policy, []string{"show", "--cwd", "missing"}, ""); code != 0 || stdout != "{\n  \"deploy\": \"auto\"\n}\n" {
		t.Errorf("array registry: code %d stdout %q, want the overrides alone", code, stdout)
	}
}
