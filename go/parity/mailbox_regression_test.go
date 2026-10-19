//go:build parity

package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrashComparator(t *testing.T) {
	const expected = "Error: post needs from and subject"
	js := outcome{exitCode: 1, stderr: "source excerpt\n" + expected + "\n    at example:1\n"}
	good := outcome{exitCode: 1, stderr: expected + "\n"}
	if err := crashError(crashC1, expected, js, good); err != nil {
		t.Fatal(err)
	}
	for _, stderr := range []string{"", "\n", "    at example:1\n", "Error: wrong message\n", expected + "\nextra\n"} {
		bad := good
		bad.stderr = stderr
		if crashError(crashC1, expected, js, bad) == nil {
			t.Errorf("accepted stderr %q", stderr)
		}
	}
	for _, code := range []int{0, 2} {
		bad := good
		bad.exitCode = code
		if crashError(crashC1, expected, js, bad) == nil || crashError(crashC1, expected, bad, good) == nil {
			t.Errorf("accepted exit %d", code)
		}
	}
}

func TestMailboxNormalizationPreservesSeedAndContent(t *testing.T) {
	const file = "project/mailbox/messages.jsonl"
	seed := mailboxMessages
	literal := `2026-10-18T10:00:00.000Z 12345678-1234-4123-8123-123456789abc`
	appended := `{"id":"12345678-1234-4123-8123-123456789abc","at":"2026-10-18T21:00:00.000Z","subject":"` + literal + `","body":"` + literal + `"}` + "\n"
	normalize := func(content string) string {
		o := normalizeMailboxWrites(outcome{files: map[string]string{file: content}}, map[string]string{file: seed}, "post")
		return o.files[file]
	}
	good := normalize(seed + appended)
	if !strings.HasPrefix(good, seed) || strings.Count(good, literal) != 2 || !strings.Contains(good, `"at":"<iso>"`) {
		t.Fatalf("seed/content lost or fresh field not normalized: %s", good)
	}
	mutated := strings.Replace(seed+appended, "2026-10-18T10:00:00.000Z", "1999-01-01T00:00:00.000Z", 1)
	if normalize(mutated) == good {
		t.Fatal("normalizer hid a seeded timestamp mutation")
	}
	ack := `{"id":"12345678-1234-4123-8123-123456789abc","at":"2026-10-18T21:00:00.000Z","note":""}` + "\n"
	o := normalizeMailboxWrites(outcome{stdout: ack, files: map[string]string{"project/mailbox/acks.jsonl": ack}}, nil, "ack")
	want := strings.Replace(ack, "2026-10-18T21:00:00.000Z", "<iso>", 1)
	if o.stdout != want || o.files["project/mailbox/acks.jsonl"] != want {
		t.Fatal("normalizer changed an acknowledgement's deterministic id")
	}
}

func (h *harness) testMailboxAstralSpread(t *testing.T) {
	inv := h.mailboxInvocation(mailboxCase{args: []string{"list", "--json"}, seed: seedMailbox("\"😀\"\n", "")})
	js, orch := h.runPair(t, inv)
	if !strings.Contains(js.stdout, `"0": "\ud83d"`) || !strings.Contains(js.stdout, `"1": "\ude00"`) || strings.Count(orch.stdout, "�") != 2 {
		t.Fatalf("PE-1 representations not observed: js %q orch %q", js.stdout, orch.stdout)
	}
	js.stdout = strings.NewReplacer(`\ud83d`, "�", `\ude00`, "�").Replace(js.stdout)
	compareOutcomes(t, js, orch)
}

func (h *harness) testMailboxRegressions(t *testing.T) {
	for _, refs := range []string{`[""]`, `[null]`, `[[]]`} {
		t.Run("list/empty-joined-refs="+refs, func(t *testing.T) {
			h.comparePair(t, h.mailboxInvocation(mailboxCase{args: []string{"list"}, seed: seedMailbox(lines(`{"id":"x","kind":"note","refs":`+refs+`}`), "")}))
		})
	}
	for _, raw := range []string{"bad\xff", "prefix\xe2\x82end", "prefix\xf0\x9fend"} {
		t.Run("post/raw-argv="+raw, func(t *testing.T) {
			h.comparePair(t, h.mailboxInvocation(mailboxCase{
				args:       []string{"post", "--root", raw, "--from", raw, "--subject", raw, "--ref", raw, "--to", raw, "--thread", raw},
				extraFiles: []string{strings.ToValidUTF8(raw, "�") + "/.orchestrator/mailbox/messages.jsonl"},
			}))
		})
		t.Run("ack/raw-id="+raw, func(t *testing.T) {
			h.comparePair(t, h.mailboxInvocation(mailboxCase{args: []string{"ack", "--id", raw}, crash: crashC1, errorLine: "Error: no message with id " + strings.ToValidUTF8(raw, "�")}))
		})
	}
	post := []string{"post", "--from", "a", "--subject", "s"}
	longRoot := strings.Repeat("x", 300)
	cases := []mailboxCase{
		{name: "fs/long-root-component", args: append([]string{"post", "--root", longRoot}, post[1:]...), crash: crashC1,
			errorLine: "Error: ENAMETOOLONG: name too long, mkdir '$PROJECT/" + longRoot + "/.orchestrator/mailbox'"},
		{name: "fs/read-directory", args: []string{"list"}, crash: crashC1, errorLine: "Error: EISDIR: illegal operation on a directory, read", seed: func(t *testing.T, sb sandbox) {
			mustMkdir(t, filepath.Join(sb.project, ".orchestrator/mailbox/messages.jsonl"))
		}},
		{name: "fs/not-directory", args: post, crash: crashC1, errorLine: "Error: ENOTDIR: not a directory, mkdir '$PROJECT/.orchestrator/mailbox'", seed: func(t *testing.T, sb sandbox) {
			if err := os.WriteFile(filepath.Join(sb.project, ".orchestrator"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fs/open-directory", args: post, crash: crashC1, errorLine: "Error: EISDIR: illegal operation on a directory, open '$PROJECT/.orchestrator/mailbox/messages.jsonl'", seed: func(t *testing.T, sb sandbox) {
			mustMkdir(t, filepath.Join(sb.project, ".orchestrator/mailbox/messages.jsonl"))
		}},
		// Node's recursive mkdir stats an existing target through symlinks, so these errnos differ
		// from os.MkdirAll's (ENOTDIR, EEXIST, EEXIST, EEXIST).
		{name: "fs/mailbox-is-file", args: post, crash: crashC1, errorLine: "Error: EEXIST: file already exists, mkdir '$PROJECT/.orchestrator/mailbox'", seed: func(t *testing.T, sb sandbox) {
			mustMkdir(t, filepath.Join(sb.project, ".orchestrator"))
			if err := os.WriteFile(filepath.Join(sb.project, ".orchestrator/mailbox"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fs/mailbox-dangling-symlink", args: post, crash: crashC1, errorLine: "Error: ENOENT: no such file or directory, mkdir '$PROJECT/.orchestrator/mailbox'", seed: func(t *testing.T, sb sandbox) {
			mustSymlink(t, filepath.Join(sb.project, "missing"), filepath.Join(sb.project, ".orchestrator/mailbox"))
		}},
		{name: "fs/mailbox-symlink-loop", args: post, crash: crashC1, errorLine: "Error: ELOOP: too many symbolic links encountered, mkdir '$PROJECT/.orchestrator/mailbox'", seed: func(t *testing.T, sb sandbox) {
			loop := filepath.Join(sb.project, ".orchestrator/mailbox")
			mustSymlink(t, loop, loop)
		}},
		{name: "fs/orchestrator-dangling-symlink", args: post, crash: crashC1, errorLine: "Error: ENOENT: no such file or directory, mkdir '$PROJECT/.orchestrator/mailbox'", seed: func(t *testing.T, sb sandbox) {
			mustSymlink(t, filepath.Join(sb.project, "missing"), filepath.Join(sb.project, ".orchestrator"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h.comparePair(t, h.mailboxInvocation(c)) })
	}
	for _, operation := range []string{"read", "open", "mkdir"} {
		t.Run("fs/permission-"+operation, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root bypasses permission bits")
			}
			c := mailboxCase{args: post, crash: crashC1}
			c.seed = func(t *testing.T, sb sandbox) {
				dir := filepath.Join(sb.project, ".orchestrator/mailbox")
				mustMkdir(t, dir)
				target := filepath.Join(sb.project, "denied")
				mode := os.FileMode(0o400)
				if operation == "read" {
					mode = 0o200
				}
				if operation == "mkdir" {
					if err := os.Remove(dir); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(filepath.Dir(dir), 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.Chmod(filepath.Dir(dir), 0o700) })
					return
				}
				if err := os.WriteFile(target, []byte("{}\n"), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "messages.jsonl")); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "read" {
				c.args = []string{"list"}
			}
			c.errorLine = "Error: EACCES: permission denied, open '$PROJECT/.orchestrator/mailbox/messages.jsonl'"
			if operation == "mkdir" {
				c.errorLine = "Error: EACCES: permission denied, mkdir '$PROJECT/.orchestrator/mailbox'"
			}
			h.comparePair(t, h.mailboxInvocation(c))
		})
	}
}

// mustSymlink creates link pointing at target, creating link's parent directory first.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
