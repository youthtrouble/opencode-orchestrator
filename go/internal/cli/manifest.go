package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/manifest"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
)

// jsonSyntaxError is a manifest file JSON.parse rejects. V8's wording is not reproduced.
type jsonSyntaxError struct {
	err error
}

func (e *jsonSyntaxError) Error() string { return e.err.Error() }

// Manifest implements `orch manifest create|verify|diff`, byte-identical to bin/manifest.mjs.
// Where the JS dies of an uncaught exception, Manifest prints only the error line and exits 1.
func Manifest(args []string, stdio Stdio) int {
	parsed := cliargs.ParseManifest(decodeArgv(args))
	root, err := paths.ProjectRoot(parsed.Values["root"])
	if err != nil {
		return crash(stdio, err)
	}
	switch parsed.Command {
	case "create":
		return manifestCreate(root, parsed, stdio)
	case "verify":
		return manifestVerify(root, parsed.Positional, stdio)
	case "diff":
		return manifestDiff(parsed.Positional, stdio)
	}
	fmt.Fprintln(stdio.Err, "commands: create | verify | diff")
	return 2
}

// manifestCreate writes the manifest to --out, resolved from the working directory (not
// --root), and prints {out (as typed), file_count, manifest_sha256}.
func manifestCreate(root string, parsed cliargs.ManifestArgs, stdio Stdio) int {
	task, author, out := parsed.Values["task"], parsed.Values["author"], parsed.Values["out"]
	if task == "" || author == "" || out == "" || len(parsed.Positional) == 0 {
		fmt.Fprintln(stdio.Err, "usage: orch manifest create --task T --author A --out file.json [--baseline b.json] [--root dir] <paths...>")
		return 2
	}
	m, err := manifest.Build(manifest.Request{
		Root:     root,
		Paths:    parsed.Positional,
		Task:     task,
		Author:   author,
		Baseline: parsed.Values["baseline"],
		Now:      time.Now(),
	})
	if err != nil {
		return crash(stdio, err)
	}
	if err := writeManifest(out, m); err != nil {
		return crash(stdio, err)
	}
	receipt := jsjson.NewObject()
	receipt.Set("out", out)
	receipt.Set("file_count", jsjson.Path(m, "file_count"))
	receipt.Set("manifest_sha256", jsjson.Path(m, "manifest_sha256"))
	return printLine(stdio, receipt)
}

// writeManifest writes m as indented JSON to out, creating its directory. The file is replaced
// through a private temp and a rename (WritePrivateFile), so a symlink at out is replaced, never
// followed, and the result is owner-only, as the JS CLI does (R15).
func writeManifest(out string, m *jsjson.Object) error {
	file, err := nodefs.Resolve(out)
	if err != nil {
		return err
	}
	if err := nodefs.MkdirAll(filepath.Dir(file)); err != nil {
		return err
	}
	data, err := jsjson.MarshalIndent(m, "  ")
	if err != nil {
		return err
	}
	return nodefs.WritePrivateFile(file, append(data, '\n'))
}

// manifestVerify prints {ok, mismatched, missing, checked} and exits 1 unless ok.
func manifestVerify(root string, positional []string, stdio Stdio) int {
	m, err := readManifest(positional, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && len(positional) > 0 {
			file, resolveErr := nodefs.Resolve(positional[0])
			if resolveErr != nil {
				return crash(stdio, resolveErr)
			}
			fmt.Fprintf(stdio.Err, "manifest not found: %s; manifests live in the checkout that created them: pass an absolute path or run from that checkout (--root selects the source tree, not the manifest)\n", file)
			return 1
		}
		return crash(stdio, err)
	}
	v, err := manifest.Verify(root, m)
	if err != nil {
		return crash(stdio, err)
	}
	if code := printLine(stdio, v.Object()); code != 0 {
		return code
	}
	if !v.OK() {
		return 1
	}
	return 0
}

// manifestDiff prints {added, removed, changed, unchanged}, indented by two spaces.
func manifestDiff(positional []string, stdio Stdio) int {
	before, err := readManifest(positional, 0)
	if err != nil {
		return crash(stdio, err)
	}
	after, err := readManifest(positional, 1)
	if err != nil {
		return crash(stdio, err)
	}
	d, err := manifest.Diff(before, after)
	if err != nil {
		return crash(stdio, err)
	}
	data, err := jsjson.MarshalIndent(d.Object(), "  ")
	if err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}

// readManifest is JSON.parse(readFileSync(path.resolve(positional[i]), "utf8")).
func readManifest(positional []string, i int) (any, error) {
	if i >= len(positional) {
		return nil, errPathArgUndefined // too few files: path.resolve(undefined)
	}
	file, err := nodefs.Resolve(positional[i])
	if err != nil {
		return nil, err
	}
	return parseJSONFile(file)
}

// parseJSONFile is JSON.parse(fs.readFileSync(file, "utf8")): Node's fs errors, a
// *jsonSyntaxError for text JSON.parse rejects, and otherwise a value of any shape.
func parseJSONFile(file string) (any, error) {
	data, err := nodefs.ReadFile(file)
	if err != nil {
		return nil, err
	}
	value, err := jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		return nil, &jsonSyntaxError{err}
	}
	return value, nil
}
