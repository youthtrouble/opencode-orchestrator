package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
)

// t0 is the fixed clock of every tick test, in epoch seconds.
const t0 = 1_800_000_000

// fixture is a temp project with a supervisor whose every dependency is injected: a fixed
// clock, a map of live pids, a recording RunSync and incident log.
type fixture struct {
	t         *testing.T
	root      string
	sup       *Supervisor
	now       time.Time
	live      map[float64]bool
	runs      []SyncSpec
	runResult SyncResult
	// respond, when set, answers each RunSync instead of runResult.
	respond   func(SyncSpec) SyncResult
	spawns    []spawnCall
	spawnErr  error
	incidents []incident.Draft
	ids       int
	// onMailbox runs each time the supervisor opens the mailbox, before it posts.
	onMailbox func()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, now: time.Unix(t0, 0), live: map[float64]bool{}}
	f.sup = New(NewProject(root), Deps{
		Now:   func() time.Time { return f.now },
		Pid:   4242,
		Self:  "/bin/orch",
		Alive: func(pid any) bool { n, _ := pid.(float64); return f.live[n] },
		RunSync: func(spec SyncSpec) SyncResult {
			f.runs = append(f.runs, spec)
			if f.respond != nil {
				return f.respond(spec)
			}
			return f.runResult
		},
		Spawn: func(name string, args []string, dir string) error {
			f.spawns = append(f.spawns, spawnCall{name, args, dir})
			return f.spawnErr
		},
		Mailbox: func() *mailbox.Mailbox {
			if f.onMailbox != nil {
				f.onMailbox()
			}
			box := mailbox.New(root)
			box.Now = func() time.Time { return f.now }
			box.NewID = func() string { f.ids++; return fmt.Sprintf("msg-%d", f.ids) }
			return box
		},
		Incident: func(d incident.Draft) { f.incidents = append(f.incidents, d) },
	})
	return f
}

// spawnCall is one recorded Spawn.
type spawnCall struct {
	Name string
	Args []string
	Dir  string
}

// at sets the clock to t0 + seconds (fractions allowed).
func (f *fixture) at(seconds float64) {
	f.now = time.UnixMilli(int64((t0 + seconds) * 1000))
}

func (f *fixture) path(rel string) string {
	return filepath.Join(f.root, ".orchestrator", rel)
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	path := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(f.path(rel), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(f.path(rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

// worker writes workers/<lane>.json with the given fields and a run id.
func (f *fixture) worker(lane, fields string) {
	f.write("workers/"+lane+".json", fmt.Sprintf(`{"run_id": "run-%s", %s}`, lane, fields))
}

// tick runs one Tick over state (a JSON text) and returns the mutated state and the error.
func (f *fixture) tick(state string) (*jsjson.Object, error) {
	f.t.Helper()
	value, err := jsjson.Unmarshal([]byte(state))
	if err != nil {
		f.t.Fatal(err)
	}
	object := value.(*jsjson.Object)
	return object, f.sup.Tick(object)
}

// mustTick is tick that fails the test on a tick error.
func (f *fixture) mustTick(state string) *jsjson.Object {
	f.t.Helper()
	object, err := f.tick(state)
	if err != nil {
		f.t.Fatalf("Tick: %v", err)
	}
	return object
}

func (f *fixture) messages() []string {
	data, err := os.ReadFile(f.path("mailbox/messages.jsonl"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func indent(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.MarshalIndent(v, "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func compact(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// iso is iso(t0 + seconds).
func iso(seconds int64) string {
	return jsjson.ISOString(time.Unix(t0+seconds, 0))
}

// deliverWith makes every channel answer with the given status.
func (f *fixture) deliverWith(status string) {
	f.sup.deliver = func(channel any, _ string) *jsjson.Object {
		result := jsjson.NewObject()
		result.Set("status", status)
		return result
	}
}

func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// wantRenameOntoDirError checks the EISDIR a private write onto the directory at path gives:
// it names orch's own `<path>.tmp-<random>`, which is removed (PE-21: the JS names and leaves
// `<path>.tmp`).
func wantRenameOntoDirError(t *testing.T, err error, path string) {
	t.Helper()
	pattern := "^EISDIR: illegal operation on a directory, rename '" + regexp.QuoteMeta(path) + `\.tmp-[0-9]+' -> '` + regexp.QuoteMeta(path) + "'$"
	if err == nil || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("error = %v, want a match for %s", err, pattern)
	}
	if temps, _ := filepath.Glob(path + ".tmp*"); len(temps) > 0 {
		t.Errorf("temps left by the failed rename: %v", temps)
	}
}
