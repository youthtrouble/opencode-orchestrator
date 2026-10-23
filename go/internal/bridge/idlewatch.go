package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
)

type activityLedger struct {
	known    bool
	at       time.Time
	inFlight bool
	writers  int
}

type idleEvidence struct {
	now, started, stdoutAt time.Time
	interval               time.Duration
	ledger                 activityLedger
	probeKnown             bool
	live                   bool
}

// idleVerdict never infers idleness from missing evidence. Future timestamps count as now.
func idleVerdict(e idleEvidence) string {
	last := e.started
	for _, at := range []time.Time{e.stdoutAt, e.ledger.at} {
		if at.After(last) {
			last = at
		}
	}
	if e.now.Sub(last) < e.interval {
		return "active"
	}
	if !e.ledger.known {
		return "ledger_unknown"
	}
	if e.ledger.inFlight {
		return "tools_in_flight"
	}
	if !e.probeKnown {
		return "probe_unknown"
	}
	if e.live {
		return "descendants"
	}
	return "stop"
}

func readActivityLedger(root string, now time.Time) activityLedger {
	reader := newActivityLedgerReader(root)
	defer reader.close()
	return reader.read(now)
}

type cachedActivityFile struct {
	info os.FileInfo
	data []byte
}

// A check takes two snapshots around the process probe, sharing a total budget of 64 opens.
// Ledger writers replace files atomically: an unchanged inode still holds the same record.
// Keeping descriptors open until the check ends prevents inode reuse from defeating that test.
type activityLedgerReader struct {
	root      string
	remaining int
	cache     map[string]cachedActivityFile
	opened    []*os.File
}

func newActivityLedgerReader(root string) *activityLedgerReader {
	return &activityLedgerReader{root: root, remaining: 64, cache: make(map[string]cachedActivityFile)}
}

func (r *activityLedgerReader) close() {
	for _, file := range r.opened {
		_ = file.Close()
	} // read-only descriptors; no buffered writes
}

func (r *activityLedgerReader) contents(dir string, info os.FileInfo) ([]byte, error) {
	if cached, ok := r.cache[info.Name()]; ok && os.SameFile(cached.info, info) && cached.info.Size() == info.Size() && cached.info.ModTime().Equal(info.ModTime()) {
		return cached.data, nil
	}
	if r.remaining == 0 {
		return nil, errors.New("activity ledger read budget exhausted")
	}
	r.remaining--
	file, err := os.Open(filepath.Join(dir, info.Name()))
	if err != nil {
		return nil, err
	}
	r.opened = append(r.opened, file)
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("activity ledger file too large")
	}
	r.cache[info.Name()] = cachedActivityFile{info: openedInfo, data: data}
	return data, nil
}

func (r *activityLedgerReader) read(now time.Time) activityLedger {
	var ledger activityLedger
	dir := filepath.Join(r.root, ".orchestrator", "activity", "opencode")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ledger
	}
	var files []os.FileInfo
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return activityLedger{}
		}
		files = append(files, info)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().After(files[j].ModTime()) })
	// An omitted writer could be busy. Bound reads without turning omitted evidence into idle.
	complete := len(files) <= 64
	unknownActivity := false
	for _, info := range files[:min(len(files), 64)] {
		if info.Size() > 64<<10 {
			complete = false
			continue
		}
		data, err := r.contents(dir, info)
		if err != nil {
			complete = false
			continue
		}
		var record struct {
			Schema         int        `json:"schema"`
			AliveAt        time.Time  `json:"alive_at"`
			LastActivityAt *time.Time `json:"last_activity_at"`
			ToolsInFlight  *int       `json:"tools_in_flight"`
		}
		if json.Unmarshal(data, &record) != nil || record.Schema != 1 {
			continue
		}
		if record.AliveAt.IsZero() || now.Sub(record.AliveAt) > 180*time.Second {
			continue
		}
		ledger.writers++
		if record.LastActivityAt == nil || record.ToolsInFlight == nil || *record.ToolsInFlight < 0 {
			unknownActivity = true
			continue
		}
		at := *record.LastActivityAt
		if at.After(now) {
			at = now
		}
		if at.After(ledger.at) {
			ledger.at = at
		}
		ledger.inFlight = ledger.inFlight || *record.ToolsInFlight > 0
	}
	ledger.known = complete && !unknownActivity && ledger.writers > 0
	return ledger
}

type idleTicker interface {
	Ticks() <-chan time.Time
	Stop()
}

type realIdleTicker struct{ *time.Ticker }

func (t realIdleTicker) Ticks() <-chan time.Time { return t.C }

// idleRuntime is per invocation so tests can advance time without affecting other bridges.
type idleRuntime struct {
	now        func() time.Time
	ticker     func(time.Duration) idleTicker
	probe      func(int) ([]int, error)
	beforeStop func(*jsThread, *procs.Child) // deterministic race seam before the final verdict
	afterCheck func()                        // test handshake; never used to determine a verdict
}

func defaultIdleRuntime() idleRuntime {
	return idleRuntime{now: time.Now, ticker: func(d time.Duration) idleTicker { return realIdleTicker{time.NewTicker(d)} }, probe: procs.LiveDescendants}
}

type idleWatch struct {
	root                      string
	minutes                   float64
	started                   time.Time
	runtime                   idleRuntime
	lastLedgerAt, lastVisible time.Time
	inactiveLogged            bool
	stopped                   activityLedger
}

func (w *idleWatch) watch(thread *jsThread, child *procs.Child, done <-chan struct{}) {
	interval := time.Duration(w.minutes * float64(time.Minute))
	every := min(time.Minute, interval/4)
	if every <= 0 {
		every = time.Nanosecond
	}
	ticker := w.runtime.ticker(every)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.Ticks():
			w.check(thread, child, interval)
			if w.runtime.afterCheck != nil {
				w.runtime.afterCheck()
			}
		}
	}
}

func (w *idleWatch) check(thread *jsThread, child *procs.Child, interval time.Duration) {
	now := w.runtime.now()
	reader := newActivityLedgerReader(w.root)
	defer reader.close()
	ledger := reader.read(now)
	thread.mu.Lock()
	if thread.closed || thread.stopCause != "" || thread.stopSent {
		thread.mu.Unlock()
		return
	}
	evidence := idleEvidence{now: now, started: w.started, stdoutAt: thread.stdoutAt, interval: interval, ledger: ledger}
	quiet := now.Sub(maxTime(w.started, thread.stdoutAt))
	if quiet >= 5*time.Minute && ledger.at.After(w.lastLedgerAt) && (w.lastVisible.IsZero() || now.Sub(w.lastVisible) >= 5*time.Minute) {
		tool := ""
		if ledger.inFlight {
			tool = ", tool running"
		}
		thread.log.printf("still working: last session activity %ds ago%s", max(0, int(now.Sub(ledger.at).Seconds())), tool)
		w.lastVisible = now
	}
	w.lastLedgerAt = ledger.at
	reason := idleVerdict(evidence)
	thread.mu.Unlock()
	// Only probe when all other evidence permits a stop.
	if reason == "probe_unknown" {
		pids, err := w.runtime.probe(child.Pid())
		evidence.probeKnown, evidence.live = err == nil, len(pids) > 0
		reason = idleVerdict(evidence)
	}
	if reason == "stop" && w.runtime.beforeStop != nil {
		w.runtime.beforeStop(thread, child)
	}
	thread.mu.Lock()
	defer thread.mu.Unlock()
	if thread.closed || thread.stopCause != "" || thread.stopSent {
		return
	}
	evidence.now, evidence.stdoutAt = w.runtime.now(), thread.stdoutAt
	// The probe can take five seconds. Revalidate the directory and every writer at the
	// signalling boundary; a replacement record is read again within the shared budget.
	evidence.ledger = reader.read(evidence.now)
	reason = idleVerdict(evidence)
	if (reason == "ledger_unknown" || reason == "probe_unknown") && !w.inactiveLogged {
		thread.log.printf("idle watchdog inactive: %s; not stopping", reason)
		w.inactiveLogged = true
	}
	if reason != "stop" {
		return
	}
	w.stopped = evidence.ledger
	thread.stopCause = "idle"
	thread.log.printf("idle watchdog: no activity for %s min and no live work; stopping", text(w.minutes))
	thread.signalLocked(child, "idle", syscall.SIGINT)
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
