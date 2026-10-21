//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type policyCase struct {
	name       string
	args       []string
	seed       seed
	extraFiles []string
	crash      crashClass
	errorLine  string
}

// seedPolicy writes the project's override file as given.
func seedPolicy(content string) seed {
	return seedProjectFile(".orchestrator/policy.json", content)
}

// testPolicy compares `orch policy` with bin/policy.mjs. The project's .orchestrator/ (and so
// policy.json) is collected by the harness.
func (h *harness) testPolicy(t *testing.T) {
	const cwdUndefined = `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined`
	cases := []policyCase{
		{name: "show/no-file", args: []string{"show"}},
		{name: "show/no-command", args: nil},
		{name: "show/empty-command", args: []string{""}},
		{name: "show/overrides", args: []string{"show"}, seed: seedPolicy(`{"deploy":"auto","new_key":"x","push_to_main":"ask","2":"index"}`)},
		{name: "show/unparsable", args: []string{"show"}, seed: seedPolicy(`{"deploy":`)},
		{name: "show/bom", args: []string{"show"}, seed: seedPolicy("\ufeff{\"deploy\":\"auto\"}")},
		{name: "show/null-file", args: []string{"show"}, seed: seedPolicy(`null`)},
		{name: "show/string-file", args: []string{"show"}, seed: seedPolicy(`"ab"`)},
		{name: "show/array-file", args: []string{"show"}, seed: seedPolicy(`["x"]`)},
		{name: "show/own-proto-key", args: []string{"show"}, seed: seedPolicy(`{"__proto__":{"deploy":"auto"},"a":1}`)},
		{name: "show/directory", args: []string{"show"}, seed: seedProjectFile(".orchestrator/policy.json/inner", "")},
		{name: "show/cwd-after", args: []string{"show", "--cwd", "sub"}, seed: seedProjectFile("sub/.orchestrator/policy.json", `{"release":"auto"}`)},
		{name: "show/cwd-first-is-command", args: []string{"--cwd", "sub", "show"}},
		{name: "show/cwd-empty", args: []string{"show", "--cwd", ""}, seed: seedPolicy(`{"release":"auto"}`)},
		{name: "show/cwd-trailing", args: []string{"show", "--cwd"}, crash: crashC1, errorLine: cwdUndefined},
		{name: "unknown/cwd-trailing", args: []string{"frob", "--cwd"}, crash: crashC1, errorLine: cwdUndefined},
		{name: "cwd-only", args: []string{"--cwd"}, crash: crashC1, errorLine: cwdUndefined},

		{name: "set/pairs", args: []string{"set", "deploy=auto", "ignored", "release=ask", "a=b=c", "=empty", "trailing="}},
		{name: "set/zero-pairs-writes", args: []string{"set"}},
		{name: "set/keeps-existing", args: []string{"set", "deploy=auto"}, seed: seedPolicy(`{"spend":"auto","deploy":"ask"}`)},
		{name: "set/cwd-value-with-equals", args: []string{"set", "--cwd", "x=y", "deploy=auto"}, extraFiles: []string{"x=y/.orchestrator/policy.json"}},
		{name: "set/cwd-before-command", args: []string{"--cwd", "sub", "set", "deploy=auto"}},
		{name: "set/refuse-never", args: []string{"set", "deploy=auto", "x=never"}},
		{name: "set/force-push-never", args: []string{"set", "force_push=never"}},
		{name: "set/force-push-auto", args: []string{"set", "force_push=auto"}},
		{name: "set/existing-force-push-auto", args: []string{"set", "deploy=auto"}, seed: seedPolicy(`{"force_push":"auto"}`)},
		{name: "set/existing-force-push-fixed", args: []string{"set", "force_push=never"}, seed: seedPolicy(`{"force_push":"auto"}`)},
		{name: "set/unparsable-file-replaced", args: []string{"set", "deploy=auto"}, seed: seedPolicy(`not json`)},
		{name: "set/proto-not-own", args: []string{"set", "__proto__=x", "deploy=auto"}},
		{name: "set/proto-own", args: []string{"set", "__proto__=x", "0=zero"}, seed: seedPolicy(`{"__proto__":{"a":1},"b":2}`)},
		{name: "set/null-file", args: []string{"set", "a=b"}, seed: seedPolicy(`null`), crash: crashC1, errorLine: "TypeError: Cannot set properties of null (setting 'a')"},
		{name: "set/null-file-no-pairs", args: []string{"set"}, seed: seedPolicy(`null`), crash: crashC1, errorLine: "TypeError: Cannot use 'in' operator to search for 'force_push' in null"},
		{name: "set/number-file", args: []string{"set", "a=b"}, seed: seedPolicy(`5`), crash: crashC1, errorLine: "TypeError: Cannot create property 'a' on number '5'"},
		{name: "set/number-file-proto", args: []string{"set", "__proto__=x"}, seed: seedPolicy(`5`), crash: crashC1, errorLine: "TypeError: Cannot use 'in' operator to search for 'force_push' in 5"},
		{name: "set/string-file-index", args: []string{"set", "0=x"}, seed: seedPolicy(`"ab"`), crash: crashC1, errorLine: "TypeError: Cannot assign to read only property '0' of string 'ab'"},
		{name: "set/string-file-key", args: []string{"set", "2=x"}, seed: seedPolicy(`"ab"`), crash: crashC1, errorLine: "TypeError: Cannot create property '2' on string 'ab'"},
		{name: "set/boolean-file", args: []string{"set", "length=1"}, seed: seedPolicy(`true`), crash: crashC1, errorLine: "TypeError: Cannot create property 'length' on boolean 'true'"},
		{name: "set/openai-credits-on", args: []string{"set", "openai_credits=on", "openai_credits_floor=20"}},
		{name: "set/openai-credits-refused", args: []string{"set", "deploy=auto", "openai_credits=security-only"}},
		{name: "set/openai-credits-floor-refused", args: []string{"set", "openai_credits_floor=lots"}},
		{name: "set/openai-credits-floor-negative", args: []string{"set", "openai_credits_floor=-5"}},
		{name: "unattended", args: []string{"unattended", "ignored=x"}, seed: seedPolicy(`{"deploy":"auto"}`)},
		{name: "unattended/cwd", args: []string{"unattended", "--cwd", "sub"}},

		{name: "unknown-command", args: []string{"frob"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.policyInvocation(c)) })
	}
	t.Run("set/array-file-PE-6", h.testPolicyArrayFile)
	h.testPolicyRegistry(t)
}

// registryMissing and registryDirectory stand for a models.json that is absent or a directory.
const (
	registryMissing   = "<missing>"
	registryDirectory = "<directory>"
)

// testPolicyRegistry runs show and set against a copy of the JS tree whose models.json is
// broken or of an unexpected shape, with orch pointed at the same file. set writes the override
// before the registry is read, in both runtimes, so the file is compared after the failure too.
func (h *harness) testPolicyRegistry(t *testing.T) {
	cases := []struct {
		name, registry string
		crash          crashClass
		errorLine      string // "$ROOT" stands for the copied tree
		syntaxError    bool
	}{
		{name: "missing", registry: registryMissing, crash: crashC1, errorLine: "Error: ENOENT: no such file or directory, open '$ROOT/models.json'"},
		{name: "directory", registry: registryDirectory, crash: crashC1, errorLine: "Error: EISDIR: illegal operation on a directory, read"},
		{name: "null", registry: `null`, crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'autonomy')"},
		{name: "unparsable", registry: `{`, syntaxError: true},
		{name: "bom", registry: "\ufeff{}", syntaxError: true},
		{name: "array", registry: `[]`},
		{name: "number", registry: `5`},
		{name: "string", registry: `"ab"`},
		{name: "autonomy-null", registry: `{"autonomy":null}`},
		{name: "autonomy-string", registry: `{"autonomy":"ab"}`},
		{name: "autonomy-comment-only", registry: `{"autonomy":{"$comment":"x","a":1}}`},
	}
	for _, c := range cases {
		root := h.jsTreeWithRegistry(t, c.registry)
		for _, args := range [][]string{{"show"}, {"set", "deploy=auto"}} {
			t.Run("registry-"+c.name+"/"+args[0], func(t *testing.T) {
				inv := h.policyInvocation(policyCase{args: args, seed: seedPolicy(`{"release":"auto"}`), crash: c.crash, errorLine: strings.ReplaceAll(c.errorLine, "$ROOT", root)})
				inv.js = append([]string{h.node, filepath.Join(root, "bin", "policy.mjs")}, args...)
				inv.env = []string{"ORCH_HOME=" + root, "ORCH_REGISTRY=" + filepath.Join(root, "models.json")}
				if !c.syntaxError {
					h.comparePair(t, inv)
					return
				}
				js, orch := h.runPair(t, inv)
				for name, o := range map[string]outcome{"js": js, "orch": orch} {
					if o.exitCode != 1 || !slices.ContainsFunc(strings.Split(o.stderr, "\n"), func(l string) bool { return strings.HasPrefix(l, "SyntaxError: ") }) {
						t.Errorf("%s: exit %d, stderr %q; want 1 and a SyntaxError line (PE-2)", name, o.exitCode, o.stderr)
					}
				}
				js.stderr, orch.stderr = "", ""
				compareOutcomes(t, js, orch)
			})
		}
	}
}

// jsTreeWithRegistry copies the JS bin/ and lib/ into a temp directory (by its physical path,
// as Node resolves its own modules) with models.json replaced by registry.
func (h *harness) jsTreeWithRegistry(t *testing.T, registry string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(root, os.DirFS(h.orchJS)); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(root, "models.json")
	if err := os.Remove(models); err != nil {
		t.Fatal(err)
	}
	switch registry {
	case registryMissing:
	case registryDirectory:
		mustMkdir(t, models)
	default:
		writeSeedFile(t, models, registry)
	}
	return root
}

func (h *harness) policyInvocation(c policyCase) invocation {
	return invocation{
		js:         append([]string{h.node, filepath.Join(h.orchJS, "bin", "policy.mjs")}, c.args...),
		orch:       append([]string{h.orchBin, "policy"}, c.args...),
		seed:       c.seed,
		extraFiles: append([]string{"sub/.orchestrator/policy.json"}, c.extraFiles...),
		crash:      c.crash,
		errorLine:  c.errorLine,
	}
}

// testPolicyArrayFile asserts PE-6: given a policy.json holding an array, the JS assigns the
// pair as an array property, rewrites the array (without it) and succeeds; orch fails with a
// TypeError and leaves the file alone.
func (h *harness) testPolicyArrayFile(t *testing.T) {
	js, orch := h.runPair(t, h.policyInvocation(policyCase{args: []string{"set", "deploy=auto"}, seed: seedPolicy(`[ "x" ]`)}))
	const file = "project/policy.json"
	if js.exitCode != 0 || js.files[file] != "[\n  \"x\"\n]\n" {
		t.Errorf("js: exit %d, file %q; want 0 and the array rewritten", js.exitCode, js.files[file])
	}
	if orch.exitCode != 1 || !strings.HasPrefix(orch.stderr, "TypeError: ") || orch.stdout != "" || orch.files[file] != `[ "x" ]` {
		t.Errorf("orch: exit %d, stderr %q, stdout %q, file %q; want a TypeError and the file untouched", orch.exitCode, orch.stderr, orch.stdout, orch.files[file])
	}
}
