package ci

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/ci/citest"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

const (
	testMachineID = "00000000-0000-4000-8000-000000000001"
	oneRun        = `[{"workflowName":"ci","status":"completed","conclusion":"success","url":"https://github.com/example/app/actions/runs/1","createdAt":"2026-10-20T11:00:00Z"}]`
)

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func testProject(t *testing.T, name string) discover.Project {
	t.Helper()
	dir := projectDir(t)
	return discover.Project{Name: name, Root: dir, RealPath: dir}
}

func newTestCache(at time.Time, previous map[string]state.CIEntry) *Cache {
	return NewCache(context.Background(), testMachineID, "/srv/owner-home", at, previous)
}

func TestCacheReusesAnEntryForFiveMinutes(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	fake.PrintRuns(oneRun)
	app := testProject(t, "app")

	first := newTestCache(testNow, nil)
	runs := first.LatestRuns(app)
	if len(runs) != 1 || runs[0].Workflow != "ci" || fake.Calls() != 1 {
		t.Fatalf("first check = %+v after %d gh calls", runs, fake.Calls())
	}
	entries := first.Entries()
	if entry := entries[snapshot.Key(testMachineID, app.RealPath)]; !entry.CheckedAt.Equal(testNow) || entry.Note != "" {
		t.Errorf("entry = %+v", entry)
	}

	fake.PrintRuns("[]")
	for _, at := range []time.Duration{time.Minute, RefreshAfter - time.Second} {
		if runs := newTestCache(testNow.Add(at), entries).LatestRuns(app); len(runs) != 1 || fake.Calls() != 1 {
			t.Errorf("%s later: runs %+v after %d gh calls, want the cached run", at, runs, fake.Calls())
		}
	}
	for i, at := range []time.Duration{RefreshAfter, -time.Second} {
		if runs := newTestCache(testNow.Add(at), entries).LatestRuns(app); len(runs) != 0 || fake.Calls() != 2+i {
			t.Errorf("%s later: runs %+v after %d gh calls, want a fresh check", at, runs, fake.Calls())
		}
	}
}

func TestCacheNotesAFailureRedactedAndCachesItButNotANonGitHubProject(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	fake.Fail(1, "failed to read /srv/owner-home/.config/gh/hosts.yml")
	app, local := testProject(t, "app"), testProject(t, "local")

	cache := newTestCache(testNow, nil)
	if runs := cache.LatestRuns(app); runs == nil || len(runs) != 0 {
		t.Errorf("failed check runs = %#v, want empty", runs)
	}
	fake.SetGit("", "")
	cache.LatestRuns(local)
	wantNote := "app: gh run list: exit status 1: failed to read ~/.config/gh/hosts.yml"
	if notes := cache.Notes(); !slices.Equal(notes, []string{wantNote}) {
		t.Errorf("notes = %q, want only %q", notes, wantNote)
	}
	entries := cache.Entries()
	if len(entries) != 2 || entries[snapshot.Key(testMachineID, local.RealPath)].Note != "" {
		t.Errorf("entries = %+v, want both projects, the local one without a note", entries)
	}

	later := newTestCache(testNow.Add(time.Minute), entries)
	later.LatestRuns(app)
	if notes := later.Notes(); fake.Calls() != 1 || len(notes) != 1 || !strings.HasPrefix(notes[0], "app: gh run list") {
		t.Errorf("a minute later: notes %q after %d gh calls, want the cached failure", notes, fake.Calls())
	}
}

func TestCacheKeepsOnlyTheProjectsItServed(t *testing.T) {
	citest.Install(t, "", "")
	app := testProject(t, "app")
	previous := map[string]state.CIEntry{"0123456789abcdef0123456789abcdef": {CheckedAt: testNow}}
	cache := newTestCache(testNow, previous)
	cache.LatestRuns(app)
	if entries := cache.Entries(); len(entries) != 1 || entries[snapshot.Key(testMachineID, app.RealPath)].CheckedAt != testNow {
		t.Errorf("entries = %+v, want only app's", entries)
	}
}

func TestACheckCutShortRecordsNothing(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	fake.Hang()
	ctx, stop := context.WithCancel(context.Background())
	stop()
	cache := NewCache(ctx, testMachineID, "/srv/owner-home", testNow, nil)
	if runs := cache.LatestRuns(testProject(t, "app")); runs != nil || len(cache.Entries()) != 0 || len(cache.Notes()) != 0 {
		t.Errorf("runs %v, entries %v, notes %q; want nothing", runs, cache.Entries(), cache.Notes())
	}
}

func TestTheCycleBudgetBoundsAllChecksTogether(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	fake.Hang()
	app, web, api := testProject(t, "app"), testProject(t, "web"), testProject(t, "api")
	staleRuns := []wire.CiRun{{Repo: "example/web", Workflow: "ci", Status: "completed", At: "2026-10-20T10:00:00Z"}}
	webKey := snapshot.Key(testMachineID, web.RealPath)
	previous := map[string]state.CIEntry{webKey: {CheckedAt: testNow.Add(-time.Hour), Runs: staleRuns}}

	cache := newTestCache(testNow, previous)
	cache.deadline = time.Now().Add(300 * time.Millisecond)
	began := time.Now()
	for _, project := range []discover.Project{app, web, api} {
		cache.LatestRuns(project)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("three hung checks took %s, want about the budget", took)
	}
	entries := cache.Entries()
	if len(entries) != 1 || !slices.Equal(entries[webKey].Runs, staleRuns) || len(cache.Notes()) != 0 {
		t.Errorf("entries %+v, notes %q; want only web's previous entry kept, nothing noted", entries, cache.Notes())
	}

	fake.PrintRuns(oneRun)
	next := newTestCache(testNow, entries)
	for _, project := range []discover.Project{app, web, api} {
		if runs := next.LatestRuns(project); len(runs) != 1 {
			t.Errorf("next cycle, %s: runs %+v, want it checked again", project.Name, runs)
		}
	}
}
