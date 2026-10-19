package cliargs

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// The expected values below were produced by running the JS loops on the same argv
// (lib/run-record.mjs parseArgs and the loops in bin/mailbox.mjs, manifest.mjs, kickoff.mjs).

func TestParseArgs(t *testing.T) {
	t.Run("a value key takes the next token even when it looks like an option", func(t *testing.T) {
		opts, positional := ParseArgs(
			[]string{"--model", "--effort", "high", "pos", "--force", "x", "--unknown", "y", "--cwd"},
			[]string{"model", "effort", "cwd"}, []string{"force"})
		assertEqual(t, opts, Opts{"model": valueOpt("--effort"), "force": flagOpt, "unknown": flagOpt, "cwd": flagOpt})
		assertEqual(t, positional, []string{"high", "pos", "x", "y"})
	})
	t.Run("later keys win and a bare -- is the empty key", func(t *testing.T) {
		opts, positional := ParseArgs([]string{"--force", "--model", "a", "--model", "b", "--", ""},
			[]string{"model"}, []string{"force"})
		assertEqual(t, opts, Opts{"force": flagOpt, "model": valueOpt("b"), "": flagOpt})
		assertEqual(t, positional, []string{""})
	})
}

func TestOptsAccessorsFollowJSTruthiness(t *testing.T) {
	opts := Opts{"cwd": valueOpt("/tmp"), "empty": valueOpt(""), "force": flagOpt}
	if value, ok := opts.Value("cwd"); !ok || value != "/tmp" {
		t.Errorf("Value(cwd) = %q, %v", value, ok)
	}
	if _, ok := opts.Value("force"); ok {
		t.Error("Value(force) reported a string for a bare flag")
	}
	if !opts.IsFlag("force") || opts.IsFlag("cwd") {
		t.Error("IsFlag disagrees with how the keys were given")
	}
	for key, want := range map[string]bool{"cwd": true, "force": true, "empty": false, "missing": false} {
		if got := opts.Truthy(key); got != want {
			t.Errorf("Truthy(%s) = %v, want %v", key, got, want)
		}
	}
}

func TestOptsJSValues(t *testing.T) {
	opts := Opts{"cwd": valueOpt("/tmp"), "empty": valueOpt(""), "force": flagOpt}
	for key, want := range map[string]any{"cwd": "/tmp", "empty": "", "force": true, "missing": nil} {
		if got := opts.JS(key); got != want {
			t.Errorf("JS(%s) = %#v, want %#v", key, got, want)
		}
	}
	for key, want := range map[string]any{"cwd": "/tmp", "empty": "x", "force": true, "missing": "x"} {
		if got := opts.Or(key, "x"); got != want {
			t.Errorf("Or(%s, x) = %#v, want %#v", key, got, want)
		}
	}
}

func TestParseMailbox(t *testing.T) {
	t.Run("flags, repeated refs, ignored positionals", func(t *testing.T) {
		args := ParseMailbox([]string{"post", "--from", "a", "stray", "--ref", "x", "--json", "--ref", "y", "--subject", "", "--body", "-", "--ref"})
		assertEqual(t, args, MailboxArgs{
			Command: "post",
			Values:  map[string]string{"from": "a", "subject": "", "body": "-"},
			Flags:   map[string]bool{"json": true, "ref": true},
			Refs:    []string{"x", "y"},
		})
	})
	t.Run("a key can be both a flag and a value", func(t *testing.T) {
		args := ParseMailbox([]string{"list", "--json", "v", "--json"})
		assertEqual(t, args.Values, map[string]string{"json": "v"})
		assertEqual(t, args.Flags, map[string]bool{"json": true})
	})
	t.Run("project root prefers --root, then ORCHESTRATOR_PROJECT, then the working directory", func(t *testing.T) {
		env := map[string]string{"ORCHESTRATOR_PROJECT": "/env/project"}
		getenv := func(key string) string { return env[key] }
		cwd, _ := os.Getwd()
		cases := []struct {
			argv []string
			env  string
			want string
		}{
			{[]string{"list", "--root", "/given/root"}, "/env/project", "/given/root"},
			{[]string{"list", "--root"}, "/env/project", "/env/project"},
			{[]string{"list"}, "", cwd},
			{[]string{"list", "--root", "rel"}, "", filepath.Join(cwd, "rel")},
		}
		for _, c := range cases {
			env["ORCHESTRATOR_PROJECT"] = c.env
			got, err := ParseMailbox(c.argv).ProjectRoot(getenv)
			if err != nil || got != c.want {
				t.Errorf("ProjectRoot(%v) = %q, %v; want %q", c.argv, got, err, c.want)
			}
		}
	})
}

func TestParseIncident(t *testing.T) {
	args, err := ParseIncident([]string{"record", "--kind", "bug", "--ctx", "a=1", "--json", "--ctx", "b=2", "--summary", "s"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, args, IncidentArgs{
		Command: "record",
		Opts:    Opts{"kind": valueOpt("bug"), "json": flagOpt, "summary": valueOpt("s")},
		Ctx:     []string{"a=1", "b=2"},
	})
}

// TestParseIncidentBareCtx pins the JS loop: a bare --ctx replaces the list with true, so a
// later --ctx value throws (Node: "TypeError: opts.ctx.push is not a function"), whatever the
// command; a trailing bare --ctx only marks the list.
func TestParseIncidentBareCtx(t *testing.T) {
	args, err := ParseIncident([]string{"list", "--ctx", "a=1", "--json", "--ctx"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, args, IncidentArgs{Command: "list", Opts: Opts{"json": flagOpt}, CtxFlag: true})
	for _, argv := range [][]string{
		{"record", "--ctx", "--ctx", "a=b", "--summary", "s"},
		{"frob", "--ctx", "--ctx", "a=b"},
		{"list", "--ctx", "a", "--ctx", "--ctx", "b"},
	} {
		if _, err := ParseIncident(argv); !errors.Is(err, ErrCtxAfterBareCtx) || err.Error() != "opts.ctx.push is not a function" {
			t.Errorf("ParseIncident(%q) error = %v", argv, err)
		}
	}
}

func TestParseManifest(t *testing.T) {
	args := ParseManifest([]string{"create", "--task", "--author", "A", "p1", "--out", "o.json", "p2", "--root"})
	assertEqual(t, args, ManifestArgs{
		Command:    "create",
		Values:     map[string]string{"task": "--author", "out": "o.json"},
		Positional: []string{"A", "p1", "p2"},
	})
}

func TestParseUsage(t *testing.T) {
	assertEqual(t, ParseUsage([]string{"--json", "--brief", "--refresh=1"}), UsageArgs{JSON: true})
	assertEqual(t, ParseUsage([]string{"--refresh", "x", "--json"}), UsageArgs{Refresh: true, JSON: true})
}

func TestParseManifestTrailingRepeatedOptionClearsValue(t *testing.T) {
	for _, key := range []string{"task", "author", "out", "root", "baseline"} {
		t.Run(key, func(t *testing.T) {
			args := ParseManifest([]string{"create", "--" + key, "old", "--keep", "yes", "--" + key})
			assertEqual(t, args.Values, map[string]string{"keep": "yes"})
		})
	}
}

func TestReadPromptMalformedUTF8MatchesNode(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"valid", "é中😀\ufffd", "é中😀\ufffd"},
		{"invalid byte", "\xff", "\ufffd"},
		{"invalid run", "\xff\xff", "\ufffd\ufffd"},
		{"continuations", "\x80\xbf", "\ufffd\ufffd"},
		{"incomplete two", "\xc2", "\ufffd"},
		{"incomplete three", "\xe2\x82", "\ufffd"},
		{"incomplete four", "\xf0\x9f\x92", "\ufffd"},
		{"interrupted three", "\xe2\x82A", "\ufffdA"},
		{"interrupted four", "\xf0\x9f\x92A", "\ufffdA"},
		{"new lead", "\xe2\x82\xc2\xa2", "\ufffd¢"},
		{"overlong two", "\xc0\xaf", "\ufffd\ufffd"},
		{"overlong three", "\xe0\x80\x80", "\ufffd\ufffd\ufffd"},
		{"overlong four", "\xf0\x80\x80\x80", "\ufffd\ufffd\ufffd\ufffd"},
		{"surrogate", "\xed\xa0\x80", "\ufffd\ufffd\ufffd"},
		{"above Unicode", "\xf4\x90\x80\x80", "\ufffd\ufffd\ufffd\ufffd"},
		{"obsolete lead", "\xf5\x80\x80\x80", "\ufffd\ufffd\ufffd\ufffd"},
		{"ASCII boundary", "a\xe2\x82\n\xffz", "a\ufffd\n\ufffdz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := fileWithContent(t, c.input)
			for _, source := range []string{"file", "stdin"} {
				t.Run(source, func(t *testing.T) {
					opts := Opts{}
					if source == "file" {
						opts["prompt-file"] = valueOpt(file.Name())
					}
					got, err := ReadPrompt(opts, nil, file)
					if err != nil || !bytes.Equal([]byte(got), []byte(c.want)) {
						t.Fatalf("ReadPrompt bytes = %x, %v; want %x", got, err, c.want)
					}
				})
			}
			t.Run("Node oracle", func(t *testing.T) {
				if _, err := exec.LookPath("node"); err != nil {
					t.Skip("Node unavailable; fixed byte expectations still tested")
				}
				cmd := exec.Command("node", "-e", `process.stdout.write(require('node:fs').readFileSync(0, 'utf8'))`)
				cmd.Stdin = bytes.NewBufferString(c.input)
				got, err := cmd.Output()
				if err != nil || !bytes.Equal(got, []byte(c.want)) {
					t.Fatalf("Node bytes = %x, %v; want %x", got, err, c.want)
				}
			})
		})
	}
}

func TestParseKickoff(t *testing.T) {
	args := ParseKickoff([]string{"--brief", "a.md", "--door", "claude", "--brief", "", "--headless", "--brief", "--brief", "b.md", "--dry-run", "--cwd"})
	assertEqual(t, args, KickoffArgs{
		Briefs: []string{"a.md", "--brief"},
		Opts:   Opts{"door": valueOpt("claude"), "headless": flagOpt, "dry-run": flagOpt, "cwd": flagOpt},
	})
}

func TestReadPrompt(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("from file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		opts       Opts
		positional []string
		stdin      string
		want       string
		wantErr    error
	}{
		{"prompt file wins over everything", Opts{"prompt-file": valueOpt(promptFile), "prompt": valueOpt("p")}, []string{"pos"}, "in", "from file\n", nil},
		{"empty prompt file value falls through", Opts{"prompt-file": valueOpt(""), "prompt": valueOpt("p")}, nil, "in", "p", nil},
		{"bare prompt file is the Node path.resolve error", Opts{"prompt-file": flagOpt}, nil, "in", "", ErrPromptFileWithoutValue},
		{"prompt before positionals", Opts{"prompt": valueOpt("p")}, []string{"pos"}, "in", "p", nil},
		{"bare prompt is refused", Opts{"prompt": flagOpt}, nil, "in", "", ErrPromptWithoutValue},
		{"positionals joined by spaces", Opts{}, []string{"fix", "the", "bug"}, "in", "fix the bug", nil},
		{"stdin when it is not a terminal", Opts{}, nil, "from stdin", "from stdin", nil},
		{"empty stdin is an empty prompt, as in Node", Opts{}, nil, "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadPrompt(c.opts, c.positional, fileWithContent(t, c.stdin))
			if !errors.Is(err, c.wantErr) || got != c.want {
				t.Errorf("ReadPrompt = %q, %v; want %q, %v", got, err, c.want, c.wantErr)
			}
		})
	}
	t.Run("/dev/null is not a terminal", func(t *testing.T) {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer devNull.Close()
		if got, err := ReadPrompt(Opts{}, nil, devNull); err != nil || got != "" {
			t.Errorf("ReadPrompt(/dev/null) = %q, %v; want empty prompt", got, err)
		}
	})
}

// The bridges print these messages and exit 2, as the JS prints e.message of readFileSync.
func TestReadPromptFileErrorsHaveNodeMessages(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // Node names the physical path
	if err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "unreadable.md")
	if err := os.WriteFile(unreadable, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, file, want string }{
		{"ENOENT", filepath.Join(dir, "absent.md"), "ENOENT: no such file or directory, open '" + filepath.Join(dir, "absent.md") + "'"},
		{"EISDIR is a read error without a path", dir, "EISDIR: illegal operation on a directory, read"},
		{"EACCES", unreadable, "EACCES: permission denied, open '" + unreadable + "'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "EACCES" && os.Geteuid() == 0 {
				t.Skip("root reads files of mode 000")
			}
			_, err := ReadPrompt(Opts{"prompt-file": valueOpt(c.file)}, nil, fileWithContent(t, ""))
			if err == nil || err.Error() != c.want {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func fileWithContent(t *testing.T, content string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func assertEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}
