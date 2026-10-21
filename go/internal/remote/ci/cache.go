package ci

import (
	"context"
	"errors"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

const (
	// RefreshAfter is how long a project's CI check is reused before gh is asked again.
	RefreshAfter = 5 * time.Minute
	// CycleBudget bounds the time all of one cycle's checks may take together, so projects
	// whose gh hangs cannot hold up pull, ack and push by Timeout each.
	CycleBudget = 2 * Timeout
)

// Cache is the snapshot.CIProvider over state.json's CI entries. It is the only cache: serve
// loads the state every cycle, so the entries are also what a later cycle, or a separate
// orch remote once, finds. A failed check is cached as well, so gh is not retried before
// RefreshAfter (a 10 s timeout must not stall every 15 s check).
//
// Checks run one after another until CycleBudget is spent. A project left unchecked, or whose
// check the budget cut short, keeps its previous entry (or has no runs) and is checked in a
// later cycle; projects already checked are then cached, so every project is reached in turn.
// A budget is simpler than running checks concurrently and bounds the cycle just as well.
type Cache struct {
	ctx       context.Context
	deadline  time.Time // when this cycle's checks must end, by the wall clock
	machineID string
	home      string // redacted out of notes
	now       time.Time
	previous  map[string]state.CIEntry
	used      map[string]state.CIEntry
	notes     []string
}

// NewCache serves the entries of previous (state.json's ci) by project key, as of now.
func NewCache(ctx context.Context, machineID, home string, now time.Time, previous map[string]state.CIEntry) *Cache {
	return &Cache{
		ctx: ctx, deadline: time.Now().Add(CycleBudget), machineID: machineID, home: home, now: now,
		previous: previous, used: map[string]state.CIEntry{},
	}
}

// LatestRuns is project's cached runs, checked again when the entry is missing, older than
// RefreshAfter or from the future (the clock moved back), while the cycle's budget lasts.
func (c *Cache) LatestRuns(project discover.Project) []wire.CiRun {
	key := snapshot.Key(c.machineID, project.RealPath)
	entry, cached := c.previous[key]
	if age := c.now.Sub(entry.CheckedAt); !cached || age < 0 || age >= RefreshAfter {
		checked, finished := c.check(project.Root)
		if finished {
			entry = checked
		} else if !cached {
			return nil // no check finished and none before it: nothing to record yet
		}
	}
	c.used[key] = entry
	if entry.Note != "" {
		c.notes = append(c.notes, project.Name+": "+entry.Note)
	}
	return entry.Runs
}

// check asks gh for the project at dir. finished is false when the cycle's budget ran out or ctx
// ended (serve is exiting) first: that says nothing about the project's CI.
func (c *Cache) check(dir string) (entry state.CIEntry, finished bool) {
	budget, cancel := context.WithDeadline(c.ctx, c.deadline)
	defer cancel()
	if budget.Err() != nil {
		return state.CIEntry{}, false
	}
	runs, err := Latest(budget, dir)
	if budget.Err() != nil {
		return state.CIEntry{}, false
	}
	entry = state.CIEntry{CheckedAt: c.now, Runs: runs}
	if err != nil {
		entry.Runs = []wire.CiRun{}
		if !errors.Is(err, ErrNoGitHubRemote) {
			entry.Note = incident.ScrubTokens(incident.Redact(c.home, err.Error()))
		}
	}
	return entry, true
}

// Entries are the entries of the projects served so far: what state.json keeps. A project not
// served (gone, excluded or unreadable) drops out, and its repository names with it.
func (c *Cache) Entries() map[string]state.CIEntry { return c.used }

// Notes say, in the order served, why a project's CI was left out: "<name>: <reason>".
func (c *Cache) Notes() []string { return c.notes }
