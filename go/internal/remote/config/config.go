// Package config reads and writes the Work Trains machine config, ~/.config/orch/remote.json.
// It holds where the Worker lives and which projects to report; never secrets, which live in
// the login Keychain (package keychain).
//
// The file lives outside ~/.config/opencode/orchestrator on purpose: that directory is a
// symlink into the public checkout, so a config there would sit in a git work tree, one
// `git add -A` away from publishing the Worker's domain. Load and Save refuse any path whose
// real location is inside a git work tree.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// PathEnv overrides the config location.
const PathEnv = "ORCH_REMOTE_CONFIG"

// The poll interval bounds, matching the protocol's poll_after_seconds.
const (
	MinPollSeconds = 15
	MaxPollSeconds = 300
)

// Config is the machine config. Roots may start with "~/" on disk; Load expands them.
type Config struct {
	URL              string   `json:"url"`
	Roots            []string `json:"roots"`
	Exclude          []string `json:"exclude"`
	IncludeWorktrees bool     `json:"include_worktrees"`
	CI               bool     `json:"ci"`
	PollSeconds      int      `json:"poll_seconds"`
}

// Default is the config every absent field falls back to. URL has no default.
func Default() Config {
	return Config{
		Roots:       []string{"~/Developer"},
		Exclude:     []string{},
		CI:          true,
		PollSeconds: 30,
	}
}

// Path is the config file: $ORCH_REMOTE_CONFIG made absolute, else <home>/.config/orch/remote.json.
func Path(getenv func(string) string, home string) (string, error) {
	if override := getenv(PathEnv); override != "" {
		return filepath.Abs(override)
	}
	return filepath.Join(home, ".config", "orch", "remote.json"), nil
}

// InvalidError is a config whose content breaks a rule.
type InvalidError struct {
	Path    string
	Problem string
}

func (e *InvalidError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Problem) }

// InsideGitTreeError is a config whose real path lies inside a git work tree.
type InsideGitTreeError struct {
	Path    string // the real path of the config file
	GitRoot string // the ancestor directory holding .git
}

func (e *InsideGitTreeError) Error() string {
	return fmt.Sprintf("%s is inside the git work tree %s; keep the remote config outside any repository (set %s)", e.Path, e.GitRoot, PathEnv)
}

// InsecureModeError is a config file readable or writable by group or others.
type InsecureModeError struct {
	Path string
	Mode fs.FileMode
}

func (e *InsecureModeError) Error() string {
	return fmt.Sprintf("%s has mode %04o; it must be owner-only (chmod 600 %s)", e.Path, e.Mode.Perm(), e.Path)
}

// Load reads the config at path, applies defaults, validates it and expands "~/" in roots
// against home. A missing file is an error wrapping fs.ErrNotExist.
func Load(path, home string) (Config, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Config{}, err
	}
	if err := refuseGitTree(realPath); err != nil {
		return Config{}, err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return Config{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, &InsecureModeError{Path: path, Mode: info.Mode()}
	}
	data, err := os.ReadFile(realPath)
	if err != nil {
		return Config{}, err
	}
	cfg, err := decode(data)
	if err != nil {
		return Config{}, &InvalidError{Path: path, Problem: err.Error()}
	}
	if err := cfg.validate(home); err != nil {
		return Config{}, &InvalidError{Path: path, Problem: err.Error()}
	}
	cfg.Roots = expandRoots(cfg.Roots, home)
	return cfg, nil
}

// Save validates cfg and writes it to path owner-only (directory 0700, file 0600, through a
// private temp file and a rename). It refuses a destination inside a git work tree.
func Save(path, home string, cfg Config) error {
	if err := cfg.validate(home); err != nil {
		return &InvalidError{Path: path, Problem: err.Error()}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if err := refuseGitTree(filepath.Join(realDir, filepath.Base(path))); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return nodefs.WritePrivateFile(path, append(data, '\n'))
}

func decode(data []byte) (Config, error) {
	cfg := Default()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("unexpected data after the JSON object")
	}
	if cfg.Roots == nil {
		cfg.Roots = []string{}
	}
	if cfg.Exclude == nil {
		cfg.Exclude = []string{}
	}
	return cfg, nil
}

func (c Config) validate(home string) error {
	if err := validateURL(c.URL); err != nil {
		return err
	}
	for _, root := range expandRoots(c.Roots, home) {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("root %q must be absolute or start with ~/", root)
		}
	}
	for _, name := range c.Exclude {
		if name == "" || (strings.Contains(name, "/") && !filepath.IsAbs(name)) {
			return fmt.Errorf("exclude %q must be a directory name or an absolute path", name)
		}
	}
	if c.PollSeconds < MinPollSeconds || c.PollSeconds > MaxPollSeconds {
		return fmt.Errorf("poll_seconds %d is outside %d..%d", c.PollSeconds, MinPollSeconds, MaxPollSeconds)
	}
	return nil
}

// loopbackHosts may be reached over plain http, for tests against a local server only.
var loopbackHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

func validateURL(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(raw)
	var parseErr *url.Error
	if errors.As(err, &parseErr) {
		// url.Error quotes the whole URL, which may hold credentials; keep only the reason.
		return fmt.Errorf("url: %w", parseErr.Err)
	} else if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if parsed.User != nil {
		return errors.New("url must not carry credentials")
	}
	// Every route is joined to the url's path, so a query or a fragment has no place to go.
	// After a successful parse a "#" can only open the fragment, an empty one included.
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#") {
		return errors.New("url must not have a query or a fragment")
	}
	// The messages below never quote the raw url: a malformed one such as
	// "user:pw@host" parses with no host and no userinfo, yet still holds credentials.
	switch {
	case parsed.Host == "":
		return errors.New("url has no host")
	case parsed.Scheme == "https":
		return nil
	case parsed.Scheme == "http" && loopbackHosts[parsed.Hostname()]:
		return nil
	}
	return errors.New("url must use https (plain http only for 127.0.0.1, localhost or [::1])")
}

func expandRoots(roots []string, home string) []string {
	expanded := make([]string, len(roots))
	for i, root := range roots {
		expanded[i] = expandHome(root, home)
	}
	return expanded
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// refuseGitTree returns an InsideGitTreeError when any ancestor directory of realPath holds a
// .git entry (a directory in a main checkout, a file in a linked worktree).
func refuseGitTree(realPath string) error {
	for dir := filepath.Dir(realPath); ; dir = filepath.Dir(dir) {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		switch {
		case err == nil:
			return &InsideGitTreeError{Path: realPath, GitRoot: dir}
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if parent := filepath.Dir(dir); parent == dir {
			return nil
		}
	}
}
