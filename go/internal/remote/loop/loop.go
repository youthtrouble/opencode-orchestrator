// Package loop is orch remote's sync loop with the Work Trains Worker (plan D6, D7, D10). A
// cycle pulls what the owner did on the page, applies it on this Mac, acks every outcome, then
// pushes the projects whose snapshot changed, or the budget alone when only it changed. Serve repeats cycles forever under launchd,
// checking local state every 15 s in between and backing off while the Worker is unreachable.
//
// Exactly-once rests on the apply layer: an ack that is lost makes the next cycle pull the same
// items again, and applying an answer or action a second time changes nothing but resends its
// recorded outcome. An item apply could not settle is not acked, so the Worker sends it again.
package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/apply"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/client"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/config"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

const (
	// pushSpacing is the least time between two accepted pushes of one project. The Worker
	// defers anything sooner than 55 s, a little under this, so clock skew never defers.
	pushSpacing = 60 * time.Second
	// maxProjectsPerPush is the schema's cap on a PushRequest. Projects past it stay due and
	// go in a later push.
	maxProjectsPerPush = 50
)

// Remote is the Worker's machine API; *client.Client outside tests.
type Remote interface {
	Push(context.Context, wire.PushRequest) (wire.PushResponse, error)
	Pull(context.Context) (wire.PullResponse, error)
	Ack(context.Context, wire.AckRequest) (wire.AckResponse, error)
}

// Applier carries out pulled answers and actions; apply.Applier outside tests.
type Applier interface {
	ApplyAnswer(wire.PulledAnswer) (apply.Outcome, error)
	ApplyAction(wire.PulledAction) (apply.Outcome, error)
}

// Loop syncs this Mac's projects with the Worker. Connect, NewApplier, Budget, Alive, Now,
// Sleep and Jitter are the outside world; tests replace them.
type Loop struct {
	Config      config.Config
	StateFile   string
	Home        string // redacted out of logged errors and last_error
	OrchVersion string
	// Connect reads the Keychain and returns a client for this machine. It runs at most once
	// per cycle, and only when the cycle has something to send.
	Connect    func(machineID string) (Remote, error)
	NewApplier func(apply.ProjectResolver) Applier
	Budget     func() (*wire.Budget, error)
	Alive      func(pid any) bool
	Now        func() time.Time
	// Sleep waits d or until ctx is done, returning ctx's error then.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter is a random duration in [ceiling/2, ceiling].
	Jitter func(ceiling time.Duration) time.Duration
	Log    io.Writer // one line per state change or error

	reported  map[string]bool // local problems already logged, so each is logged once
	unsettled map[string]bool // unsettled items already logged; only a pull renews it
}

// Report is what one cycle or local check did.
type Report struct {
	Pulled   int `json:"pulled"`
	Applied  int `json:"applied"`
	Pushed   int `json:"pushed"`
	Deferred int `json:"deferred"`
	// PollAfter is when to pull next: the Worker's poll_after_seconds clamped to 15..300 s,
	// else the configured poll_seconds.
	PollAfter time.Duration `json:"-"`
}

// cycle is the work of one cycle on the state it loaded.
type cycle struct {
	loop     *Loop
	state    *state.State
	now      time.Time
	remote   Remote
	problems []string
}

// Cycle runs one full cycle: pull, apply, ack, then push what is due, and persists the state.
func (l *Loop) Cycle(ctx context.Context) (Report, error) {
	return l.withState(ctx, func(c *cycle) (Report, error) { return c.run(ctx) })
}

// PushDue pushes the projects whose snapshot is due, without pulling: the cheap check Serve
// makes between pulls. It touches the network only when something is due.
func (l *Loop) PushDue(ctx context.Context) (Report, error) {
	return l.withState(ctx, func(c *cycle) (Report, error) {
		var report Report
		return report, c.push(ctx, c.discover(), &report)
	})
}

// withState loads the state, runs work on it, records its error (or clears the last one) and
// saves the state, privately and atomically, when it changed: a quiet 15 s check writes nothing.
// A cycle cut short by ctx (serve stopping) leaves last_error as it was: being stopped is not a
// failure.
func (l *Loop) withState(ctx context.Context, work func(*cycle) (Report, error)) (Report, error) {
	st, err := state.Load(l.StateFile)
	if err != nil {
		return Report{}, &LocalError{Op: "read the state", Err: err}
	}
	loaded, err := json.Marshal(st)
	if err != nil {
		return Report{}, &LocalError{Op: "encode the state", Err: err}
	}
	c := &cycle{loop: l, state: &st, now: l.Now().UTC()}
	report, err := work(c)
	l.reported = l.logOnce(l.reported, c.problems)
	switch {
	case ctx.Err() != nil:
		// stopped, not failed: keep the last error
	case err != nil:
		st.LastError = &state.LastError{At: c.now, Kind: Kind(err), Message: l.Redact(err.Error())}
	default:
		st.LastError = nil
	}
	if current, encodeErr := json.Marshal(st); encodeErr == nil && bytes.Equal(current, loaded) {
		return report, err
	}
	if saveErr := state.Save(l.StateFile, st); saveErr != nil {
		return report, errors.Join(err, &LocalError{Op: "save the state", Err: saveErr})
	}
	return report, err
}

func (c *cycle) run(ctx context.Context) (Report, error) {
	report := Report{PollAfter: c.loop.defaultPoll()}
	projects := c.discover()
	remote, err := c.connect()
	if err != nil {
		return report, err
	}
	pulled, err := remote.Pull(ctx)
	if err != nil {
		return report, err
	}
	c.state.LastPullAt = &c.now
	report.PollAfter = clampPoll(pulled.PollAfterSeconds)
	report.Pulled = len(pulled.Answers) + len(pulled.Actions)
	results := c.loop.applyPulled(pulled, resolverOf(projects))
	report.Applied = countApplied(results)
	if len(results) > 0 {
		if _, err := remote.Ack(ctx, wire.AckRequest{V: wire.ProtocolVersion, Results: results}); err != nil {
			return report, err
		}
	}
	return report, c.push(ctx, projects, &report)
}

// connect returns the cycle's client, reading the Keychain on first use only.
func (c *cycle) connect() (Remote, error) {
	if c.remote == nil {
		remote, err := c.loop.Connect(c.state.MachineID)
		if err != nil {
			return nil, err
		}
		c.remote = remote
	}
	return c.remote, nil
}

// localProject is a discovered project with its Work Trains key.
type localProject struct {
	discover.Project
	key string
}

// discover finds the configured projects; a root or directory it cannot examine is a problem
// to log, never a failed cycle.
func (c *cycle) discover() []localProject {
	found, errs := discover.Projects(c.loop.Config)
	for _, err := range errs {
		c.problems = append(c.problems, "skipped "+err.Error())
	}
	projects := make([]localProject, len(found))
	for i, project := range found {
		projects[i] = localProject{Project: project, key: snapshot.Key(c.state.MachineID, project.RealPath)}
	}
	return projects
}

func resolverOf(projects []localProject) apply.ProjectResolver {
	roots := make(map[string]string, len(projects))
	for _, project := range projects {
		roots[project.key] = project.Root
	}
	return func(key string) (string, bool) {
		root, ok := roots[key]
		return root, ok
	}
}

// applyPulled applies every pulled answer and action and returns the results to ack. An
// outcome is acked as it is, whatever error came with it: apply's outcomes are final (an
// applied answer whose human gate could not be checked stays applied, a rejected one is
// rejected), and the error goes to the log only. An item apply could not settle, a
// *apply.RetryableError, gets no result: left unacked, it comes back in the next pull. It is
// logged once while it lasts, not on every pull that brings the item back, by its fixed step
// only: the wrapped error can quote text the response chose (a mailbox lookup names the id).
func (l *Loop) applyPulled(pulled wire.PullResponse, projects apply.ProjectResolver) []wire.AckResult {
	applier := l.NewApplier(projects)
	results := []wire.AckResult{}
	var unsettled []string
	settle := func(kind, id string, outcome apply.Outcome, err error) {
		var retryable *apply.RetryableError
		if errors.As(err, &retryable) {
			unsettled = append(unsettled, fmt.Sprintf("%s %s not applied, the Worker sends it again: %s", kind, loggableID(id), retryable.Step))
			return
		}
		l.logOutcome(kind, id, outcome, err)
		results = append(results, wire.AckResult{ID: id, Type: kind, Outcome: string(outcome.Result), Detail: outcome.Detail})
	}
	for _, answer := range pulled.Answers {
		outcome, err := applier.ApplyAnswer(answer)
		settle("answer", answer.ID, outcome, err)
	}
	for _, action := range pulled.Actions {
		outcome, err := applier.ApplyAction(action)
		settle("action", action.ID, outcome, err)
	}
	l.unsettled = l.logOnce(l.unsettled, unsettled)
	return results
}

func (l *Loop) logOutcome(kind, id string, outcome apply.Outcome, err error) {
	line := fmt.Sprintf("%s %s: %s", kind, loggableID(id), outcome.Result)
	if outcome.Detail != "" {
		line += ": " + outcome.Detail
	}
	if err != nil {
		line += " (error: " + l.Redact(err.Error()) + ")"
	}
	l.logf("%s", line)
}

// workerID is how the Worker numbers answers and actions: a lowercase UUID.
var workerID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// loggableID is a pulled id as the log may show it. The response chose it, and the client
// checks only its type, so one of another shape, which could be any text, is not repeated.
func loggableID(id string) string {
	if workerID.MatchString(id) {
		return id
	}
	return "<id not a uuid>"
}

func countApplied(results []wire.AckResult) int {
	applied := 0
	for _, result := range results {
		if result.Outcome == string(apply.Applied) {
			applied++
		}
	}
	return applied
}

// push sends every due project in one request and records the Worker's verdict on each. It
// sends a request without projects when only the budget is due: the Worker stores the budget on
// push alone, so a reading that changed or expired would otherwise stay on the page for as long
// as no project changed.
func (c *cycle) push(ctx context.Context, projects []localProject, report *Report) error {
	due := c.dueSnapshots(projects)
	budget, budgetHash := c.budget()
	if len(due) == 0 && !c.isBudgetDue(budgetHash) {
		return nil
	}
	remote, err := c.connect()
	if err != nil {
		return err
	}
	request := snapshot.NewPushRequest(c.state.MachineID, c.loop.OrchVersion, c.now, budget, due)
	response, err := remote.Push(ctx, request)
	if err != nil {
		return err
	}
	c.state.LastPushAt = &c.now
	c.state.LastBudgetHash = budgetHash
	if len(due) == 0 {
		c.loop.logf("pushed the budget only")
		return nil
	}
	report.Pushed = len(due)
	report.Deferred = c.record(due, response.Projects)
	return nil
}

// isBudgetDue reports whether a push for the budget alone is due: its hash differs from the
// one last pushed and the last push is at least pushSpacing old. Never before a first push has
// landed: the Worker takes a machine's first push as its backlog, and one without projects
// would end that before any project's questions came.
func (c *cycle) isBudgetDue(budgetHash string) bool {
	last := c.state.LastPushAt
	return last != nil && budgetHash != c.state.LastBudgetHash && c.now.Sub(*last) >= pushSpacing
}

// dueSnapshots builds every project's snapshot and keeps those due, at most one push's worth.
// A project is due when its hash differs from the last one the Worker took, its last accepted
// push is at least pushSpacing old and any retry_after the Worker set has passed.
func (c *cycle) dueSnapshots(projects []localProject) []wire.ProjectSnapshot {
	env := snapshot.Env{MachineID: c.state.MachineID, Home: c.loop.Home, Now: c.now, Alive: c.loop.Alive}
	due := []wire.ProjectSnapshot{}
	for _, project := range projects {
		built, err := snapshot.Build(project.Project, env)
		if err != nil {
			c.problems = append(c.problems, fmt.Sprintf("skipped %s: %v", project.Name, err))
			continue
		}
		if c.isDue(built) && len(due) < maxProjectsPerPush {
			due = append(due, built)
		}
	}
	return due
}

func (c *cycle) isDue(built wire.ProjectSnapshot) bool {
	last := c.state.Projects[built.Key]
	switch {
	case last.LastHash == built.SnapshotHash:
		return false
	case last.LastAcceptedAt != nil && c.now.Sub(*last.LastAcceptedAt) < pushSpacing:
		return false
	case last.RetryAfter != nil && c.now.Before(*last.RetryAfter):
		return false
	}
	return true
}

// budget is the cached budget to push, or null when it cannot be read, with the hash of its
// wire form.
func (c *cycle) budget() (*wire.Budget, string) {
	budget, err := c.loop.Budget()
	var encoded []byte
	if err == nil {
		encoded, err = json.Marshal(budget)
	}
	if err != nil {
		c.problems = append(c.problems, "budget left out: "+err.Error())
		budget, encoded = nil, []byte("null")
	}
	sum := sha256.Sum256(encoded)
	return budget, hex.EncodeToString(sum[:])
}

// record stores the Worker's verdict on each pushed project and returns how many it deferred.
// A project the response leaves out, or gives a result this client does not know, keeps its
// old entry, so it stays due and goes again.
func (c *cycle) record(pushed []wire.ProjectSnapshot, results []wire.PushResult) (deferred int) {
	hashes := make(map[string]string, len(pushed))
	for _, built := range pushed {
		hashes[built.Key] = built.SnapshotHash
	}
	for _, result := range results {
		hash, sent := hashes[result.Key]
		if !sent {
			continue
		}
		entry := c.state.Projects[result.Key]
		switch result.Result {
		case "accepted":
			entry = state.Project{LastHash: hash, LastAcceptedAt: &c.now}
		case "unchanged":
			entry.LastHash, entry.RetryAfter = hash, nil
		case "deferred":
			retryAfter := c.now.Add(retryDelay(result.RetryAfterSeconds))
			entry.RetryAfter = &retryAfter
			deferred++
		default:
			continue
		}
		c.state.Projects[result.Key] = entry
	}
	c.loop.logf("pushed %d project(s), %d deferred", len(pushed), deferred)
	return deferred
}

// The bounds of a push result's retry_after_seconds in remote/protocol/v1/schema.json.
const (
	minRetryAfterSeconds = 1
	maxRetryAfterSeconds = 300
)

// retryDelay is the wait the Worker asked for, else the push spacing. A wait outside the
// schema's bounds is not trusted: it would overflow time.Duration or, persisted in the state,
// hold the project back for good.
func retryDelay(seconds *int) time.Duration {
	if seconds == nil || *seconds < minRetryAfterSeconds || *seconds > maxRetryAfterSeconds {
		return pushSpacing
	}
	return time.Duration(*seconds) * time.Second
}

func (l *Loop) defaultPoll() time.Duration {
	return time.Duration(l.Config.PollSeconds) * time.Second
}

func clampPoll(seconds int) time.Duration {
	return time.Duration(min(max(seconds, config.MinPollSeconds), config.MaxPollSeconds)) * time.Second
}

// logOnce logs, redacted, each problem not in logged, and returns the problems now current,
// which the next call takes as logged: a problem is logged once while it lasts (an unreadable
// project would otherwise fill the log every 15 s), and again if it goes away and comes back.
// Problems are told apart before redaction, which writes every id as <uuid>: two unsettled
// items are two problems, each logged once.
func (l *Loop) logOnce(logged map[string]bool, problems []string) map[string]bool {
	current := make(map[string]bool, len(problems))
	for _, problem := range problems {
		if !logged[problem] && !current[problem] {
			l.logf("%s", l.Redact(problem))
		}
		current[problem] = true
	}
	return current
}

// Redact strips the home directory, user paths, emails and tokens from text bound for the log
// or the state file.
func (l *Loop) Redact(text string) string {
	return incident.ScrubTokens(incident.Redact(l.Home, text))
}

func (l *Loop) logf(format string, args ...any) {
	fmt.Fprintf(l.Log, "%s orch remote: %s\n", l.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// LocalError is a failure on this Mac, such as a state file that cannot be read or written.
type LocalError struct {
	Op  string
	Err error
}

func (e *LocalError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *LocalError) Unwrap() error { return e.Err }

// Kind names the class of a cycle error for last_error and the log.
func Kind(err error) string {
	var (
		denied   *client.AccessDeniedError
		status   *client.HTTPStatusError
		protocol *client.ProtocolError
		network  *client.NetworkError
		missing  *client.MissingSecretError
		security *keychain.CommandError
	)
	switch {
	case errors.As(err, &denied):
		return "access_denied"
	case errors.As(err, &status):
		return "http_status"
	case errors.As(err, &protocol):
		return "protocol"
	case errors.As(err, &network):
		return "network"
	case errors.As(err, &missing), errors.As(err, &security):
		return "keychain"
	}
	return "local"
}
