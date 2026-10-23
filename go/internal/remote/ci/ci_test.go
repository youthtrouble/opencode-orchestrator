package ci

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/ci/citest"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

func TestRunSetsPWDToDir(t *testing.T) {
	t.Setenv("PWD", t.TempDir())
	dir := projectDir(t)
	out, err := run(context.Background(), dir, "/usr/bin/env")
	if err != nil {
		t.Fatal(err)
	}
	var pwd []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "PWD=") {
			pwd = append(pwd, line)
		}
	}
	if !slices.Equal(pwd, []string{"PWD=" + dir}) {
		t.Errorf("PWD entries = %q, want PWD=%s", pwd, dir)
	}
	if !strings.Contains(string(out), "GH_HOST=github.com\n") {
		t.Error("lost GH_HOST override")
	}
}

// projectDir is a directory for the fake git and gh to run in, by its physical path.
func projectDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// sevenRuns spans six workflows, out of date order, so only the newest five survive.
const sevenRuns = `[
 {"workflowName":"ci","status":"completed","conclusion":"failure","url":"https://github.com/example/app/actions/runs/1","createdAt":"2026-10-20T08:00:00Z"},
 {"workflowName":"ci","status":"in_progress","conclusion":"","url":"https://github.com/example/app/actions/runs/7","createdAt":"2026-10-20T11:00:00Z"},
 {"workflowName":"lint","status":"completed","conclusion":"success","url":"https://github.com/example/app/actions/runs/2","createdAt":"2026-10-20T10:00:00Z"},
 {"workflowName":"release","status":"completed","conclusion":"startup_failure","url":"","createdAt":"2026-10-20T09:30:00+01:00"},
 {"workflowName":"docs","status":"waiting","conclusion":"","url":"https://github.com/example/app/actions/runs/4","createdAt":"2026-10-20T07:00:00Z"},
 {"workflowName":"e2e","status":"completed","conclusion":"stale","url":"https://github.com/example/app/actions/runs/5","createdAt":"2026-10-20T06:00:00Z"},
 {"workflowName":"nightly","status":"completed","conclusion":"success","url":"https://github.com/example/app/actions/runs/6","createdAt":"2026-10-20T05:00:00Z"}
]`

func ptr(s string) *string { return &s }

func TestLatestKeepsTheNewestRunOfEachWorkflowOnTheDefaultBranch(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "refs/remotes/origin/trunk")
	fake.PrintRuns(sevenRuns)
	dir := projectDir(t)

	runs, err := Latest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []wire.CiRun{
		{Repo: "example/app", Workflow: "ci", Status: "in_progress", URL: ptr("https://github.com/example/app/actions/runs/7"), At: "2026-10-20T11:00:00Z"},
		{Repo: "example/app", Workflow: "lint", Status: "completed", Conclusion: ptr("success"), URL: ptr("https://github.com/example/app/actions/runs/2"), At: "2026-10-20T10:00:00Z"},
		{Repo: "example/app", Workflow: "release", Status: "completed", Conclusion: ptr("failure"), At: "2026-10-20T08:30:00Z"},
		{Repo: "example/app", Workflow: "docs", Status: "unknown", URL: ptr("https://github.com/example/app/actions/runs/4"), At: "2026-10-20T07:00:00Z"},
		{Repo: "example/app", Workflow: "e2e", Status: "completed", URL: ptr("https://github.com/example/app/actions/runs/5"), At: "2026-10-20T06:00:00Z"},
	}
	if !reflect.DeepEqual(runs, want) {
		got, _ := json.MarshalIndent(runs, "", " ")
		t.Errorf("runs = %s", got)
	}
	calledIn, args := fake.LastCall()
	wantArgs := []string{"run", "list", "--repo", "example/app", "--branch", "trunk", "--limit", "10", "--json", "workflowName,status,conclusion,url,createdAt"}
	if calledIn != dir || !slices.Equal(args, wantArgs) {
		t.Errorf("gh ran in %s with %q, want %s with %q", calledIn, args, dir, wantArgs)
	}
}

func TestLatestAsksForMainWhenOriginHEADIsUnset(t *testing.T) {
	fake := citest.Install(t, "https://github.com/example/app", "")
	if runs, err := Latest(context.Background(), projectDir(t)); err != nil || runs == nil || len(runs) != 0 {
		t.Fatalf("Latest = %v, %v; want no runs", runs, err)
	}
	if _, args := fake.LastCall(); !slices.Contains(args, "main") {
		t.Errorf("gh args %q, want --branch main", args)
	}
}

func TestGithubRepoReadsEveryGitHubRemoteFormAndNothingElse(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/example/app.git":          "example/app",
		"https://github.com/example/app":              "example/app",
		"https://GitHub.com/example/app/":             "example/app",
		"https://x-access-token@github.com/o-1/a.b_c": "o-1/a.b_c",
		"ssh://git@github.com/example/app.git":        "example/app",
		"git@github.com:example/app.git":              "example/app",
		"github.com:example/app":                      "example/app",
		"https://gitlab.com/example/app.git":          "",
		"https://github.com.example.com/example/app":  "",
		"git@gitlab.com:example/app.git":              "",
		"https://github.com/example/app/extra":        "",
		"https://github.com/example":                  "",
		"/srv/repos/app.git":                          "",
		"../app:github.com":                           "",
		"":                                            "",
	} {
		got, ok := githubRepo(remote)
		if got != want || ok != (want != "") {
			t.Errorf("githubRepo(%q) = %q, %v; want %q", remote, got, ok, want)
		}
	}
}

func TestAProjectWithoutAGitHubOriginNeverRunsGh(t *testing.T) {
	fake := citest.Install(t, "", "")
	for _, origin := range []string{"", "git@gitlab.com:example/app.git"} {
		fake.SetGit(origin, "")
		if _, err := Latest(context.Background(), projectDir(t)); !errors.Is(err, ErrNoGitHubRemote) {
			t.Errorf("origin %q: err = %v, want ErrNoGitHubRemote", origin, err)
		}
	}
	if fake.Calls() != 0 {
		t.Errorf("gh ran %d times", fake.Calls())
	}
}

func TestOnlyANoSuchRemoteExitOrNoCheckoutMeansNoGitHubRemote(t *testing.T) {
	fake := citest.Install(t, "", "")
	checkout := projectDir(t)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(checkout, "sub")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	fake.FailOrigin(128, "fatal: detected dubious ownership in repository")
	for _, dir := range []string{checkout, nested} {
		var command *CommandError
		_, err := Latest(context.Background(), dir)
		if !errors.As(err, &command) || err.Error() != "git remote get-url: exit status 128: fatal: detected dubious ownership in repository" {
			t.Errorf("%s: err = %v, want a CommandError naming git's reason", dir, err)
		}
	}
	fake.FailOrigin(128, "fatal: not a git repository")
	if _, err := Latest(context.Background(), projectDir(t)); !errors.Is(err, ErrNoGitHubRemote) {
		t.Errorf("outside a checkout: err = %v, want ErrNoGitHubRemote", err)
	}
	fake.FailOrigin(2, "error: No such remote 'origin'")
	if _, err := Latest(context.Background(), checkout); !errors.Is(err, ErrNoGitHubRemote) {
		t.Errorf("no origin: err = %v, want ErrNoGitHubRemote", err)
	}
}

func TestGhIsPinnedToGitHubDotCom(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	t.Setenv("GH_HOST", "ghe.example.com")
	if _, err := Latest(context.Background(), projectDir(t)); err != nil {
		t.Fatal(err)
	}
	if host := fake.LastHost(); host != "github.com" {
		t.Errorf("gh ran with GH_HOST %q, want github.com", host)
	}
}

func TestACancelledCheckKillsTheChildrenGhStarted(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	pidFile := fake.HangWithChild()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	childPid := make(chan int, 1)
	go func() { // cancel once gh has started its child, however slow the machine
		defer cancel()
		for give := time.Now().Add(20 * time.Second); time.Now().Before(give); time.Sleep(20 * time.Millisecond) {
			if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
				pid, _ := strconv.Atoi(string(data))
				childPid <- pid
				return
			}
		}
		childPid <- 0
	}()
	if _, err := Latest(ctx, projectDir(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the check cancelled", err)
	}
	pid := <-childPid
	if pid <= 0 {
		t.Fatal("gh never started its child")
	}
	for give := time.Now().Add(3 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(give) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("gh's child %d outlived the cancelled check", pid)
		}
	}
}

func TestEveryFailureIsTyped(t *testing.T) {
	fake := citest.Install(t, "git@github.com:example/app.git", "")
	dir := projectDir(t)

	fake.Fail(4, "To get started with GitHub CLI, please run:  gh auth login\nmore")
	_, err := Latest(context.Background(), dir)
	var command *CommandError
	if !errors.As(err, &command) || err.Error() != "gh run list: exit status 4: To get started with GitHub CLI, please run:  gh auth login" {
		t.Errorf("not authenticated: err = %v", err)
	}

	fake.PrintRuns("not json")
	var output *OutputError
	if _, err := Latest(context.Background(), dir); !errors.As(err, &output) {
		t.Errorf("bad output: err = %v, want an OutputError", err)
	}

	fake.Hang()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err = Latest(ctx, dir)
	if !errors.As(err, &command) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("hung gh: err = %v, want a timed out CommandError", err)
	}
	if waited := time.Since(began); waited > 5*time.Second {
		t.Errorf("a hung gh held Latest for %s", waited)
	}

	fake.RemoveGh()
	if _, err := Latest(context.Background(), dir); !errors.Is(err, ErrGhMissing) {
		t.Errorf("no gh: err = %v, want ErrGhMissing", err)
	}
}
