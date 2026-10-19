// Package manifest builds, verifies and compares source manifests (lib/manifest.mjs): a JSON
// map of project-relative path -> sha256, so a reviewer can prove it read exactly the files the
// author released. Manifests are interchangeable with the JS runtime's, byte for byte.
//
// Manifests may be hand-crafted. Reading mirrors the JS for every JSON shape; where a shape
// makes the JS throw a TypeError, the functions here return a *ShapeError.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// ErrPathMissing is the JS error for a path that does not exist (a dangling or looping symlink
// does not exist); it is wrapped with the path as typed.
var ErrPathMissing = errors.New("manifest path does not exist")

// ShapeError reports a hand-crafted manifest whose shape makes the JS throw a TypeError. The
// message follows V8's wording; only the error class is part of the contract.
type ShapeError struct {
	Detail string
}

func (e *ShapeError) Error() string { return e.Detail }

// ignoredDirs are skipped below a walked directory, never when named explicitly; a file with
// one of these names is kept.
var ignoredDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, ".next": true, ".expo": true,
	"__pycache__": true, ".orchestrator": true, "coverage": true, ".turbo": true, ".cache": true,
}

// Request describes the manifest to build. Root is absolute; Paths are as typed, resolved
// against Root. An empty Baseline is written as null.
type Request struct {
	Root     string
	Paths    []string
	Task     string
	Author   string
	Baseline string
	Now      time.Time
}

// Build hashes every file Paths name (directories are walked) and returns the manifest
// {task, author, baseline, created_at, file_count, files, manifest_sha256}. files is keyed
// by path relative to Root (`../x` for a path outside it) in JS property order;
// manifest_sha256 is the sha256 of its compact JSON.
func Build(r Request) (*jsjson.Object, error) {
	rels, err := expand(r.Root, r.Paths)
	if err != nil {
		return nil, err
	}
	files := jsjson.NewObject()
	for _, rel := range rels {
		hash, err := sha256File(joinPath(r.Root, rel))
		if err != nil {
			return nil, err
		}
		// The JS assigns files[rel] on a plain object, where "__proto__" hits the prototype
		// setter and a string value is ignored: the file is hashed but not recorded.
		if rel != "__proto__" {
			files.Set(rel, hash)
		}
	}
	filesJSON, err := jsjson.Marshal(files)
	if err != nil {
		return nil, err
	}
	var baseline any
	if r.Baseline != "" {
		baseline = r.Baseline
	}
	m := jsjson.NewObject()
	m.Set("task", r.Task)
	m.Set("author", r.Author)
	m.Set("baseline", baseline)
	m.Set("created_at", jsjson.ISOString(r.Now))
	m.Set("file_count", float64(files.Len()))
	m.Set("files", files)
	m.Set("manifest_sha256", sha256Hex(filesJSON))
	return m, nil
}

// expand resolves each path, following symlinks: a directory contributes the files of its
// walk, anything else itself. The result is deduplicated and sorted in UTF-16 order.
func expand(root string, paths []string) ([]string, error) {
	var rels []string
	for _, p := range paths {
		abs := resolve(root, p)
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrPathMissing, p)
		}
		if info.IsDir() {
			if rels, err = walk(root, abs, rels); err != nil {
				return nil, err
			}
			continue
		}
		rel, err := nodefs.Relative(root, abs)
		if err != nil {
			return nil, err
		}
		rels = append(rels, rel)
	}
	slices.SortFunc(rels, jsstr.Compare)
	return slices.Compact(rels), nil
}

// walk appends the regular files below dir. Entries are typed without following symlinks,
// so symlinks, FIFOs, sockets and devices are skipped and never opened, and a symlink loop
// cannot recurse. Names are decoded as Node decodes them; an undecodable name then fails to
// open, as in the JS.
func walk(root, dir string, rels []string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nodefs.FileError(err, "scandir", dir)
	}
	for _, entry := range entries {
		name := jsstr.DecodeUTF8([]byte(entry.Name()))
		path := filepath.Join(dir, name)
		switch {
		case entry.IsDir():
			if ignoredDirs[name] {
				continue
			}
			if rels, err = walk(root, path, rels); err != nil {
				return nil, err
			}
		case entry.Type().IsRegular():
			rel, err := nodefs.Relative(root, path)
			if err != nil {
				return nil, err
			}
			rels = append(rels, rel)
		}
	}
	return rels, nil
}

// resolve is path.resolve(root, p) for an absolute root.
func resolve(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// joinPath is path.join(root, rel), which keeps a trailing slash of rel (so "file/" names a
// directory and does not exist when file is a regular file).
func joinPath(root, rel string) string {
	joined := filepath.Join(root, rel)
	if strings.HasSuffix(rel, "/") && joined != "/" {
		joined += "/"
	}
	return joined
}

func sha256File(path string) (string, error) {
	data, err := nodefs.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
