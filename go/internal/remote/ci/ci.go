// Package ci finds the latest GitHub Actions runs of a project's default branch with the owner's
// own gh login (plan Q14), one per workflow, and caches them per project in state.json. It is
// optional: whatever goes wrong leaves the project without CI and never fails a sync cycle.
package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

const (
	// Timeout bounds one project's whole check: both git calls and gh.
	Timeout = 10 * time.Second
	// listLimit is how many recent runs gh is asked for; the newest per workflow is kept.
	listLimit = 10
	// fallbackBranch is the default branch when origin/HEAD is not set locally.
	fallbackBranch = "main"
)

// ErrNoGitHubRemote is a project whose origin is missing or not a github.com repository. It has
// no GitHub CI, which is not a failure.
var ErrNoGitHubRemote = errors.New("origin is not a GitHub repository")

// ErrGhMissing is gh not being on PATH.
var ErrGhMissing = errors.New("gh is not installed (not on PATH)")

// CommandError is a git or gh run that failed or timed out.
type CommandError struct {
	Command string // such as "gh run list"
	Err     error
	Stderr  string // the first line the command wrote to stderr, if any
}

func (e *CommandError) Error() string {
	message := e.Command + ": " + e.Err.Error()
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

func (e *CommandError) Unwrap() error { return e.Err }

// OutputError is gh output that is not the run list asked for.
type OutputError struct{ Err error }

func (e *OutputError) Error() string { return "gh run list printed no run list: " + e.Err.Error() }
func (e *OutputError) Unwrap() error { return e.Err }

// Latest returns the newest run of each workflow on the default branch of the GitHub repository
// that is the origin of the git checkout at dir, newest first, at most snapshot.MaxCIRuns.
func Latest(ctx context.Context, dir string) ([]wire.CiRun, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	repo, err := originRepo(ctx, dir)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, ErrGhMissing
	}
	// --repo pins the repository the runs are labelled with: in a fork gh would otherwise
	// pick its own default repository, which may be the parent.
	out, err := run(ctx, dir, "gh", "run", "list", "--repo", repo, "--branch", defaultBranch(ctx, dir),
		"--limit", fmt.Sprint(listLimit), "--json", "workflowName,status,conclusion,url,createdAt")
	if err != nil {
		return nil, err
	}
	var runs []ghRun
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, &OutputError{Err: err}
	}
	return latestPerWorkflow(repo, runs), nil
}

// originRepo is "owner/name" of origin. A checkout without origin (git's exit 2, "no such
// remote") or dir not being inside a checkout at all is ErrNoGitHubRemote. Any other failure,
// such as git refusing a repository it does not trust, is a CommandError, so the owner hears
// why CI is missing.
func originRepo(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "git", "remote", "get-url", "origin")
	var exited *exec.ExitError
	if errors.As(err, &exited) && (exited.ExitCode() == gitNoSuchRemote || !insideCheckout(dir)) {
		return "", ErrNoGitHubRemote
	} else if err != nil {
		return "", err
	}
	repo, ok := githubRepo(strings.TrimSpace(string(out)))
	if !ok {
		return "", ErrNoGitHubRemote
	}
	return repo, nil
}

// gitNoSuchRemote is the exit code of git remote get-url for a remote that is not configured.
const gitNoSuchRemote = 2

// insideCheckout is whether dir or a directory above it holds a .git (a directory, or the file
// a linked worktree or submodule has). git exits 128 both outside a checkout and for a
// checkout it refuses; only the second is a failure worth a note.
func insideCheckout(dir string) bool {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// repoPattern is a GitHub owner/name.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// githubRepo is "owner/name" of a github.com remote URL: https://github.com/owner/name(.git),
// ssh://git@github.com/owner/name(.git) or the scp-like git@github.com:owner/name(.git).
func githubRepo(remote string) (string, bool) {
	var host, path string
	if parsed, err := url.Parse(remote); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host, path = parsed.Hostname(), parsed.Path
	} else if userHost, rest, found := strings.Cut(remote, ":"); found && !strings.Contains(userHost, "/") {
		host, path = userHost[strings.LastIndex(userHost, "@")+1:], rest
	}
	repo := strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if !strings.EqualFold(host, "github.com") || !repoPattern.MatchString(repo) {
		return "", false
	}
	return repo, true
}

// defaultBranch is origin's HEAD as git last fetched it. A clone always records it, but a remote
// added by hand does not; main is then the likeliest name, and a wrong guess only means no runs.
func defaultBranch(ctx context.Context, dir string) string {
	out, err := run(ctx, dir, "git", "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	branch, found := strings.CutPrefix(strings.TrimSpace(string(out)), "refs/remotes/origin/")
	if err != nil || !found || branch == "" {
		return fallbackBranch
	}
	return branch
}

// githubHost pins gh to github.com: a GH_HOST in the owner's environment naming an enterprise
// host would otherwise make gh query that host for a repository labelled as github.com's.
const githubHost = "GH_HOST=github.com"

// run runs name with args in dir and returns its stdout. The command gets its own process
// group, and a timeout kills the whole group, so a child it started cannot outlive it.
func run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	env, err := procs.WithPWD(append(os.Environ(), githubHost), dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env // git ignores GH_HOST
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A child that left the group may still hold the pipes open; stop waiting for it soon after.
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	command := strings.Join(append([]string{name}, args[:min(2, len(args))]...), " ")
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out: %w", ctx.Err())
	}
	first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
	return nil, &CommandError{Command: command, Err: err, Stderr: strings.TrimSpace(first)}
}

// ghRun is one run as gh run list --json prints it.
type ghRun struct {
	WorkflowName string    `json:"workflowName"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
}

// latestPerWorkflow keeps the newest run of each workflow, newest first, at most
// snapshot.MaxCIRuns, as wire runs of repo.
func latestPerWorkflow(repo string, runs []ghRun) []wire.CiRun {
	slices.SortStableFunc(runs, func(a, b ghRun) int { return b.CreatedAt.Compare(a.CreatedAt) })
	seen := map[string]bool{}
	latest := []wire.CiRun{}
	for _, run := range runs {
		if len(latest) == snapshot.MaxCIRuns {
			break
		}
		if seen[run.WorkflowName] {
			continue
		}
		seen[run.WorkflowName] = true
		latest = append(latest, wire.CiRun{
			Repo:       repo,
			Workflow:   run.WorkflowName,
			Status:     wireStatus(run.Status),
			Conclusion: wireConclusion(run.Conclusion),
			URL:        optional(run.URL),
			At:         run.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return latest
}

// wireStatus maps a GitHub run status onto the protocol's; the waiting states GitHub adds
// (requested, waiting, pending) are unknown to the page.
func wireStatus(status string) string {
	switch status {
	case "queued", "in_progress", "completed":
		return status
	}
	return "unknown"
}

// wireConclusion maps a GitHub run conclusion onto the protocol's. A run that failed to start
// is a failure to the owner; stale and anything newer have no protocol value and become null,
// as does the empty conclusion of a run still going.
func wireConclusion(conclusion string) *string {
	switch conclusion {
	case "success", "failure", "cancelled", "skipped", "neutral", "timed_out", "action_required":
		return &conclusion
	case "startup_failure":
		failure := "failure"
		return &failure
	}
	return nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
