package usage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

const (
	sqliteTimeout   = 5 * time.Second
	authListTimeout = 15 * time.Second
)

// Channels records which vendors are natively authenticated inside opencode (as opposed to
// reachable only through their CLI).
type Channels struct {
	Anthropic bool
	OpenAI    bool
}

// ByVendor is the form routing.ResolveRoles takes.
func (c Channels) ByVendor() map[string]bool {
	return map[string]bool{routing.Anthropic: c.Anthropic, routing.OpenAI: c.OpenAI}
}

// JSON returns {anthropic, openai}.
func (c Channels) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set(routing.Anthropic, c.Anthropic)
	out.Set(routing.OpenAI, c.OpenAI)
	return out
}

var (
	mentionsOpenAI    = regexp.MustCompile(`(?i)openai`)
	mentionsAnthropic = regexp.MustCompile(`(?i)anthropic`)
)

// DetectChannels mirrors detectChannels. opencode v2 keeps credentials in SQLite: when its
// database exists the credential table is queried with sqlite3, falling back to `opencode
// auth list` (the schema may differ); v1's auth.json is read when the database is missing
// or both of those fail.
func DetectChannels(home string) Channels {
	share := filepath.Join(home, ".local", "share", "opencode")
	db := filepath.Join(share, "opencode.db")
	if _, err := os.Stat(db); err == nil {
		if out, err := runQuiet("sqlite3", []string{db, "select distinct integration_id from credential where active = 1 or active is null"}, sqliteTimeout); err == nil {
			return channelsFromIntegrationIDs(out)
		}
		if out, err := runQuiet("opencode", []string{"auth", "list"}, authListTimeout); err == nil {
			return Channels{Anthropic: mentionsAnthropic.MatchString(out), OpenAI: mentionsOpenAI.MatchString(out)}
		}
	}
	return channelsFromAuthFile(filepath.Join(share, "auth.json"))
}

func channelsFromIntegrationIDs(out string) Channels {
	var channels Channels
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case routing.Anthropic:
			channels.Anthropic = true
		case routing.OpenAI:
			channels.OpenAI = true
		}
	}
	return channels
}

func channelsFromAuthFile(file string) Channels {
	data, err := readJSONFile(file)
	if err != nil {
		return Channels{}
	}
	return Channels{
		Anthropic: jsjson.Truthy(jsjson.Path(data, routing.Anthropic)),
		OpenAI:    jsjson.Truthy(jsjson.Path(data, routing.OpenAI)),
	}
}
