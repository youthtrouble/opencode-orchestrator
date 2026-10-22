package usage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// The exact request lines JSON.stringify produces in lib/codex-appserver.mjs.
const wantAppServerRequests = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"opencode-orchestrator","title":"opencode orchestrator","version":"0.1.0"},"capabilities":{"experimentalApi":false}}}
{"jsonrpc":"2.0","method":"initialized","params":{}}
{"jsonrpc":"2.0","id":2,"method":"account/read","params":{}}
{"jsonrpc":"2.0","id":3,"method":"account/rateLimits/read","params":{"excludeResetCreditDetails":true}}
`

// fakeAppServer installs a codex fake that records its pid, logs every stdin line and answers
// the n-th line with replies[n] (1-based; an absent n gets no answer), then lingers until
// killed. The pid file appears atomically once the fake runs with its signal handling in
// place, so it doubles as the readiness signal; when a test call returns after an answer it
// therefore exists. Replies are written to files so the script needs no quoting.
func fakeAppServer(t *testing.T, s *sandbox, replies map[int]string) (requestLog, pidFile string) {
	t.Helper()
	return installAppServer(t, s, "", replies)
}

// fakeStubbornAppServer is fakeAppServer for a child that ignores SIGTERM, so only SIGKILL
// stops it.
func fakeStubbornAppServer(t *testing.T, s *sandbox, replies map[int]string) (pidFile string) {
	t.Helper()
	_, pidFile = installAppServer(t, s, "trap '' TERM", replies)
	return pidFile
}

func installAppServer(t *testing.T, s *sandbox, prelude string, replies map[int]string) (requestLog, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	for n, reply := range replies {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(n)), []byte(reply+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requestLog, pidFile = filepath.Join(dir, "requests"), filepath.Join(dir, "pid")
	s.fake(t, "codex", fmt.Sprintf(`[ "$1" = app-server ] || exit 9
%[4]s
echo $$ > %[1]q.tmp && mv %[1]q.tmp %[1]q
n=0
while IFS= read -r line; do
  printf '%%s\n' "$line" >> %[2]q
  n=$((n+1))
  [ -f %[3]q/$n ] && cat %[3]q/$n
done
exec sleep 30`, pidFile, requestLog, dir, prelude))
	return requestLog, pidFile
}

func rpcResult(t *testing.T, id int, fixture string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, compact(t, readFixture(t, fixture)))
}

func TestReadCodexAccountAndLimitsSpeaksTheJSProtocol(t *testing.T) {
	s := newSandbox(t)
	requestLog, pidFile := fakeAppServer(t, s, map[int]string{
		1: `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"fake"}}`,
		3: "not json\n" + `{"jsonrpc":"2.0","method":"account/updated","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":7,"result":"stray"}` + "\n" + rpcResult(t, 2, "codex-account.json"),
		4: rpcResult(t, 3, "codex-ratelimits.json"),
	})

	got, err := ReadCodexAccountAndLimits("codex", answeredTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if want := readGolden(t, "codex.golden"); indented(t, NormalizeCodex(got)) != want {
		t.Errorf("normalized\n%s\nwant\n%s", indented(t, NormalizeCodex(got)), want)
	}
	if log, _ := os.ReadFile(requestLog); string(log) != wantAppServerRequests {
		t.Errorf("requests\n%s\nwant\n%s", log, wantAppServerRequests)
	}
	assertKilled(t, pidFile)
}

func TestReadCodexAccountAndLimitsFailures(t *testing.T) {
	cases := []struct {
		name    string
		replies map[int]string
		timeout time.Duration
		want    string
	}{
		{"rpc error message", map[int]string{1: `{"id":1,"error":{"code":-32600,"message":"not logged in"}}`}, answeredTimeout, "not logged in"},
		{"rpc error without message names the method", map[int]string{
			1: `{"id":1,"result":{}}`,
			3: `{"id":2,"error":{"code":1}}`,
		}, answeredTimeout, "account/read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			_, pidFile := fakeAppServer(t, s, c.replies)
			_, err := ReadCodexAccountAndLimits("codex", c.timeout)
			if err == nil || err.Error() != c.want {
				t.Errorf("error %v, want %q", err, c.want)
			}
			assertKilled(t, pidFile)
		})
	}
}

// A child that exits at once leaves the request unanswered; the JS waits out the timeout.
func TestReadCodexAccountAndLimitsWaitsForTheTimeoutWhenTheChildExits(t *testing.T) {
	newSandbox(t) // its default codex fake exits 97 at once
	start := time.Now()
	_, err := ReadCodexAccountAndLimits("codex", 300*time.Millisecond)
	if err == nil || err.Error() != "codex app-server timeout" {
		t.Errorf("error %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("returned after %v, before the timeout", elapsed)
	}
}

// The session deadline is a real timer: with no answer, a request fails with the JS timeout
// error once it passes, and close then stops and reaps the child. The pid comes from the
// spawn, so this holds however slowly the fake starts.
func TestAppServerSessionTimesOutOnItsOwnDeadline(t *testing.T) {
	s := newSandbox(t)
	fakeAppServer(t, s, nil)
	start := time.Now()
	session, err := startAppServer(context.Background(), "codex", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.request("initialize", jsjson.NewObject())
	elapsed := time.Since(start)
	session.close()
	if !errors.Is(err, errCodexTimeout) {
		t.Errorf("error %v, want %v", err, errCodexTimeout)
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("timed out after %v, before the deadline", elapsed)
	}
	assertGone(t, session.cmd.Process.Pid)
}

// Whether the session succeeds or ends without an answer, the child is stopped and reaped
// before the call returns; one that ignores SIGTERM is killed after the grace period. Sessions
// without an answer end only once the fake is ready (see endWhenReady), so the child has
// installed its SIGTERM handling and the escalation is really exercised.
func TestReadCodexAccountAndLimitsStopsTheChild(t *testing.T) {
	answers := map[int]string{
		1: `{"jsonrpc":"2.0","id":1,"result":{}}`,
		3: rpcResult(t, 2, "codex-account.json"),
		4: rpcResult(t, 3, "codex-ratelimits.json"),
	}
	cases := []struct {
		name         string
		ignoresTERM  bool
		replies      map[int]string
		endWhenReady bool
		wantErr      string
	}{
		{"success, child ignores SIGTERM", true, answers, false, ""},
		{"no answer, child ignores SIGTERM", true, nil, true, "codex app-server timeout"},
		{"no answer, child honours SIGTERM", false, nil, true, "codex app-server timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			var pidFile string
			if c.ignoresTERM {
				pidFile = fakeStubbornAppServer(t, s, c.replies)
			} else {
				_, pidFile = fakeAppServer(t, s, c.replies)
			}
			parent := context.Background()
			if c.endWhenReady {
				parent = endWhenReady(t, pidFile)
			}
			start := time.Now()
			_, err := readCodexAccountAndLimits(parent, "codex", answeredTimeout)
			elapsed := time.Since(start)
			if got := fmt.Sprint(err); (err == nil) != (c.wantErr == "") || (err != nil && got != c.wantErr) {
				t.Errorf("error %v, want %q", err, c.wantErr)
			}
			if c.ignoresTERM && elapsed < appServerKillGrace {
				t.Errorf("returned after %v: the child cannot have been killed after the %v grace", elapsed, appServerKillGrace)
			}
			assertKilled(t, pidFile)
		})
	}
}

// endWhenReady is a context cancelled once the fake has written pidFile, ending the session
// the way its deadline would. Polling waits for an event, not for a time to pass; the bound
// only stops a fake that never starts from hanging the test (assertKilled then fails).
func endWhenReady(t *testing.T, pidFile string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		defer cancel()
		for bound := time.Now().Add(30 * time.Second); time.Now().Before(bound); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(pidFile); err == nil {
				return
			}
		}
	}()
	return ctx
}

func TestReadCodexAccountAndLimitsSpawnFailure(t *testing.T) {
	newSandbox(t)
	_, err := ReadCodexAccountAndLimits("codex-not-installed", time.Second)
	var spawnErr *SpawnError
	if !errors.As(err, &spawnErr) {
		t.Fatalf("error %v, want a SpawnError", err)
	}
}

// The goldens are normalizeCodex's output for the same inputs (testdata/usage/gen.mjs).
func TestNormalizeCodexMatchesJS(t *testing.T) {
	// a90ac3eb, plan amendment A1: single-window absence metadata postdates the
	// 9e3a456 oracle. Keep its golden immutable and account for only this delta.
	postOracleDeltas := map[string]func(*testing.T, *jsjson.Object){
		"codex-by-id": func(t *testing.T, golden *jsjson.Object) {
			t.Helper()
			weekly := jsjson.Path(golden, "weekly").(*jsjson.Object)
			if _, present := weekly.Get("applicable"); present {
				t.Fatal("codex-by-id.golden must stay the 9e3a456 oracle output; do not regenerate it from post-a90ac3eb JS")
			}
			weekly.Set("applicable", false)
		},
	}
	cases := map[string]CodexAccount{
		"codex":       {Account: readFixture(t, "codex-account.json"), Limits: readFixture(t, "codex-ratelimits.json")},
		"codex-by-id": {Limits: readFixture(t, "codex-ratelimits-by-id.json")},
		"codex-empty": {},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			want := readGolden(t, name+".golden")
			if delta, ok := postOracleDeltas[name]; ok {
				golden, err := jsjson.Unmarshal([]byte(want))
				if err != nil {
					t.Fatal(err)
				}
				delta(t, golden.(*jsjson.Object))
				want = indented(t, golden)
			}
			if got := indented(t, NormalizeCodex(input)); got != want {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestNormalizeCodexSingleWindowFeedShapes(t *testing.T) {
	for _, tc := range []struct{ name, fixture, want string }{
		{"weekly-only", "codex-ratelimits-weekly-only.json", `{"ok":true,"plan":"prolite","five_hour":{"pct":null,"resets_at":null,"window_mins":null,"applicable":false},"weekly":{"pct":0,"resets_at":"2099-01-07T00:06:43.000Z","window_mins":10080},"rate_limit_reached":null,"ordinary_usage_allowed":null,"reset_credits_available":0,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"}}`},
		{"five-hour-only", "codex-ratelimits-by-id.json", `{"ok":true,"plan":"pro","five_hour":{"pct":100,"resets_at":"2026-10-19T00:20:19.987Z","window_mins":300},"weekly":{"pct":null,"resets_at":null,"window_mins":null,"applicable":false},"rate_limit_reached":"primary","ordinary_usage_allowed":null,"reset_credits_available":0,"credits":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := readFixture(t, tc.fixture)
			windowData := jsjson.Path(fixture, "rateLimits")
			if windowData == nil {
				windowData = jsjson.Path(fixture, "rateLimitsByLimitId", "codex")
			}
			topLevel := jsjson.NewObject()
			topLevel.Set("rateLimits", windowData)
			byID := jsjson.NewObject()
			byID.Set("codex", windowData)
			fallback := jsjson.NewObject()
			fallback.Set("rateLimitsByLimitId", byID)
			var first string
			for i, limits := range []*jsjson.Object{topLevel, fallback} {
				got, err := jsjson.Marshal(NormalizeCodex(CodexAccount{Limits: limits}))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.want {
					t.Errorf("shape %d = %s, want %s", i, got, tc.want)
				}
				if i == 0 {
					first = string(got)
				} else if string(got) != first {
					t.Error("feed shapes normalize differently")
				}
			}
		})
	}
}

func TestNormalizeCodexDurationWindows(t *testing.T) {
	for _, tc := range []struct{ name, five, weekly, plan string }{
		{"weekly-only", `{"pct":null,"resets_at":null,"window_mins":null,"applicable":false}`, `{"pct":0,"resets_at":"2099-01-07T00:06:43.000Z","window_mins":10080}`, "prolite"},
		{"five-hour-only", `{"pct":16,"resets_at":"2099-01-01T05:00:19.000Z","window_mins":300}`, `{"pct":null,"resets_at":null,"window_mins":null,"applicable":false}`, "plus"},
		{"swapped", `{"pct":16,"resets_at":"2099-01-01T05:00:19.000Z","window_mins":300}`, `{"pct":60,"resets_at":"2099-01-07T00:06:43.000Z","window_mins":10080}`, "plus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeCodex(CodexAccount{Limits: readFixture(t, "codex-ratelimits-"+tc.name+".json")})
			for key, want := range map[string]string{"five_hour": tc.five, "weekly": tc.weekly} {
				encoded, err := jsjson.Marshal(jsjson.Path(got, key))
				if err != nil || string(encoded) != want {
					t.Errorf("%s = %s (%v), want %s", key, encoded, err, want)
				}
			}
			if jsjson.Path(got, "plan") != tc.plan {
				t.Errorf("plan = %v", jsjson.Path(got, "plan"))
			}
		})
	}
}

func TestNormalizeCodexUnreadableAndAmbiguousWindows(t *testing.T) {
	for _, raw := range []string{`{}`, `{"rateLimits":{"primary":{"usedPercent":null},"secondary":null}}`, `{"rateLimits":{"primary":7,"secondary":[]}}`} {
		v, err := jsjson.Unmarshal([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		got := NormalizeCodex(CodexAccount{Limits: v})
		for _, key := range []string{"five_hour", "weekly"} {
			if _, present := jsjson.Lookup(got, key, "applicable"); present {
				t.Errorf("%s: unreadable window marked absent", raw)
			}
		}
	}
	for _, duration := range []int{300, 1440, 10080} {
		raw := fmt.Sprintf(`{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":%d},"secondary":{"usedPercent":2,"windowDurationMins":%d}}}`, duration, duration)
		v, _ := jsjson.Unmarshal([]byte(raw))
		got := NormalizeCodex(CodexAccount{Limits: v})
		if jsjson.Path(got, "five_hour", "pct") != 1.0 || jsjson.Path(got, "weekly", "pct") != 2.0 {
			t.Errorf("collision did not retain position: %s", indented(t, got))
		}
	}
	v, _ := jsjson.Unmarshal([]byte(`{"rateLimits":{"primary":{"usedPercent":null,"windowDurationMins":1440}}}`))
	got := NormalizeCodex(CodexAccount{Limits: v})
	if jsjson.Path(got, "five_hour", "applicable") != false {
		t.Error("1440 minutes must map to weekly")
	}
	if _, present := jsjson.Lookup(got, "weekly", "applicable"); present {
		t.Error("present window with null percentage must not be n/a")
	}
}

// assertKilled checks that the fake recorded in pidFile is already gone (killed and reaped)
// when the call that started it has returned.
func assertKilled(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	assertGone(t, pid)
}

// assertGone checks that process pid no longer exists (it was killed and reaped).
func assertGone(t *testing.T, pid int) {
	t.Helper()
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("app-server child %d still running", pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
