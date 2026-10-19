// Package usage gathers Claude and Codex subscription usage, caches it in the usage file the
// JS runtime shares (lib/usage.mjs), and resolves routing from it.
//
// Every document is a jsjson value so a cache written by either runtime reads the same in
// the other, byte for byte.
package usage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

// MaxCacheAge is how old a cached usage document may be before readers re-collect it.
const MaxCacheAge = 5 * time.Minute

// Config locates what usage reads and writes. Programs (security, sqlite3, opencode, codex)
// are looked up on PATH, as the JS does.
type Config struct {
	Home           string // credential stores and opencode's data dir live under it
	UsageFile      string
	RegistryFile   string
	ClaudeUsageURL string
	ClaudeTimeout  time.Duration
	CodexBin       string
	CodexTimeout   time.Duration
	Now            func() time.Time
}

// NewConfig is the production configuration for the resolved paths.
func NewConfig(p paths.Paths) Config {
	return Config{
		Home:           p.Home,
		UsageFile:      p.UsageFile,
		RegistryFile:   p.RegistryFile,
		ClaudeUsageURL: ClaudeUsageURL,
		ClaudeTimeout:  claudeFetchTimeout,
		CodexBin:       "codex",
		CodexTimeout:   codexAppServerTimeout,
		Now:            time.Now,
	}
}

// Collect mirrors collectUsage: both feeds are read concurrently and generated_at is stamped
// once both are done. Only a codex spawn failure is an error (see SpawnError).
func Collect(cfg Config) (*jsjson.Object, error) {
	var claude *jsjson.Object
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		claude = ReadClaudeUsage(cfg)
	}()
	account, codexErr := ReadCodexAccountAndLimits(cfg.CodexBin, cfg.CodexTimeout)
	wg.Wait()

	var codex *jsjson.Object
	var spawnErr *SpawnError
	switch {
	case errors.As(codexErr, &spawnErr):
		return nil, codexErr
	case codexErr != nil:
		codex = failure(codexErr.Error())
	default:
		codex = NormalizeCodex(account)
	}
	usage := jsjson.NewObject()
	usage.Set("generated_at", jsjson.ISOString(cfg.Now()))
	usage.Set("claude", claude)
	usage.Set("codex", codex)
	return usage, nil
}

// ReadCached mirrors readCachedUsage: the cached document, a copy with "stale": true when it
// is older than maxAge (or its generated_at does not parse) or any routing window in it has
// reset since it was generated (see anyWindowResetSince), nil when there is none, it is not
// JSON or it is JSON null.
func ReadCached(cfg Config, maxAge time.Duration) *jsjson.Object {
	data, err := readJSONFile(cfg.UsageFile)
	if err != nil || data == nil {
		return nil
	}
	now := cfg.Now().UnixMilli()
	generatedAt, _ := jsjson.Path(data, "generated_at").(string)
	if at, ok := jsjson.ParseDate(generatedAt); ok && now-at <= maxAge.Milliseconds() && !anyWindowResetSince(data, at, now) {
		return data.(*jsjson.Object) // only an object can hold a parsable generated_at
	}
	return withStale(data)
}

// routingWindows are the paths of the windows whose postures gate routing (see
// routing.VendorPostures). Once any of them has reset, a cached reading misreports it (a red
// session that is now empty), so it is stale.
var routingWindows = [][]string{
	{"claude", "session"},
	{"claude", "weekly"},
	{"claude", "weekly_by_model", "fable"},
	{"codex", "five_hour"},
	{"codex", "weekly"},
}

// anyWindowResetSince mirrors the JS of the same name: whether a routing window reset after
// since (the reading's generated_at) and at or before now, both epoch milliseconds. A reset at
// or before since was already behind the collection that wrote the reading: either the reading
// is post-reset, or the collection failed and carried an old window forward as last-good
// (read_error), in which case the retry waits for maxAge instead of re-collecting on every
// read. An absent or unparsable resets_at never counts.
func anyWindowResetSince(usage any, since, now int64) bool {
	for _, window := range routingWindows {
		resetsAt, isString := jsjson.Path(jsjson.Path(usage, window...), "resets_at").(string)
		if at, ok := jsjson.ParseDate(resetsAt); isString && ok && at > since && at <= now {
			return true
		}
	}
	return false
}

// withStale is `{...data, stale: true}`. A document that is not an object (a malformed
// cache) contributes no keys; the JS would spread the indices of an array or string.
func withStale(data any) *jsjson.Object {
	out := jsjson.NewObject()
	if obj, isObject := data.(*jsjson.Object); isObject {
		for _, key := range obj.Keys() {
			value, _ := obj.Get(key)
			out.Set(key, value)
		}
	}
	out.Set("stale", true)
	return out
}

// LastGoodWindow is how long a vendor's last good cached reading stands in for a failed one.
const LastGoodWindow = 30 * time.Minute

// Get mirrors getUsage: a fresh cached document unless refresh is set, else a new collection,
// with each failed vendor reading replaced by the last good one (see keepLastGood), written to
// the cache (indent 2, no trailing newline; see writeCache).
func Get(cfg Config, refresh bool) (*jsjson.Object, error) {
	cached := ReadCached(cfg, MaxCacheAge)
	if !refresh && cached != nil && !jsjson.Truthy(jsjson.Path(cached, "stale")) {
		return cached, nil
	}
	usage, err := Collect(cfg)
	if err != nil {
		return nil, err
	}
	keepLastGood(usage, cached, cfg.Now())
	if err := writeCache(cfg.UsageFile, usage); err != nil {
		return nil, err
	}
	return usage, nil
}

// keepLastGood stops a failed read (an HTTP 429 from the usage endpoint, say) from making a
// vendor look unavailable: when the fresh reading failed and the cached one was ok in a cache
// generated less than LastGoodWindow ago, the cached entry is kept, as
// `{...last, last_good_at: last.last_good_at || cached.generated_at, read_error: fresh.error}`.
// The window runs from the cache's generated_at, not from last_good_at, and every failed
// refresh renews generated_at, so failures less than 30 minutes apart keep a reading of any
// age. That is the JS behaviour, mirrored on purpose (see the S3 report).
func keepLastGood(usage, cached *jsjson.Object, now time.Time) {
	if cached == nil {
		return
	}
	generatedAt, _ := jsjson.Path(cached, "generated_at").(string)
	at, parsed := jsjson.ParseDate(generatedAt)
	if !parsed || now.UnixMilli()-at >= LastGoodWindow.Milliseconds() {
		return
	}
	for _, vendor := range []string{"claude", "codex"} {
		fresh, _ := jsjson.Path(usage, vendor).(*jsjson.Object)
		last, _ := jsjson.Path(cached, vendor).(*jsjson.Object)
		if fresh == nil || jsjson.Truthy(jsjson.Path(fresh, "ok")) || last == nil || !jsjson.Truthy(jsjson.Path(last, "ok")) {
			continue
		}
		kept := jsjson.NewObject()
		for _, key := range last.Keys() {
			value, _ := last.Get(key)
			kept.Set(key, value)
		}
		if lastGoodAt := jsjson.Path(last, "last_good_at"); jsjson.Truthy(lastGoodAt) {
			kept.Set("last_good_at", lastGoodAt)
		} else {
			kept.Set("last_good_at", generatedAt)
		}
		kept.Set("read_error", jsjson.Path(fresh, "error")) // every failed reading carries an error string
		usage.Set(vendor, kept)
	}
}

// writeCache publishes the document atomically: it is written to a temp file of its own in
// the cache directory and renamed over the cache. The JS uses the fixed name usage.json.tmp;
// a unique name keeps concurrent writers (Go or JS) from truncating or renaming each other's
// temp file. The cache's bytes and mode (0666 less the umask) are the JS's.
func writeCache(file string, usage *jsjson.Object) error {
	data, err := jsjson.MarshalIndent(usage, "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		return fmt.Errorf("usage cache: %w", err)
	}
	tmp, err := createUniqueTemp(file)
	if err != nil {
		return fmt.Errorf("usage cache: %w", err)
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("usage cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("usage cache: %w", err)
	}
	return nil
}

// tempSequence numbers the temp files of this process.
var tempSequence atomic.Uint64

// createUniqueTemp creates `<file>.<pid>.<n>.tmp` exclusively, mode 0666 less the umask
// (os.CreateTemp would force 0600), skipping names a crashed writer left behind.
func createUniqueTemp(file string) (*os.File, error) {
	for {
		name := fmt.Sprintf("%s.%d.%d.tmp", file, os.Getpid(), tempSequence.Add(1))
		tmp, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if !errors.Is(err, fs.ErrExist) {
			return tmp, err
		}
	}
}

// Routing is getRouting's result: the usage document, the registry, the native channels,
// the resolved roles and the prompt summary of them.
type Routing struct {
	Usage    *jsjson.Object
	Registry *jsjson.Object
	Channels Channels
	Resolved routing.Resolved
	Prompt   string
}

// JSON returns {usage, registry, channels, resolved, prompt}.
func (r *Routing) JSON() *jsjson.Object {
	out := jsjson.NewObject()
	out.Set("usage", r.Usage)
	out.Set("registry", r.Registry)
	out.Set("channels", r.Channels.JSON())
	out.Set("resolved", r.Resolved.JSON())
	out.Set("prompt", r.Prompt)
	return out
}

// GetRouting mirrors getRouting: load the registry, get usage (see Get), detect channels and
// resolve the roles.
func GetRouting(cfg Config, refresh bool) (*Routing, error) {
	reg, err := registry.Load(cfg.RegistryFile)
	if err != nil {
		return nil, err
	}
	usage, err := Get(cfg, refresh)
	if err != nil {
		return nil, err
	}
	return resolve(cfg, reg, usage), nil
}

// ErrNoCache reports that there is no usage cache to route from.
var ErrNoCache = errors.New("no cached usage")

// CachedRouting resolves routing from the cache as it is, never collecting: a stale cache is
// used with "stale": true set. It returns ErrNoCache when there is no usable cache.
func CachedRouting(cfg Config) (*Routing, error) {
	reg, err := registry.Load(cfg.RegistryFile)
	if err != nil {
		return nil, err
	}
	usage := ReadCached(cfg, MaxCacheAge)
	if usage == nil {
		return nil, ErrNoCache
	}
	return resolve(cfg, reg, usage), nil
}

func resolve(cfg Config, reg, usage *jsjson.Object) *Routing {
	channels := DetectChannels(cfg.Home)
	resolved := routing.ResolveRoles(usage, reg, cfg.Now(), channels.ByVendor())
	return &Routing{
		Usage:    usage,
		Registry: reg,
		Channels: channels,
		Resolved: resolved,
		Prompt:   routing.SummarizeForPrompt(resolved, usage),
	}
}
