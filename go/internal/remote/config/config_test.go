package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const home = "/home/u"

func writeConfig(t *testing.T, content string, mode fs.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remote.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile's mode is masked by the umask
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaultsAndExpandsRoots(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"url":"https://trains.example.com"}`, 0o600), home)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{URL: "https://trains.example.com", Roots: []string{"/home/u/Developer"}, Exclude: []string{}, CI: true, PollSeconds: 30}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load = %+v\nwant %+v", cfg, want)
	}

	cfg, err = Load(writeConfig(t, `{"url":"https://trains.example.com","roots":["~","~/src","/abs"],"exclude":["scratch","/abs/old"],"include_worktrees":true,"ci":false,"poll_seconds":15}`, 0o600), home)
	if err != nil {
		t.Fatal(err)
	}
	want = Config{URL: "https://trains.example.com", Roots: []string{"/home/u", "/home/u/src", "/abs"}, Exclude: []string{"scratch", "/abs/old"}, IncludeWorktrees: true, CI: false, PollSeconds: 15}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load = %+v\nwant %+v", cfg, want)
	}
}

func TestLoadRejectsBadContent(t *testing.T) {
	cases := map[string]struct{ content, problem string }{
		"plain http":             {`{"url":"http://trains.example.com"}`, "must use https"},
		"no url":                 {`{}`, "url is required"},
		"credentials in url":     {`{"url":"https://user:pw@trains.example.com"}`, "must not carry credentials"},
		"unparsable url":         {`{"url":"https://user:pw@trains.example.com/%zz"}`, `url: invalid URL escape "%zz"`},
		"no host":                {`{"url":"https:///path"}`, "has no host"},
		"credentials, no scheme": {`{"url":"user:pw@trains.example.com"}`, "has no host"},
		"credentials over http":  {`{"url":"http://user:pw@trains.example.com"}`, "must not carry credentials"},
		"credentials, opaque":    {`{"url":"ftp:user:pw@trains.example.com"}`, "has no host"},
		"unknown field":          {`{"url":"https://trains.example.com","token":"x"}`, `unknown field "token"`},
		"relative root":          {`{"url":"https://trains.example.com","roots":["src"]}`, `root "src" must be absolute`},
		"relative exclude path":  {`{"url":"https://trains.example.com","exclude":["a/b"]}`, `exclude "a/b"`},
		"poll too fast":          {`{"url":"https://trains.example.com","poll_seconds":14}`, "outside 15..300"},
		"poll too slow":          {`{"url":"https://trains.example.com","poll_seconds":301}`, "outside 15..300"},
		"trailing data":          {`{"url":"https://trains.example.com"} {}`, "unexpected data"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, c.content, 0o600), home)
			var invalid *InvalidError
			if !errors.As(err, &invalid) || !strings.Contains(invalid.Problem, c.problem) {
				t.Errorf("Load = %v, want an InvalidError containing %q", err, c.problem)
			}
			if err != nil && strings.Contains(err.Error(), "user:pw") {
				t.Errorf("Load = %v, which quotes the url's credentials", err)
			}
		})
	}
}

func TestLoadAllowsPlainHTTPOnlyOnLoopback(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:8787", "http://localhost:8787", "http://[::1]:8787"} {
		if _, err := Load(writeConfig(t, `{"url":"`+url+`"}`, 0o600), home); err != nil {
			t.Errorf("%s: %v", url, err)
		}
	}
}

func TestLoadRefusesAGroupOrOtherAccessibleFile(t *testing.T) {
	for _, mode := range []fs.FileMode{0o640, 0o604, 0o660} {
		_, err := Load(writeConfig(t, `{"url":"https://trains.example.com"}`, mode), home)
		var insecure *InsecureModeError
		if !errors.As(err, &insecure) {
			t.Errorf("mode %04o: Load = %v, want an InsecureModeError", mode, err)
		}
	}
}

func TestLoadRefusesAConfigSymlinkedIntoAGitTree(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "orchestrator", "remote.json")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte(`{"url":"https://trains.example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "remote.json")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	_, err := Load(link, home)
	var inGit *InsideGitTreeError
	if !errors.As(err, &inGit) {
		t.Fatalf("Load = %v, want an InsideGitTreeError", err)
	}
	if realRepo, _ := filepath.EvalSymlinks(repo); inGit.GitRoot != realRepo {
		t.Errorf("GitRoot = %s, want %s", inGit.GitRoot, realRepo)
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "remote.json"), home)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load = %v, want fs.ErrNotExist", err)
	}
}

func TestSaveWritesOwnerOnlyAndLoadReadsItBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orch", "remote.json")
	cfg := Default()
	cfg.URL = "https://trains.example.com"
	if err := Save(path, home, cfg); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]fs.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		if info, err := os.Stat(file); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %04o", file, info.Mode().Perm(), err, want)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"~/Developer"`) {
		t.Errorf("Save must keep ~ unexpanded on disk: %s, %v", data, err)
	}
	loaded, err := Load(path, home)
	if err != nil || loaded.Roots[0] != "/home/u/Developer" {
		t.Errorf("Load = %+v, %v", loaded, err)
	}
}

func TestSaveRefusesInvalidConfigAndGitTrees(t *testing.T) {
	var invalid *InvalidError
	if err := Save(filepath.Join(t.TempDir(), "remote.json"), home, Default()); !errors.As(err, &invalid) {
		t.Errorf("Save without url = %v, want an InvalidError", err)
	}

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.URL = "https://trains.example.com"
	path := filepath.Join(repo, "orchestrator", "remote.json")
	var inGit *InsideGitTreeError
	if err := Save(path, home, cfg); !errors.As(err, &inGit) {
		t.Errorf("Save into a worktree = %v, want an InsideGitTreeError", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Save wrote %s inside a git tree", path)
	}
}

func TestPathHonoursTheOverride(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	if got, _ := Path(env(nil), home); got != "/home/u/.config/orch/remote.json" {
		t.Errorf("Path = %s", got)
	}
	if got, _ := Path(env(map[string]string{PathEnv: "/etc/remote.json"}), home); got != "/etc/remote.json" {
		t.Errorf("Path = %s", got)
	}
}
