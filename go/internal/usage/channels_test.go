package usage

import (
	"os/exec"
	"path/filepath"
	"testing"
)

const opencodeShare = ".local/share/opencode"

// createDB builds opencode.db under the sandbox HOME with the real sqlite3.
func createDB(t *testing.T, s *sandbox, sql string) {
	t.Helper()
	s.writeFile(t, opencodeShare+"/.keep", "")
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}
	if out, err := exec.Command(sqlite, filepath.Join(s.home, opencodeShare, "opencode.db"), sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
}

const credentialTable = "create table credential (integration_id text, active integer);"

func TestDetectChannels(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *sandbox)
		want  Channels
	}{
		{"nothing installed", func(t *testing.T, s *sandbox) {}, Channels{}},
		{"credential rows that are active or unmarked count", func(t *testing.T, s *sandbox) {
			s.linkReal(t, "sqlite3")
			createDB(t, s, credentialTable+`insert into credential values ('anthropic', 1), ('openai', null), ('google', 1);`)
		}, Channels{Anthropic: true, OpenAI: true}},
		{"inactive rows do not count and auth.json is not consulted", func(t *testing.T, s *sandbox) {
			s.linkReal(t, "sqlite3")
			createDB(t, s, credentialTable+`insert into credential values ('anthropic', 0);`)
			s.writeFile(t, opencodeShare+"/auth.json", `{"anthropic":{},"openai":{}}`)
		}, Channels{}},
		{"another schema falls back to opencode auth list", func(t *testing.T, s *sandbox) {
			s.linkReal(t, "sqlite3")
			createDB(t, s, `create table credential (provider text);`)
			s.fake(t, "opencode", `[ "$*" = "auth list" ] || exit 9`+"\necho '  OpenAI  oauth'")
		}, Channels{OpenAI: true}},
		{"sqlite3 and opencode both failing fall back to auth.json", func(t *testing.T, s *sandbox) {
			s.fake(t, "sqlite3", "exit 1")
			s.fake(t, "opencode", "exit 1")
			s.writeFile(t, opencodeShare+"/opencode.db", "")
			s.writeFile(t, opencodeShare+"/auth.json", `{"anthropic":{"type":"oauth"},"openai":null}`)
		}, Channels{Anthropic: true}},
		{"no database reads auth.json", func(t *testing.T, s *sandbox) {
			s.writeFile(t, opencodeShare+"/auth.json", `{"openai":{"type":"oauth"}}`)
		}, Channels{OpenAI: true}},
		{"unparsable auth.json reads as nothing", func(t *testing.T, s *sandbox) {
			s.writeFile(t, opencodeShare+"/auth.json", `{`)
		}, Channels{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			c.setup(t, s)
			if got := DetectChannels(s.home); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
