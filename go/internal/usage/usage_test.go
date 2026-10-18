package usage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const cachedDoc = `{
  "generated_at": "%s",
  "claude": {
    "ok": false,
    "error": "cached"
  },
  "codex": {
    "ok": false,
    "error": "cached"
  }
}`

func writeCacheAt(t *testing.T, s *sandbox, cfg Config, generatedAt string) {
	t.Helper()
	s.writeFile(t, strings.TrimPrefix(cfg.UsageFile, s.home+"/"), strings.Replace(cachedDoc, "%s", generatedAt, 1))
}

func TestReadCached(t *testing.T) {
	cases := []struct {
		name    string
		content string // "" leaves no cache file
		want    string // compact JSON, "null" for no cache
	}{
		{"missing", "", "null"},
		{"not JSON", "{", "null"},
		{"JSON null", "null", "null"},
		{"fresh", `{"generated_at":"2026-10-18T19:56:00.000Z","x":1}`, `{"generated_at":"2026-10-18T19:56:00.000Z","x":1}`},
		{"exactly five minutes old is fresh", `{"generated_at":"2026-10-18T19:55:00.000Z"}`, `{"generated_at":"2026-10-18T19:55:00.000Z"}`},
		{"older is stale", `{"generated_at":"2026-10-18T19:54:59.999Z","x":1}`, `{"generated_at":"2026-10-18T19:54:59.999Z","x":1,"stale":true}`},
		{"stale keeps the position of an existing stale key", `{"stale":false,"generated_at":"2020-01-01T00:00:00.000Z"}`, `{"stale":true,"generated_at":"2020-01-01T00:00:00.000Z"}`},
		{"unparsable generated_at is stale", `{"generated_at":"yesterday"}`, `{"generated_at":"yesterday","stale":true}`},
		{"a non-object document is stale", `[1]`, `{"stale":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			cfg := s.config("")
			if c.content != "" {
				s.writeFile(t, strings.TrimPrefix(cfg.UsageFile, s.home+"/"), c.content)
			}
			got := "null"
			if cached := ReadCached(cfg, MaxCacheAge); cached != nil {
				got = compact(t, cached)
			}
			if got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestGetUsesAFreshCacheWithoutCollecting(t *testing.T) {
	s := newSandbox(t)
	cfg := s.config("")
	writeCacheAt(t, s, cfg, "2026-10-18T19:59:00.000Z")
	s.fake(t, "codex", "touch "+s.home+"/codex-was-run; exit 97")

	got, err := Get(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("generated_at"); v != "2026-10-18T19:59:00.000Z" {
		t.Errorf("generated_at %v", v)
	}
	if _, err := os.Stat(s.home + "/codex-was-run"); err == nil {
		t.Error("codex ran although the cache was fresh")
	}
}

func TestGetCollectsAndWritesTheCacheLikeJS(t *testing.T) {
	for _, c := range []struct {
		name    string
		refresh bool
		cached  string // generated_at of an existing cache, "" for none
	}{
		{"no cache", false, ""},
		{"stale cache", false, "2026-10-18T19:00:00.000Z"},
		{"refresh ignores a fresh cache", true, "2026-10-18T19:59:00.000Z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			cfg := s.config("")
			if c.cached != "" {
				writeCacheAt(t, s, cfg, c.cached)
			}
			fakeAppServer(t, s, map[int]string{
				1: `{"id":1,"result":{}}`,
				3: rpcResult(t, 2, "codex-account.json"),
				4: rpcResult(t, 3, "codex-ratelimits.json"),
			})
			got, err := Get(cfg, c.refresh)
			if err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(cfg.UsageFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != indented(t, got) {
				t.Errorf("cache file is not the returned document, indent 2, without a trailing newline:\n%q", written)
			}
			want := `{"generated_at":"2026-10-18T20:00:00.000Z","claude":{"ok":false,"error":"` + noClaudeTokenError + `"},"codex":` + compact(t, NormalizeCodex(CodexAccount{Account: readFixture(t, "codex-account.json"), Limits: readFixture(t, "codex-ratelimits.json")})) + "}"
			if compact(t, got) != want {
				t.Errorf("got  %s\nwant %s", compact(t, got), want)
			}
			assertNoTempFiles(t, cfg.UsageFile)
		})
	}
}

func TestCollectRecordsACodexFailureButNotASpawnFailure(t *testing.T) {
	s := newSandbox(t)
	cfg := s.config("")
	fakeAppServer(t, s, map[int]string{1: `{"id":1,"error":{"message":"not logged in"}}`})
	got, err := Collect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if codex := compact(t, jsjson.Path(got, "codex")); codex != `{"ok":false,"error":"not logged in"}` {
		t.Errorf("codex entry %s", codex)
	}

	cfg.CodexBin = "codex-not-installed"
	var spawnErr *SpawnError
	if _, err := Collect(cfg); !errors.As(err, &spawnErr) {
		t.Errorf("error %v, want a SpawnError", err)
	}
	if _, err := Get(cfg, true); !errors.As(err, &spawnErr) {
		t.Errorf("Get error %v, want a SpawnError", err)
	}
	if _, err := os.Stat(cfg.UsageFile); err == nil {
		t.Error("a spawn failure wrote the cache")
	}
}

func TestCachedRoutingNeverCollects(t *testing.T) {
	s := newSandbox(t)
	cfg := s.config("")
	if _, err := CachedRouting(cfg); !errors.Is(err, ErrNoCache) {
		t.Fatalf("error %v, want ErrNoCache", err)
	}
	writeCacheAt(t, s, cfg, "2026-10-18T10:00:00.000Z")
	r, err := CachedRouting(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stale, _ := r.Usage.Get("stale"); stale != true {
		t.Errorf("stale = %v, want true", stale)
	}
	if !strings.HasPrefix(r.Prompt, "Claude usage: unavailable (cached).") {
		t.Errorf("prompt %q", r.Prompt)
	}
	keys := r.JSON().Keys()
	if strings.Join(keys, ",") != "usage,registry,channels,resolved,prompt" {
		t.Errorf("keys %v", keys)
	}
}

func TestGetRoutingFailsWithoutARegistry(t *testing.T) {
	s := newSandbox(t)
	cfg := s.config("")
	cfg.RegistryFile = s.home + "/missing.json"
	if _, err := GetRouting(cfg, false); err == nil {
		t.Error("no error")
	}
}

func TestGetKeepsTheLastGoodReadingForThirtyMinutes(t *testing.T) {
	const okClaude = `{"ok":true,"source":"cached","plan":"max"}`
	freshClaude := `{"ok":false,"error":"` + noClaudeTokenError + `"}`
	freshCodex := `{"ok":false,"error":"not logged in"}`
	cases := []struct {
		name        string
		generatedAt string
		claude      string // the cached claude entry
		codex       string // the cached codex entry
		wantClaude  string
		wantCodex   string
	}{
		{
			name:        "within 30 minutes the cached ok entry is kept with its age and the read error",
			generatedAt: "2026-10-18T19:30:00.001Z",
			claude:      okClaude, codex: freshCodex,
			wantClaude: `{"ok":true,"source":"cached","plan":"max","last_good_at":"2026-10-18T19:30:00.001Z","read_error":"` + noClaudeTokenError + `"}`,
			wantCodex:  freshCodex,
		},
		{
			name:        "at 30 minutes the failed reading stands",
			generatedAt: "2026-10-18T19:30:00.000Z",
			claude:      okClaude, codex: okClaude,
			wantClaude: freshClaude, wantCodex: freshCodex,
		},
		{
			name:        "an existing last_good_at is carried forward and read_error keeps its position",
			generatedAt: "2026-10-18T19:59:00.000Z",
			claude:      `{"ok":true,"read_error":"429","last_good_at":"2026-10-18T19:40:00.000Z","plan":"max"}`,
			codex:       `{"ok":true,"last_good_at":""}`,
			wantClaude:  `{"ok":true,"read_error":"` + noClaudeTokenError + `","last_good_at":"2026-10-18T19:40:00.000Z","plan":"max"}`,
			wantCodex:   `{"ok":true,"last_good_at":"2026-10-18T19:59:00.000Z","read_error":"not logged in"}`,
		},
		{
			name:        "a cached entry that was not ok is not kept",
			generatedAt: "2026-10-18T19:59:00.000Z",
			claude:      `{"ok":false,"error":"cached"}`, codex: `{"error":"no ok key"}`,
			wantClaude: freshClaude, wantCodex: freshCodex,
		},
		{
			name:        "an unparsable generated_at keeps nothing",
			generatedAt: "yesterday",
			claude:      okClaude, codex: okClaude,
			wantClaude: freshClaude, wantCodex: freshCodex,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			cfg := s.config("")
			s.writeFile(t, strings.TrimPrefix(cfg.UsageFile, s.home+"/"),
				`{"generated_at":"`+c.generatedAt+`","claude":`+c.claude+`,"codex":`+c.codex+`}`)
			fakeAppServer(t, s, map[int]string{1: `{"id":1,"error":{"message":"not logged in"}}`})

			got, err := Get(cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"generated_at":"2026-10-18T20:00:00.000Z","claude":` + c.wantClaude + `,"codex":` + c.wantCodex + `}`
			if compact(t, got) != want {
				t.Errorf("got  %s\nwant %s", compact(t, got), want)
			}
			written, err := os.ReadFile(cfg.UsageFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != indented(t, got) {
				t.Error("the cache file is not the returned document")
			}
		})
	}
}

func TestGetPrefersAFreshOkReadingToTheCachedOne(t *testing.T) {
	s := newSandbox(t)
	cfg := s.config("")
	s.writeFile(t, strings.TrimPrefix(cfg.UsageFile, s.home+"/"),
		`{"generated_at":"2026-10-18T19:59:00.000Z","claude":{"ok":true},"codex":{"ok":true,"plan":"cached"}}`)
	fakeAppServer(t, s, map[int]string{
		1: `{"id":1,"result":{}}`,
		3: rpcResult(t, 2, "codex-account.json"),
		4: rpcResult(t, 3, "codex-ratelimits.json"),
	})

	got, err := Get(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	wantCodex := compact(t, NormalizeCodex(CodexAccount{Account: readFixture(t, "codex-account.json"), Limits: readFixture(t, "codex-ratelimits.json")}))
	if codex := compact(t, jsjson.Path(got, "codex")); codex != wantCodex {
		t.Errorf("codex %s, want the fresh reading %s", codex, wantCodex)
	}
	if _, kept := jsjson.Lookup(got, "claude", "last_good_at"); !kept {
		t.Error("the failed claude reading did not keep the cached one")
	}
}

// Concurrent writers each publish a complete document of their own: none fails and the cache
// is one writer's document, never a mix or a truncation.
func TestWriteCacheConcurrentWritersNeverCollide(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "usage.json")
	const writers = 64
	docs := make([]*jsjson.Object, writers)
	for i := range docs {
		docs[i] = jsjson.NewObject()
		docs[i].Set("writer", float64(i))
		docs[i].Set("padding", strings.Repeat("x", 256<<10))
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for _, doc := range docs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- writeCache(file, doc)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsjson.Unmarshal(written)
	if err != nil {
		t.Fatalf("cache is not one complete document: %v", err)
	}
	writer, _ := jsjson.Path(value, "writer").(float64)
	if want := indented(t, docs[int(writer)]); string(written) != want {
		t.Error("cache is not exactly the document of the writer it names")
	}
	assertNoTempFiles(t, file)
}

func TestWriteCacheRemovesItsTempFileWhenTheRenameFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "usage.json")
	if err := os.MkdirAll(filepath.Join(file, "occupied"), 0o755); err != nil { // a non-empty directory cannot be renamed over
		t.Fatal(err)
	}
	if err := writeCache(file, jsjson.NewObject()); err == nil {
		t.Fatal("no error")
	}
	assertNoTempFiles(t, file)
}

func TestWriteCacheKeepsTheJSFileMode(t *testing.T) {
	umask := syscall.Umask(0o022)
	defer syscall.Umask(umask)
	file := filepath.Join(t.TempDir(), "usage.json")
	if err := writeCache(file, jsjson.NewObject()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("mode %v, want 0644 (0666 less the umask, as fs.writeFileSync)", mode)
	}
}

// assertNoTempFiles checks that no writer's temp file is left beside the cache.
func assertNoTempFiles(t *testing.T, cacheFile string) {
	t.Helper()
	leftovers, err := filepath.Glob(cacheFile + ".*tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) > 0 {
		t.Errorf("temp files left behind: %s", fmt.Sprint(leftovers))
	}
}

// The 30 minutes are measured from the previous document's generated_at, which every failed
// refresh renews, so failures less than 30 minutes apart keep a reading of any age. This is
// the JS behaviour (getUsage at bf1af64), mirrored on purpose; a hard cap on last_good_at
// needs a coordinated JS and Go change.
func TestGetRollsTheLastGoodWindowForwardOnRepeatedFailures(t *testing.T) {
	s := newSandbox(t)
	cfg := s.config("")
	s.writeFile(t, strings.TrimPrefix(cfg.UsageFile, s.home+"/"),
		`{"generated_at":"2026-10-18T19:45:00.000Z","claude":{"ok":true,"plan":"max"},"codex":{"ok":false,"error":"cached"}}`)
	fakeAppServer(t, s, map[int]string{1: `{"id":1,"error":{"message":"not logged in"}}`})

	for _, now := range []time.Time{fixedNow, fixedNow.Add(25 * time.Minute), fixedNow.Add(50 * time.Minute)} {
		cfg.Now = func() time.Time { return now }
		got, err := Get(cfg, true)
		if err != nil {
			t.Fatal(err)
		}
		if lastGoodAt := jsjson.Path(got, "claude", "last_good_at"); lastGoodAt != "2026-10-18T19:45:00.000Z" {
			t.Errorf("at %s: claude last_good_at %v, want the 19:45 reading kept", jsjson.ISOString(now), lastGoodAt)
		}
	}
}
