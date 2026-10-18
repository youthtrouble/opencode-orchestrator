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
	cases := map[string]CodexAccount{
		"codex":       {Account: readFixture(t, "codex-account.json"), Limits: readFixture(t, "codex-ratelimits.json")},
		"codex-by-id": {Limits: readFixture(t, "codex-ratelimits-by-id.json")},
		"codex-empty": {},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if got, want := indented(t, NormalizeCodex(input)), readGolden(t, name+".golden"); got != want {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
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
