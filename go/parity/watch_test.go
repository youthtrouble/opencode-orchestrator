//go:build parity

package parity

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

// watchHeader is the header's project basename and clock, which differ between the two
// sandboxes and runs; everything else in a frame is seeded relative to one fixed instant.
var watchHeader = regexp.MustCompile(`(?m)^== .*  \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ  \(usage from `)

func normalizeWatchHeader(s string) string {
	return watchHeader.ReplaceAllString(normalizeGeneratedAt(s), "== <project>  <iso>Z  (usage from ")
}

// watchUsageTime is the header's slice of generated_at, which a re-collection sets.
var watchUsageTime = regexp.MustCompile(`(?m)^(== <project>  <iso>Z  \(usage from )\d\d:\d\d:\d\dZ`)

func normalizeWatchCollected(s string) string {
	return watchUsageTime.ReplaceAllString(normalizeWatchHeader(s), "${1}<generated_at>Z")
}

// watchProject seeds a project file under .orchestrator/.
func watchProject(rel, content string) seed {
	return seedProjectFile(filepath.Join(".orchestrator", rel), content)
}

// testWatch compares `orch watch` with bin/watch.mjs. The usage cache is fresh, so neither
// runtime refreshes the feeds; all seeded times are relative to one instant.
func (h *harness) testWatch(t *testing.T) {
	green, _ := readCacheFixtures(t).Get("green")
	instant := time.Now().Add(-time.Minute).UTC()
	iso := func(offset time.Duration) string { return jsjson.ISOString(instant.Add(offset)) }
	cache := seedCache(green, instant)
	deadPid := reapedPid(t)
	message := func(id, at, to, kind, subject string) string {
		return fmt.Sprintf(`{"id":%q,"at":%q,"from":"conductor","to":%q,"kind":%q,"subject":%q}`, id, at, to, kind, subject)
	}
	var nine []string
	for i := range 9 {
		nine = append(nine, message(fmt.Sprintf("m%d", i), iso(time.Duration(i)*time.Minute), "*", "note", fmt.Sprintf("note %d", i)))
	}
	var questions []string
	for i := range 21 {
		questions = append(questions, message(fmt.Sprintf("q%02d-long-identifier", i), iso(time.Duration(i)*time.Second), "human", "question", fmt.Sprintf("question %d", i)))
	}
	codexFails := seedCodex(map[int]string{
		1: `{"jsonrpc":"2.0","id":1,"result":{"userAgent":"parity"}}`,
		3: `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"not logged in"}}`,
	})
	cases := []struct {
		name      string
		seed      seed
		crashLine string
		collects  bool // the cache is stale, so getRouting re-collects (and both feeds fail)
	}{
		{name: "empty"},
		{name: "supervisor-alive", seed: watchProject("supervisor-state.json", fmt.Sprintf(`{"status":"running","pid":%d,"checked_at":%q}`, os.Getpid(), iso(0)))},
		{name: "supervisor-dead", seed: watchProject("supervisor-state.json", fmt.Sprintf(`{"status":"running","pid":%d,"checked_at":%q}`, deadPid, iso(0)))},
		{name: "supervisor-pid-zero", seed: watchProject("supervisor-state.json", fmt.Sprintf(`{"status":"running","pid":0,"checked_at":%q}`, iso(0)))}, // no falsy guard: alive
		{name: "supervisor-no-checked-at", seed: watchProject("supervisor-state.json", `{"status":"stopped"}`)},
		{name: "flip-flipped", seed: watchProject("flip.json", fmt.Sprintf(`{"from":"opencode","to":"claude","at":%q}`, iso(0)))},
		{name: "flip-paused", seed: watchProject("flip.json", fmt.Sprintf(`{"paused":{"door":"opencode","at":%q}}`, iso(0)))},
		{name: "flip-awaiting-human-with-at", seed: watchProject("flip.json", fmt.Sprintf(`{"from":"a","to":"b","at":%q,"awaiting_human":{"reason":"push?"}}`, iso(0)))},
		{name: "flip-awaiting-human-only", seed: watchProject("flip.json", `{"awaiting_human":{"reason":"push?"}}`), crashLine: "TypeError: Cannot read properties of undefined (reading 'slice')"},
		{name: "lanes", seed: seeds(
			watchProject("workers/a-alive.json", fmt.Sprintf(`{"lane":"a","harness":"claude","status":"running","pid":%d,"updated_at":%q}`, os.Getpid(), iso(0))),
			watchProject("workers/b-dead.json", fmt.Sprintf(`{"lane":"b","harness":"codex","status":"running","pid":%d,"last_progress_at":%q}`, deadPid, iso(time.Minute))),
			watchProject("workers/c-finished.json", fmt.Sprintf(`{"lane":"c","harness":"opencode","status":"finished","last_tool":"Bash","updated_at":%q}`, iso(0))),
			watchProject("workers/d-seven.json", `7`),
			watchProject("workers/e-broken.json", `{`),
		)},
		{name: "questions", seed: watchProject("mailbox/messages.jsonl", strings.Join([]string{
			message("to-human-1", iso(0), "human", "question", "for the human"),
			message("to-star-22", iso(time.Second), "*", "question", "for everyone"),
			message("acked-333", iso(2*time.Second), "human", "question", "already answered"),
			message("someone-4", iso(3*time.Second), "coder", "question", "not for you"),
			message("astral-55", iso(4*time.Second), "human", "question", strings.Repeat("a", 99)+"😀"),
		}, "\n")+"\n")},
		{name: "questions-acked", seed: seeds(
			watchProject("mailbox/messages.jsonl", message("acked-333", iso(0), "human", "question", "answered")+"\n"),
			watchProject("mailbox/acks.jsonl", fmt.Sprintf(`{"id":"acked-333","by":"human","at":%q}`, iso(time.Second))+"\n"),
		)},
		{name: "questions-21", seed: watchProject("mailbox/messages.jsonl", strings.Join(questions, "\n")+"\n")},
		{name: "messages-9", seed: watchProject("mailbox/messages.jsonl", strings.Join(nine, "\n")+"\n")},
		{name: "incidents", seed: seedFile(incidentsFile, strings.Join([]string{
			fmt.Sprintf(`{"at":%q,"summary":"outside"}`, jsjson.ISOString(time.Now().Add(-25*time.Hour))),
			fmt.Sprintf(`{"at":%q,"summary":"inside %s"}`, iso(0), strings.Repeat("s", 100)),
		}, "\n")+"\n")},
		{name: "state-next-action", seed: watchProject("STATE.md", "# State\n## Next action\n\nrun the tests\n## Open\n")},
		{name: "state-next-action-exact", seed: watchProject("STATE.md", "## Next action (exact)\nrun the tests\n")},
		{name: "state-without-next-action", seed: watchProject("STATE.md", "# State\n")},
		{name: "stale-cache-failing-feeds", collects: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inv := invocation{
				js:        []string{h.node, filepath.Join(h.orchJS, "bin", "watch.mjs")},
				orch:      []string{h.orchBin, "watch"},
				seed:      seeds(cache, codexAnswers(t), orNothing(c.seed)),
				normalize: normalizeWatchHeader,
			}
			if c.collects {
				inv.seed = seeds(seedCache(green, time.Now().Add(-6*time.Minute)), codexFails)
				inv.normalize = normalizeWatchCollected
			}
			if c.crashLine != "" {
				inv.crash, inv.errorLine = crashC1, c.crashLine
			}
			h.comparePair(t, inv)
		})
	}
	t.Run("follow", func(t *testing.T) {
		t.Parallel()
		h.compareWatchFollow(t, seeds(cache, codexAnswers(t), watchProject("STATE.md", "## Next action\nfollow\n")), false)
	})
	t.Run("follow-awaiting-human-only", func(t *testing.T) {
		t.Parallel()
		h.compareWatchFollow(t, seeds(cache, codexAnswers(t), watchProject("flip.json", `{"awaiting_human":{"reason":"r"}}`)), true)
	})
}

// orNothing is s, or a seed that writes nothing.
func orNothing(s seed) seed {
	if s == nil {
		return seeds()
	}
	return s
}

const followFrameStart = "\x1b[2J\x1b[H"

// compareWatchFollow runs `--follow --interval 1` in each runtime until a second frame (or,
// for errorFrames, a second stderr line) begins, SIGTERMs the pid it started and compares the
// first frame (or stderr line) and that both died of the signal.
func (h *harness) compareWatchFollow(t *testing.T, seed seed, errorFrames bool) {
	run := func(argv []string) (string, *os.ProcessState) {
		sb := h.newSandbox(t)
		seed(t, sb)
		cmd := h.command(sb, append(argv, "--follow", "--interval", "1"), nil, nil)
		var watched io.ReadCloser
		var err error
		var other bytes.Buffer
		if errorFrames {
			cmd.Stdout = &other
			watched, err = cmd.StderrPipe()
		} else {
			cmd.Stderr = &other
			watched, err = cmd.StdoutPipe()
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// Bounds the process's life even if this test fails before signalling it.
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		first := make(chan string, 1)
		go func() { first <- readFirstFrame(watched, errorFrames) }()
		var frame string
		select {
		case frame = <-first:
		case <-time.After(5 * time.Second):
			t.Errorf("%s: no second frame within 5 s", argv[0])
			// Every read from the pipe must end before Wait: the reader meets EOF once the
			// process is gone.
			_ = cmd.Process.Kill()
			<-first
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		if other.Len() > 0 {
			t.Errorf("%s: unexpected output on the other stream: %q", argv[0], other.String())
		}
		return normalizeWatchHeader(frame), cmd.ProcessState
	}
	jsFrame, jsState := run([]string{h.node, filepath.Join(h.orchJS, "bin", "watch.mjs")})
	orchFrame, orchState := run([]string{h.orchBin, "watch"})
	if jsFrame == "" || jsFrame != orchFrame {
		t.Errorf("first frame differs:\n  js: %q\norch: %q", jsFrame, orchFrame)
	}
	for name, state := range map[string]*os.ProcessState{"js": jsState, "orch": orchState} {
		status, ok := state.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Errorf("%s: %v, want killed by SIGTERM", name, state)
		}
	}
}

// readFirstFrame returns the first frame once the second has begun: for a stdout stream the
// text between the first two clear-screen sequences, for a stderr stream the first line once a
// second is complete.
func readFirstFrame(r io.Reader, errorFrames bool) string {
	reader := bufio.NewReader(r)
	if errorFrames {
		first, _ := reader.ReadString('\n')
		if _, err := reader.ReadString('\n'); err != nil {
			return ""
		}
		return first
	}
	var seen strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		seen.Write(buf[:n])
		text := seen.String()
		if rest, ok := strings.CutPrefix(text, followFrameStart); ok {
			if frame, _, found := strings.Cut(rest, followFrameStart); found {
				return frame
			}
		}
		if err != nil {
			return ""
		}
	}
}
