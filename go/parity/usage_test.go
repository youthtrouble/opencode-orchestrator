//go:build parity

package parity

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

const usageTestdata = "../testdata/usage"

// testUsage compares `orch usage` with bin/usage.mjs over cache fixtures, channel sources,
// re-collection through the scripted codex fake, the cache round trip in both directions,
// and the plugin-facing routing, budget gate and effort commands with the JS functions
// they replace. The Claude feed is always token-less here (the security fake has no
// keychain entry), so the real usage endpoint is never contacted.
func (h *harness) testUsage(t *testing.T) {
	h.testNestedProjectRoot(t, "usage")
	caches := readCacheFixtures(t)
	for _, name := range caches.Keys() {
		if name == "$comment" {
			continue
		}
		fixture, _ := caches.Get(name)
		for _, args := range [][]string{{"--json"}, {}} {
			t.Run(fmt.Sprintf("cache=%s/args=%v", name, args), func(t *testing.T) {
				h.comparePair(t, h.usageInvocation(args, seedCache(fixture, time.Now())))
			})
		}
	}
	t.Run("channels", func(t *testing.T) { h.testUsageChannels(t, caches) })
	t.Run("collect", h.testUsageCollect)
	t.Run("round-trip", h.testUsageCacheRoundTrip)
	t.Run("routing", func(t *testing.T) { h.testRoutingCommand(t, caches) })
	t.Run("budget-gate", func(t *testing.T) { h.testBudgetGate(t, caches) })
	t.Run("effort", h.testEffort)
}

func (h *harness) testUsageChannels(t *testing.T, caches *jsjson.Object) {
	green, _ := caches.Get("green")
	const table = "create table credential (integration_id text, active integer);"
	cases := map[string]seed{
		"db-rows":          seedDB(table + "insert into credential values ('anthropic', 1), ('openai', null), ('google', 1);"),
		"db-inactive":      seedDB(table + "insert into credential values ('openai', 0);"),
		"db-other-schema":  seeds(seedDB("create table credential (provider text);"), seedFile("fake-opencode/auth-list.txt", "Credentials\n  OpenAI oauth\n")),
		"db-unreadable":    seeds(seedDB("create table credential (provider text);"), seedFile(".local/share/opencode/auth.json", `{"anthropic":{"type":"api"}}`)),
		"auth-json-only":   seedFile(".local/share/opencode/auth.json", `{"openai":{"type":"oauth"},"anthropic":null}`),
		"auth-json-broken": seedFile(".local/share/opencode/auth.json", `{`),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			h.comparePair(t, h.usageInvocation([]string{"--json"}, seeds(seedCache(green, time.Now()), s)))
		})
	}
}

// testUsageCollect covers re-collection: no cache, a stale cache and --refresh, a codex RPC
// error, and the last good reading standing in for failed ones (the Claude feed always fails
// here). generated_at is the only normalized value.
func (h *harness) testUsageCollect(t *testing.T) {
	caches := readCacheFixtures(t)
	green, _ := caches.Get("green")
	unknown, _ := caches.Get("unknown")
	carried := withLastGood(t, green, map[string]string{"claude": "2026-10-18T19:00:00.000Z", "codex": ""})
	rpcError := seedCodex(map[int]string{
		1: `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"parity"}}`,
		3: `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"not logged in"}}`,
	})
	cases := []struct {
		name string
		args []string
		seed seed
	}{
		{"no-cache/json", []string{"--json"}, codexAnswers(t)},
		{"no-cache/text", nil, codexAnswers(t)},
		{"stale-cache", []string{"--json"}, seeds(seedCache(green, time.Now().Add(-6*time.Minute)), codexAnswers(t))},
		{"refresh-ignores-fresh-cache", []string{"--refresh", "--json"}, seeds(seedCache(green, time.Now()), codexAnswers(t))},
		{"codex-rpc-error", []string{"--refresh", "--json"}, rpcError},
		{"last-good-within-30m", []string{"--refresh", "--json"}, seeds(seedCache(green, time.Now().Add(-29*time.Minute)), rpcError)},
		{"last-good-within-30m/text", []string{"--refresh"}, seeds(seedCache(green, time.Now().Add(-29*time.Minute)), rpcError)},
		{"last-good-expired", []string{"--refresh", "--json"}, seeds(seedCache(green, time.Now().Add(-31*time.Minute)), rpcError)},
		{"last-good-carried-forward", []string{"--refresh", "--json"}, seeds(seedCache(carried, time.Now().Add(-time.Minute)), rpcError)},
		{"last-good-cache-not-ok", []string{"--refresh", "--json"}, seeds(seedCache(unknown, time.Now().Add(-time.Minute)), rpcError)},
		{"last-good-fresh-codex-ok", []string{"--refresh", "--json"}, seeds(seedCache(green, time.Now().Add(-time.Minute)), codexAnswers(t))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := h.usageInvocation(c.args, c.seed)
			inv.normalize = normalizeGeneratedAt
			h.comparePair(t, inv)
		})
	}
}

// testUsageCacheRoundTrip has one runtime collect and write the cache and the other read it:
// the reader must print exactly what the writer printed and leave every file as it was.
func (h *harness) testUsageCacheRoundTrip(t *testing.T) {
	js := []string{h.node, filepath.Join(h.orchJS, "bin", "usage.mjs")}
	orch := []string{h.orchBin, "usage"}
	for _, pair := range []struct {
		name           string
		writer, reader []string
	}{
		{"js-writes-orch-reads", js, orch},
		{"orch-writes-js-reads", orch, js},
	} {
		t.Run(pair.name, func(t *testing.T) {
			sb := h.newSandbox(t)
			codexAnswers(t)(t, sb)
			written := h.run(t, sb, append(pair.writer, "--refresh", "--json"), nil, nil)
			if _, ok := written.files["state/usage.json"]; !ok || written.exitCode != 0 {
				t.Fatalf("writer failed: exit %d, stderr %s", written.exitCode, written.stderr)
			}
			compareOutcomes(t, written, h.run(t, sb, append(pair.reader, "--json"), nil, nil))
		})
	}
}

func (h *harness) testRoutingCommand(t *testing.T, caches *jsjson.Object) {
	const getRouting = `const { getRouting } = await import(process.argv[1] + "/lib/usage.mjs");
console.log(JSON.stringify(await getRouting({ refresh: process.argv[2] === "--refresh" }), null, 2));`
	amber, _ := caches.Get("amber")
	t.Run("cached-usage", func(t *testing.T) {
		h.comparePair(t, invocation{
			js:   []string{h.node, "--input-type=module", "-e", getRouting, h.orchJS, ""},
			orch: []string{h.orchBin, "routing", "--json"},
			seed: seedCache(amber, time.Now()),
		})
	})
	t.Run("refresh", func(t *testing.T) {
		h.comparePair(t, invocation{
			js:        []string{h.node, "--input-type=module", "-e", getRouting, h.orchJS, "--refresh"},
			orch:      []string{h.orchBin, "routing", "--refresh", "--json"},
			seed:      codexAnswers(t),
			normalize: normalizeGeneratedAt,
		})
	})
}

func (h *harness) testBudgetGate(t *testing.T, caches *jsjson.Object) {
	const budgetGate = `const { budgetGate } = await import(process.argv[1] + "/lib/budget.mjs");
const [vendor, flags] = process.argv.slice(2);
const gate = await budgetGate(vendor, { force: flags.includes("force"), frontier: flags.includes("frontier") });
delete gate.routing;
console.log(JSON.stringify(gate));`
	for _, cache := range []string{"green", "amber", "red", "unknown", "locked"} {
		fixture, _ := caches.Get(cache)
		for _, vendor := range []string{"anthropic", "openai"} {
			for _, flags := range [][]string{{}, {"--force"}, {"--frontier"}} {
				t.Run(fmt.Sprintf("%s/%s/%v", cache, vendor, flags), func(t *testing.T) {
					h.comparePair(t, invocation{
						js:   []string{h.node, "--input-type=module", "-e", budgetGate, h.orchJS, vendor, fmt.Sprint(flags)},
						orch: append([]string{h.orchBin, "budget", "gate", "--vendor", vendor}, flags...),
						seed: seedCache(fixture, time.Now()),
					})
				})
			}
		}
	}
}

func (h *harness) testEffort(t *testing.T) {
	const effortFor = `const { effortFor } = await import(process.argv[1] + "/lib/routing.mjs");
const { loadRegistry } = await import(process.argv[1] + "/lib/usage.mjs");
console.log(String(effortFor(loadRegistry(), process.argv[2], process.argv[3] || undefined)));`
	for _, role := range []string{"planner", "coder", "reviewer", "verifier", "researcher", "explorer", "utility", "conductor"} {
		for _, size := range []string{"", "trivial", "small", "medium", "large", "hard", "enormous"} {
			t.Run(role+"/"+size, func(t *testing.T) {
				orch := []string{h.orchBin, "effort", "--role", role}
				if size != "" {
					orch = append(orch, "--size", size)
				}
				h.comparePair(t, invocation{
					js:   []string{h.node, "--input-type=module", "-e", effortFor, h.orchJS, role, size},
					orch: orch,
				})
			})
		}
	}
}

func (h *harness) usageInvocation(args []string, s seed) invocation {
	return invocation{
		js:   append([]string{h.node, filepath.Join(h.orchJS, "bin", "usage.mjs")}, args...),
		orch: append([]string{h.orchBin, "usage"}, args...),
		seed: s,
	}
}

// seed prepares a sandbox before a run.
type seed = func(t *testing.T, sb sandbox)

func seeds(all ...seed) seed {
	return func(t *testing.T, sb sandbox) {
		for _, s := range all {
			s(t, sb)
		}
	}
}

// soonPlaceholder in a cache fixture stands for a reset 10 minutes after generated_at: inside
// the routing "soon" threshold, yet not passed, since a passed reset makes the cache stale.
const soonPlaceholder = `"<soon>"`

// seedCache writes the usage cache as the JS does (indent 2, no trailing newline) with
// generated_at first and soonPlaceholder resolved. The time is fixed when the seed is built,
// so both runtimes of a pair read the same document.
func seedCache(fixture any, generatedAt time.Time) seed {
	doc := jsjson.NewObject()
	doc.Set("generated_at", jsjson.ISOString(generatedAt))
	entries := fixture.(*jsjson.Object)
	for _, key := range entries.Keys() {
		value, _ := entries.Get(key)
		doc.Set(key, value)
	}
	return func(t *testing.T, sb sandbox) {
		data, err := jsjson.MarshalIndent(doc, "  ")
		if err != nil {
			t.Fatal(err)
		}
		soon := `"` + jsjson.ISOString(generatedAt.Add(10*time.Minute)) + `"`
		writeSandboxFile(t, sb, ".local/state/opencode/orchestrator/usage.json", strings.ReplaceAll(string(data), soonPlaceholder, soon))
	}
}

func seedFile(rel, content string) seed {
	return func(t *testing.T, sb sandbox) { writeSandboxFile(t, sb, rel, content) }
}

// seedDB creates opencode.db with the real sqlite3, skipping the case when it is missing.
func seedDB(sql string) seed {
	return func(t *testing.T, sb sandbox) {
		sqlite, err := exec.LookPath("sqlite3")
		if err != nil {
			t.Skip("sqlite3 not installed")
		}
		db := writeSandboxFile(t, sb, ".local/share/opencode/opencode.db", "")
		if out, err := exec.Command(sqlite, db, sql).CombinedOutput(); err != nil {
			t.Fatalf("sqlite3: %v: %s", err, out)
		}
	}
}

// seedCodex scripts the codex fake's app-server: replies[n] answers the n-th stdin line.
func seedCodex(replies map[int]string) seed {
	return func(t *testing.T, sb sandbox) {
		for n, reply := range replies {
			writeSandboxFile(t, sb, filepath.Join("fake-codex", strconv.Itoa(n)), reply+"\n")
		}
		if err := os.MkdirAll(filepath.Join(sb.home, "fake-log"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// codexAnswers scripts a logged-in app-server answering with the recorded fixtures (lines 1,
// 3 and 4 are initialize, account/read and account/rateLimits/read; line 2 is the
// initialized notification).
func codexAnswers(t *testing.T) seed {
	return seedCodex(map[int]string{
		1: `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"parity"}}`,
		3: `{"jsonrpc":"2.0","id":2,"result":` + compactFixture(t, "codex-account.json") + `}`,
		4: `{"jsonrpc":"2.0","id":3,"result":` + compactFixture(t, "codex-ratelimits.json") + `}`,
	})
}

// withLastGood copies a cache fixture whose vendor entries already carry last_good_at (the
// given value) and a read_error, as a document written after an earlier failed read.
func withLastGood(t *testing.T, fixture any, lastGoodAt map[string]string) *jsjson.Object {
	t.Helper()
	data, err := jsjson.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := copied.(*jsjson.Object)
	for vendor, at := range lastGoodAt {
		entry := jsjson.Path(doc, vendor).(*jsjson.Object)
		entry.Set("last_good_at", at)
		entry.Set("read_error", "usage endpoint 429")
	}
	return doc
}

var generatedAtPattern = regexp.MustCompile(`"generated_at": "\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z"`)

// normalizeGeneratedAt replaces the collection time, which differs between two collections.
func normalizeGeneratedAt(s string) string {
	return generatedAtPattern.ReplaceAllString(s, `"generated_at": "<generated_at>"`)
}

func writeSandboxFile(t *testing.T, sb sandbox, rel, content string) string {
	t.Helper()
	file := filepath.Join(sb.home, rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func readCacheFixtures(t *testing.T) *jsjson.Object {
	t.Helper()
	return readUsageFixture(t, "parity-caches.json").(*jsjson.Object)
}

func compactFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := jsjson.Marshal(readUsageFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readUsageFixture(t *testing.T, name string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(usageTestdata, name))
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsjson.Unmarshal(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}
