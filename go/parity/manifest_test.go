//go:build parity

package parity

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// ignoredDirNames are IGNORED_DIRS in lib/manifest.mjs.
var ignoredDirNames = []string{"node_modules", ".git", "dist", "build", ".next", ".expo", "__pycache__", ".orchestrator", "coverage", ".turbo", ".cache"}

// seedTree builds tree/ in the project: nested directories; every ignored name once as a
// directory (skipped below a walk) and once as a file (kept); names whose UTF-16 order differs
// from byte order (U+E000, U+1F600); NFD and NFC é (one file on normalization-insensitive file
// systems such as APFS, two elsewhere; written in a fixed order so the surviving name is the
// same in every sandbox); a symlink to a file, one to a directory, a symlink loop and a FIFO,
// all skipped and never opened inside a walk. At the project root it adds index-like names and
// a file named __proto__, which the JS drops there (and only there).
func seedTree(t *testing.T, sb sandbox) {
	tree := filepath.Join(sb.project, "tree")
	files := map[string]string{
		"top.txt":          "top\n",
		"__proto__":        "nested proto\n",
		"nested/deep/a.go": "package a\n",
		"nested/b.md":      "# b\n",
		"names/cafe\u0301": "nfd",
		"names/caf\u00e9":  "nfc",
		"names/\ue000":     "private use",
		"names/😀":          "emoji",
		"names/z":          "z",
	}
	for _, name := range ignoredDirNames {
		files["ignored/"+name+"/hidden.txt"] = "skipped"
		files["kept-files/"+name] = "a file named " + name
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		writeSeedFile(t, filepath.Join(tree, name), files[name])
	}
	for _, name := range []string{"__proto__", "2", "10", "42"} {
		writeSeedFile(t, filepath.Join(sb.project, name), "root "+name)
	}
	for link, target := range map[string]string{
		"links/to-file": "../top.txt",
		"links/to-dir":  "../nested",
		"links/loop":    "loop",
	} {
		path := filepath.Join(tree, link)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(tree, "links", "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/b.md", filepath.Join(tree, "explicit-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(tree, "broken-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tree, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// seedOutside adds x beside sub/ for paths outside a --root.
func seedOutside(t *testing.T, sb sandbox) {
	writeSeedFile(t, filepath.Join(sb.project, "x"), "outside root")
	writeSeedFile(t, filepath.Join(sb.project, "sub", "in.txt"), "inside root")
}

// seedManifests writes hand-made manifests into .orchestrator/ (compared unchanged afterwards).
func seedManifests(manifests map[string]string) seed {
	return func(t *testing.T, sb sandbox) {
		for name, content := range manifests {
			writeSeedFile(t, filepath.Join(sb.project, ".orchestrator", name), content)
		}
	}
}

func writeSeedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Text(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// createdAt is the one fresh value of a created manifest, rewritten only in its exact place
// and format.
var createdAt = regexp.MustCompile(`(?m)^(  "created_at": ")` + isoMillis.String() + `(",)$`)

func normalizeCreatedAt(s string) string {
	return createdAt.ReplaceAllString(s, "${1}<iso>${2}")
}

type manifestCase struct {
	name       string
	args       []string
	seed       seed
	extraFiles []string
	crash      crashClass
	errorLine  string
	// jsStdout, when set, is what the JS must print, so the case cannot pass vacuously.
	jsStdout string
}

func (h *harness) manifestInvocation(c manifestCase) invocation {
	return invocation{
		js:         append([]string{h.node, filepath.Join(h.orchJS, "bin", "manifest.mjs")}, c.args...),
		orch:       append([]string{h.orchBin, "manifest"}, c.args...),
		seed:       c.seed,
		extraFiles: c.extraFiles,
		crash:      c.crash,
		errorLine:  c.errorLine,
		normalize:  normalizeCreatedAt,
	}
}

// testManifest compares `orch manifest` with bin/manifest.mjs.
func (h *harness) testManifest(t *testing.T) {
	h.testNestedProjectRoot(t, "manifest")
	create := func(out string, rest ...string) []string {
		return append([]string{"create", "--task", "T-1", "--author", "claude-opus", "--out", out}, rest...)
	}
	okHash := sha256Text("top\n")
	manifests := seedManifests(map[string]string{
		"ok.json":       `{"files":{"tree/top.txt":"` + okHash + `","2":"` + sha256Text("root 2") + `"}}`,
		"mismatch.json": `{"files":{"tree/top.txt":"` + sha256Text("old") + `","2":"` + sha256Text("root 2") + `"}}`,
		"missing.json":  `{"files":{"tree/top.txt":"` + okHash + `","tree/gone":"x","tree/top.txt/":"x"}}`,
		"dir.json":      `{"files":{"tree/nested":"x"}}`,
		"nofiles.json":  `{"task":"t"}`,
		"null.json":     `null`,
		"odd.json":      `{"files":{"2":1,"__proto__":"p","tree/top.txt":"` + okHash + `"}}`,
		"string.json":   `{"files":"ab"}`,
		"before.json":   `{"files":{"same":"1","changed":"1","removed":"1","constructor":"c","valueOf":"v"}}`,
		"after.json":    `{"files":{"same":"1","changed":"2","added":"1","toString":"t","__proto__":"p","10":"x","2":"y"}}`,
		"array.json":    `{"files":["a","b"]}`,
		"index.json":    `{"files":{"0":"a","1":"c","2":"x","length":2,"map":"m"}}`,
		"strfiles.json": `{"files":"s"}`,
		"bad.json":      `{"files":`,
	})
	withTree := seeds(seedTree, manifests)
	treeOut := ".orchestrator/manifests/m.json"
	cases := []manifestCase{
		{name: "create/tree", args: create(treeOut, "tree"), seed: withTree},
		{name: "create/root-dot", args: create(treeOut, "."), seed: withTree},
		{name: "create/explicit-ignored-dirs", args: create(treeOut, "tree/ignored/dist", "tree/ignored/node_modules", "tree/ignored/.git"), seed: withTree},
		{name: "create/explicit-symlinks", args: create(treeOut, "tree/explicit-link", "tree/links/to-dir", "tree/links/to-file"), seed: withTree},
		{name: "create/duplicates-and-overlap", args: create(treeOut, "tree/nested", "tree/nested/b.md", "./tree/nested/", "tree/nested/deep", "tree/nested/b.md"), seed: withTree},
		{name: "create/baseline", args: create(treeOut, "--baseline", "base.json", "tree/top.txt"), seed: withTree},
		{name: "create/empty-baseline-is-null", args: create(treeOut, "--baseline", "", "tree/top.txt"), seed: withTree},
		{name: "create/empty-dir", args: create(treeOut, "tree/empty-dir"), seed: withTree},
		{name: "create/root-sub-outside", args: create(treeOut, "--root", "sub", "../x", "in.txt", "."), seed: seedOutside},
		{name: "create/out-outside-orchestrator", args: create("out/m.json", "tree/top.txt"), seed: withTree, extraFiles: []string{"out/m.json"}},
		{name: "create/out-relative-to-cwd-not-root", args: create("o.json", "--root", "sub", "in.txt"), seed: seedOutside, extraFiles: []string{"o.json", "sub/o.json"}},
		{name: "create/root-trailing-option-cleared", args: create(treeOut, "--root", "sub", "tree/top.txt", "--root"), seed: withTree},
		{name: "create/broken-symlink", args: create(treeOut, "tree/broken-link"), seed: withTree, crash: crashC1, errorLine: "Error: manifest path does not exist: tree/broken-link"},
		{name: "create/symlink-loop", args: create(treeOut, "tree/links/loop"), seed: withTree, crash: crashC1, errorLine: "Error: manifest path does not exist: tree/links/loop"},
		{name: "create/missing", args: create(treeOut, "tree/top.txt", "nope"), seed: withTree, crash: crashC1, errorLine: "Error: manifest path does not exist: nope"},
		{name: "create/usage-no-paths", args: create(treeOut), seed: withTree},
		{name: "create/usage-no-task", args: []string{"create", "--author", "a", "--out", treeOut, "tree"}, seed: withTree},
		{name: "create/usage-empty-author", args: []string{"create", "--task", "t", "--author", "", "--out", treeOut, "tree"}, seed: withTree},
		{name: "create/usage-option-eats-option", args: []string{"create", "--task", "--author", "a", "--out", treeOut, "tree"}, seed: withTree},

		{name: "verify/ok", args: []string{"verify", ".orchestrator/ok.json"}, seed: withTree,
			jsStdout: `{"ok":true,"mismatched":[],"missing":[],"checked":2}` + "\n"},
		{name: "verify/mismatch", args: []string{"verify", ".orchestrator/mismatch.json"}, seed: withTree,
			jsStdout: `{"ok":false,"mismatched":["tree/top.txt"],"missing":[],"checked":2}` + "\n"},
		{name: "verify/missing", args: []string{"verify", ".orchestrator/missing.json"}, seed: withTree,
			jsStdout: `{"ok":false,"mismatched":[],"missing":["tree/gone","tree/top.txt/"],"checked":3}` + "\n"},
		{name: "verify/odd-values", args: []string{"verify", ".orchestrator/odd.json"}, seed: withTree},
		{name: "verify/string-files", args: []string{"verify", ".orchestrator/string.json"}, seed: withTree},
		{name: "verify/root", args: []string{"verify", "--root", "tree", ".orchestrator/ok.json"}, seed: withTree},
		{name: "verify/no-argument", args: []string{"verify"}, seed: withTree, crash: crashC1,
			errorLine: `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined`},
		{name: "verify/manifest-is-dir", args: []string{"verify", "tree"}, seed: withTree, crash: crashC1, errorLine: "Error: EISDIR: illegal operation on a directory, read"},
		{name: "verify/entry-is-dir", args: []string{"verify", ".orchestrator/dir.json"}, seed: withTree, crash: crashC1, errorLine: "Error: EISDIR: illegal operation on a directory, read"},
		{name: "verify/files-absent", args: []string{"verify", ".orchestrator/nofiles.json"}, seed: withTree, crash: crashC1, errorLine: "TypeError: Cannot convert undefined or null to object"},
		{name: "verify/null-manifest", args: []string{"verify", ".orchestrator/null.json"}, seed: withTree, crash: crashC1, errorLine: "TypeError: Cannot read properties of null (reading 'files')"},

		{name: "diff/prototype-keys", args: []string{"diff", ".orchestrator/before.json", ".orchestrator/after.json"}, seed: manifests,
			jsStdout: "{\n  \"added\": [\n    \"2\",\n    \"10\",\n    \"added\"\n  ],\n  \"removed\": [\n    \"removed\"\n  ],\n  \"changed\": [\n    \"changed\",\n    \"toString\",\n    \"__proto__\"\n  ],\n  \"unchanged\": 1\n}\n"},
		{name: "diff/reverse", args: []string{"diff", ".orchestrator/after.json", ".orchestrator/before.json"}, seed: manifests},
		{name: "diff/array-files", args: []string{"diff", ".orchestrator/array.json", ".orchestrator/index.json"}, seed: manifests},
		{name: "diff/same", args: []string{"diff", ".orchestrator/ok.json", ".orchestrator/ok.json"}, seed: manifests},
		{name: "diff/one-argument", args: []string{"diff", ".orchestrator/ok.json"}, seed: manifests, crash: crashC1,
			errorLine: `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined`},
		{name: "diff/in-on-string", args: []string{"diff", ".orchestrator/strfiles.json", ".orchestrator/ok.json"}, seed: manifests, crash: crashC2},
		{name: "diff/files-absent", args: []string{"diff", ".orchestrator/ok.json", ".orchestrator/nofiles.json"}, seed: manifests, crash: crashC1,
			errorLine: "TypeError: Cannot convert undefined or null to object"},

		{name: "unknown-command", args: []string{"frob"}},
		{name: "no-command", args: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.jsStdout == "" {
				h.comparePair(t, h.manifestInvocation(c))
				return
			}
			js, orch := h.runPair(t, h.manifestInvocation(c))
			if js.stdout != c.jsStdout {
				t.Errorf("js stdout %q, want %q", js.stdout, c.jsStdout)
			}
			compareOutcomes(t, js, orch)
		})
	}
	t.Run("create/tree-contents", func(t *testing.T) {
		h.testManifestTreeContents(t, create(treeOut, "tree", "__proto__", "2", "10", "42"), withTree)
	})
	t.Run("create/out-is-symlink", func(t *testing.T) { h.testManifestOutIsSymlink(t, create(treeOut, "tree/top.txt"), withTree) })
	t.Run("create/out-is-dir", func(t *testing.T) { h.testManifestOutIsDir(t, create(treeOut, "tree/top.txt"), withTree) })
	t.Run("verify/unparsable-json", func(t *testing.T) { h.testManifestSyntaxError(t, manifests) })
	t.Run("PE-43-missing-manifest-remedy", func(t *testing.T) {
		inv := h.manifestInvocation(manifestCase{args: []string{"verify", "absent.json"}, seed: withTree})
		inv.projectPaths = true
		js, orch := h.runPair(t, inv)
		if js.exitCode != 1 || !strings.Contains(js.stderr, "ENOENT: no such file or directory, open '$PROJECT/absent.json'") {
			t.Fatalf("frozen JS missing manifest: exit %d, err %s", js.exitCode, js.stderr)
		}
		want := "manifest not found: $PROJECT/absent.json; manifests live in the checkout that created them: pass an absolute path or run from that checkout (--root selects the source tree, not the manifest)\n"
		if orch.exitCode != 1 || orch.stdout != "" || orch.stderr != want {
			t.Fatalf("Go missing manifest: exit %d, out %q, err %q", orch.exitCode, orch.stdout, orch.stderr)
		}
	})
	t.Run("harness/created-at-normalization", testCreatedAtNormalization)
	t.Run("harness/temp-suffix-normalization", testTempSuffixNormalization)
}

// testManifestOutIsSymlink proves a symlink at --out is replaced, never followed (R15): each
// runtime leaves the manifest as a regular file and the file the link pointed at unchanged.
func (h *harness) testManifestOutIsSymlink(t *testing.T, args []string, s seed) {
	const victim = "victim bytes\n"
	plantLink := func(t *testing.T, sb sandbox) {
		mustSymlink(t, "../../victim.txt", filepath.Join(sb.project, ".orchestrator", "manifests", "m.json"))
	}
	inv := h.manifestInvocation(manifestCase{args: args, seed: seeds(s, seedProjectFile("victim.txt", victim), plantLink), extraFiles: []string{"victim.txt"}})
	js, orch := h.runPair(t, inv)
	compareOutcomes(t, js, orch)
	if js.exitCode != 0 || !strings.Contains(js.files["project/manifests/m.json"], `"tree/top.txt"`) {
		t.Errorf("js: exit %d, manifests/m.json %q: want a regular file holding the manifest", js.exitCode, js.files["project/manifests/m.json"])
	}
	if js.files["extra/victim.txt"] != victim {
		t.Errorf("js victim.txt %q, want it unchanged", js.files["extra/victim.txt"])
	}
}

// tempSuffix is the random part of the temp a create renames over --out; each runtime draws its
// own, so it is the one fresh value in a failed rename's error line.
var tempSuffix = regexp.MustCompile(`\.tmp-[0-9]+'`)

func normalizeTempSuffix(s string) string {
	return tempSuffix.ReplaceAllString(s, ".tmp-<random>'")
}

// testManifestOutIsDir proves a directory at --out fails the same way in both runtimes, at the
// rename, once the random temp suffix is normalized, and that neither leaves its temp behind.
func (h *harness) testManifestOutIsDir(t *testing.T, args []string, s seed) {
	plantDir := func(t *testing.T, sb sandbox) {
		mustMkdir(t, filepath.Join(sb.project, ".orchestrator", "manifests", "m.json"))
	}
	inv := h.manifestInvocation(manifestCase{args: args, seed: seeds(s, plantDir), crash: crashC1,
		errorLine: "Error: EISDIR: illegal operation on a directory, rename '$PROJECT/.orchestrator/manifests/m.json.tmp-<random>' -> '$PROJECT/.orchestrator/manifests/m.json'"})
	inv.normalize = func(s string) string { return normalizeTempSuffix(normalizeCreatedAt(s)) }
	js, orch := h.runPair(t, inv)
	for runtime, o := range map[string]outcome{"js": js, "orch": orch} {
		for name := range o.files {
			if strings.Contains(name, ".tmp-") {
				t.Errorf("%s left its temp behind: %s", runtime, name)
			}
		}
	}
	if err := crashError(inv.crash, inv.errorLine, js, orch); err != nil {
		t.Error(err)
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

func testTempSuffixNormalization(t *testing.T) {
	in := "rename '/p/m.json.tmp-3141592653' -> '/p/m.json'; '/p/x.tmp-12' '/p/y.tmp-1a'"
	want := "rename '/p/m.json.tmp-<random>' -> '/p/m.json'; '/p/x.tmp-<random>' '/p/y.tmp-1a'"
	if got := normalizeTempSuffix(in); got != want {
		t.Errorf("normalized %q, want %q", got, want)
	}
}

// testManifestTreeContents proves the tree case is not vacuous: the JS manifest holds exactly
// the walked regular files, in JS property order with UTF-16 sorting, and orch writes the same.
func (h *harness) testManifestTreeContents(t *testing.T, args []string, s seed) {
	js, orch := h.runPair(t, h.manifestInvocation(manifestCase{args: args, seed: s}))
	compareOutcomes(t, js, orch)
	var keys []string
	for _, line := range strings.Split(js.files["project/manifests/m.json"], "\n") {
		if key, isEntry := strings.CutPrefix(line, `    "`); isEntry {
			keys = append(keys, key[:strings.Index(key, `"`)])
		}
	}
	kept := []string{}
	for _, name := range ignoredDirNames {
		kept = append(kept, "tree/kept-files/"+name)
	}
	slices.Sort(kept) // ASCII names: byte order is UTF-16 order
	want := slices.Concat(
		[]string{"2", "10", "42", "tree/__proto__"}, // index keys first, numerically; root __proto__ dropped
		kept, accentedNames(t),
		[]string{"tree/names/z", "tree/names/😀", "tree/names/\ue000", "tree/nested/b.md", "tree/nested/deep/a.go", "tree/top.txt"},
	)
	if !slices.Equal(keys, want) {
		t.Errorf("js manifest files\n%+q\nwant\n%+q", keys, want)
	}
}

// accentedNames are the manifest keys of the NFD and NFC é files seedTree writes, as this file
// system keeps them: both, in UTF-16 order, or only the first written (NFD) where the two
// names are one file.
func accentedNames(t *testing.T) []string {
	dir := t.TempDir()
	for _, name := range []string{"cafe\u0301", "caf\u00e9"} {
		writeSeedFile(t, filepath.Join(dir, name), name)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, "tree/names/"+entry.Name())
	}
	slices.Sort(names) // NFD's "e" (U+0065) sorts before U+00E9 in bytes and in UTF-16
	return names
}

// testManifestSyntaxError: JSON.parse failures exit 1 with a SyntaxError line in both
// runtimes; V8's wording is not reproduced (PE-2).
func (h *harness) testManifestSyntaxError(t *testing.T, manifests seed) {
	js, orch := h.runPair(t, h.manifestInvocation(manifestCase{args: []string{"verify", ".orchestrator/bad.json"}, seed: manifests}))
	for name, o := range map[string]outcome{"js": js, "orch": orch} {
		if o.exitCode != 1 || !slices.ContainsFunc(strings.Split(o.stderr, "\n"), func(l string) bool { return strings.HasPrefix(l, "SyntaxError: ") }) {
			t.Errorf("%s: exit %d, stderr %q; want 1 and a SyntaxError line", name, o.exitCode, o.stderr)
		}
	}
	js.stderr, orch.stderr = "", ""
	compareOutcomes(t, js, orch)
}

func testCreatedAtNormalization(t *testing.T) {
	written := "{\n  \"task\": \"2026-10-18T10:00:00.000Z\",\n  \"created_at\": \"2026-10-19T01:02:03.004Z\",\n  \"files\": {\n    \"created_at\": \"2026-10-19T01:02:03.004Z\",\n  }\n}\n"
	want := "{\n  \"task\": \"2026-10-18T10:00:00.000Z\",\n  \"created_at\": \"<iso>\",\n  \"files\": {\n    \"created_at\": \"2026-10-19T01:02:03.004Z\",\n  }\n}\n"
	if got := normalizeCreatedAt(written); got != want {
		t.Errorf("normalized\n%s\nwant\n%s", got, want)
	}
	if got := normalizeCreatedAt(`  "created_at": "2026-10-19T01:02:03Z",`); strings.Contains(got, "<iso>") {
		t.Errorf("rewrote a timestamp without milliseconds: %q", got)
	}
}
