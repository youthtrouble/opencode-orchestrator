package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

func TestKeyIsAStableOpaqueDigestOfMachineAndPath(t *testing.T) {
	key := Key(testMachineID, "/srv/owner-home/Developer/app")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(key) || strings.Contains(key, "app") {
		t.Errorf("Key = %q, want 32 lowercase hex digits", key)
	}
	if again := Key(testMachineID, "/srv/owner-home/Developer/app"); again != key {
		t.Errorf("Key is not stable: %q then %q", key, again)
	}
	if Key("00000000-0000-4000-8000-000000000002", "/srv/owner-home/Developer/app") == key ||
		Key(testMachineID, "/srv/owner-home/Developer/app2") == key {
		t.Error("Key does not depend on both the machine id and the path")
	}
}

// project writes files under a temp project's .orchestrator/ and returns it.
func project(t *testing.T, files map[string]string) discover.Project {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, ".orchestrator", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return discover.Project{Name: "app", Root: root, RealPath: root}
}

func TestBuildTreatsUnparsableStateFilesAsNoRecord(t *testing.T) {
	snapshot, err := Build(project(t, map[string]string{
		"flip.json":              `[1, 2]`,
		"workers/conductor.json": `{"status":`,
		"sessions/claude.json":   `"just a string"`,
		"sessions/opencode.json": `{"state":"working","updated_at":"not a time"}`,
	}), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != stateIdle || snapshot.Door != nil || snapshot.Run.Kind != "none" || snapshot.Slice != nil {
		t.Errorf("snapshot = %+v, want an idle project with nothing known", snapshot)
	}
}

func TestBuildReportsUnreadableStateAsAnInputError(t *testing.T) {
	p := project(t, map[string]string{"flip.json": `{}`})
	if err := os.MkdirAll(filepath.Join(p.Root, ".orchestrator", "mailbox", "messages.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Build(p, testEnv())
	var input *InputError
	if !errors.As(err, &input) || input.File != filepath.Join(p.Root, ".orchestrator", "mailbox") {
		t.Errorf("Build = %v, want an InputError for the mailbox", err)
	}
}

func TestQuestionsAndEventsFollowTheContract(t *testing.T) {
	line := func(id, kind, to, extra string) string {
		return `{"id":"` + id + `","at":"2026-10-20T11:00:00.000Z","from":"conductor","to":"` + to + `","kind":"` + kind +
			`","subject":"s-` + id + `","body":"secret body ` + id + `","refs":[],"thread":null` + extra + "}\n"
	}
	legacy, toCoder, acked, noTime := testID(26), testID(27), testID(28), testID(29)
	var messages strings.Builder
	for i := range 25 {
		messages.WriteString(line(testID(i+1), "question", "human", `,"recommended":"yes","blocking":false`))
	}
	messages.WriteString(line(legacy, "question", "*", ``))
	messages.WriteString(line(toCoder, "question", "coder", `,"recommended":"yes","blocking":true`))
	messages.WriteString(line(acked, "question", "human", `,"recommended":"yes","blocking":true`))
	messages.WriteString(`{"id":"` + noTime + `","at":"yesterday","from":"x","to":"*","kind":"note","subject":"s"}` + "\n")
	messages.WriteString(`not json at all` + "\n")
	snapshot, err := Build(project(t, map[string]string{
		"mailbox/messages.jsonl": messages.String(),
		"mailbox/acks.jsonl":     `{"id":"` + acked + `","by":"human","at":"2026-10-20T11:01:00.000Z","note":"replied"}` + "\n",
	}), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Questions) != maxQuestions || snapshot.Questions[0].ID != legacy || snapshot.Questions[1].ID != testID(25) {
		t.Fatalf("questions: %d, first %q then %q; want the newest %d open ones, newest first", len(snapshot.Questions),
			snapshot.Questions[0].ID, snapshot.Questions[1].ID, maxQuestions)
	}
	if legacy := snapshot.Questions[0]; legacy.Recommended != nil || legacy.Blocking {
		t.Errorf("a legacy question = %+v, want no recommendation and not blocking", legacy)
	}
	if snapshot.State != stateIdle {
		t.Errorf("state %q: only non-blocking questions are open", snapshot.State)
	}
	if len(snapshot.Events) != maxEvents || snapshot.Events[0].ID != acked {
		t.Errorf("events: %d, first %q; want the newest %d carried lines", len(snapshot.Events), snapshot.Events[0].ID, maxEvents)
	}
	for _, event := range snapshot.Events {
		if strings.Contains(event.Subject, "secret body") {
			t.Errorf("event %q carries a body", event.ID)
		}
	}
}

// testID is the n-th id in the lowercase uuid form orch's writers generate.
func testID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// mailboxLine is a mailbox message line at the given minute past 11:00 on the test day.
func mailboxLine(id string, minute int, extra string) string {
	return fmt.Sprintf(`{"id":%q,"at":"2026-10-20T11:%02d:00.000Z","from":"conductor","to":"human","kind":"question",`+
		`"subject":"s","body":"b","refs":[],"thread":null%s}`+"\n", id, minute, extra)
}

func TestMessagesWithUnsendableIDsAreLeftOut(t *testing.T) {
	planted := []string{
		"/Users/someone/secret-plan",
		"owner@example.com",
		"sk-test0123456789abcdefghijklmnop",
		fakeToken("AK"+"IA", 16),
		"has space",
		"dotted.name",
		"id-1",
		"00000000-0000-4000-8000-0000000000AB", // uppercase hex
		strings.Repeat("a", 65),
	}
	sendable := []string{testID(101), testID(102)}
	var messages strings.Builder
	for i, id := range append(planted, sendable...) {
		messages.WriteString(mailboxLine(id, i, ""))
	}
	snapshot, err := Build(project(t, map[string]string{"mailbox/messages.jsonl": messages.String()}), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, event := range snapshot.Events {
		ids = append(ids, event.ID)
	}
	if want := []string{sendable[1], sendable[0]}; !slices.Equal(ids, want) || len(snapshot.Questions) != len(want) {
		t.Errorf("event ids = %q and %d questions, want only %q", ids, len(snapshot.Questions), want)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"/Users/someone", "owner@example.com", "sk-test", "AK" + "IA", "dotted.name", "id-1"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the snapshot carries %q", leak)
		}
	}
}

func TestBlockingQuestionsCountBeyondTheListCap(t *testing.T) {
	var messages strings.Builder
	messages.WriteString(mailboxLine(testID(1), 0, `,"recommended":"approve","blocking":true`))
	for i := range maxQuestions + 5 {
		messages.WriteString(mailboxLine(testID(i+2), i+1, `,"recommended":"yes","blocking":false`))
	}
	snapshot, err := Build(project(t, map[string]string{"mailbox/messages.jsonl": messages.String()}), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Questions) != maxQuestions || slices.ContainsFunc(snapshot.Questions, func(q wire.Question) bool { return q.Blocking }) {
		t.Fatalf("questions: %d, want the newest %d, none blocking", len(snapshot.Questions), maxQuestions)
	}
	if snapshot.State != stateNeedsYou || snapshot.StateReason != "1 blocking question" {
		t.Errorf("state %q (%q), want needs_you from the blocking question past the cap", snapshot.State, snapshot.StateReason)
	}
}

func TestFlipPausedAndAwaitingHumanUseJavaScriptTruthiness(t *testing.T) {
	cases := map[string]struct {
		flip, state, reason string
	}{
		"paused true":           {`{"paused":true}`, statePaused, "both vendors red until a reset"},
		"paused object":         {`{"paused":{"reason":"red"}}`, statePaused, "both vendors red until a reset"},
		"paused empty object":   {`{"paused":{}}`, statePaused, "both vendors red until a reset"},
		"paused false":          {`{"paused":false}`, stateIdle, "nothing is running"},
		"paused zero":           {`{"paused":0}`, stateIdle, "nothing is running"},
		"paused empty string":   {`{"paused":""}`, stateIdle, "nothing is running"},
		"paused null":           {`{"paused":null}`, stateIdle, "nothing is running"},
		"awaiting_human true":   {`{"awaiting_human":true}`, statePaused, "awaiting human"},
		"awaiting_human text":   {`{"awaiting_human":"deploy"}`, statePaused, "awaiting human"},
		"awaiting_human object": {`{"awaiting_human":{"reason":"deploy","question":"q1"}}`, stateNeedsYou, "awaiting your answer: deploy"},
		"awaiting_human false":  {`{"awaiting_human":false}`, stateIdle, "nothing is running"},
	}
	for name, c := range cases {
		snapshot, err := Build(project(t, map[string]string{"flip.json": c.flip}), testEnv())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if snapshot.State != c.state || snapshot.StateReason != c.reason {
			t.Errorf("%s: %q (%q), want %q (%q)", name, snapshot.State, snapshot.StateReason, c.state, c.reason)
		}
	}
}

func TestSnapshotHashChangesWithContentOnly(t *testing.T) {
	files := map[string]string{"sessions/claude.json": `{"state":"working","now":"one","updated_at":"2026-10-20T11:59:00.000Z"}`}
	p := project(t, files)
	first, err := Build(p, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Build(p, testEnv())
	if again.SnapshotHash != first.SnapshotHash || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first.SnapshotHash) {
		t.Errorf("hash %q then %q, want one stable sha256", first.SnapshotHash, again.SnapshotHash)
	}
	later := testEnv()
	later.Now = testNow.Add(20 * time.Minute) // the heartbeat goes stale: working -> idle
	if aged, _ := Build(p, later); aged.SnapshotHash == first.SnapshotHash {
		t.Error("the hash did not change when the state did")
	}
}

// Credentials and complete paths planted in every free-text field never reach the wire.
func TestBuildScrubsCredentialsAndPathsFromEveryFreeTextField(t *testing.T) {
	github, aws, slack := fakeToken("ghp_", 36), fakeToken("AK"+"IA", 16), fakeToken("xo"+"xp-", 30)
	backup := fakeToken("gh"+"o_", 36) // glued to a word: backup_gho_…
	question, err := json.Marshal(map[string]any{
		"id": testID(1), "at": "2026-10-20T11:00:00.000Z", "from": "conductor", "to": "human", "kind": "question",
		"subject": "Rotate " + github + "?", "body": "It sits in /Volumes/PrivateClient/keys with password=fake-pass-1, backup_" + backup +
			` in "/Volumes/Private Client/a b.txt" and /Volumes/Private Client/keys/old.txt; also /Volumes/Private Client` +
			"\nthen /Volumes/Private client, /srv/private customer; (/Volumes/Private Client) ['/srv/private customer']" +
			" and /srv/private customer at https://trains.example.com/docs/setup and 1/2 done\n/Volumes/Private client",
		"recommended": "Revoke " + aws + " and backup_" + backup + " under ~/creds/aws, then /private-client.env", "blocking": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := `{"id":"` + testID(2) + `","at":"2026-10-20T11:01:00.000Z","from":"conductor","to":"*","kind":"note",` +
		`"subject":"Posted ` + slack + ` from /private/tmp/x/run"}` + "\n"
	snapshot, err := Build(project(t, map[string]string{
		"mailbox/messages.jsonl": string(question) + "\n" + event,
		"flip.json":              `{"awaiting_human":{"reason":"check /srv/app/secrets token=fake-2"}}`,
		"sessions/claude.json": `{"state":"working","now":"Editing /opt/client/plan.md","slice":"S1 ` + github + `",` +
			`"updated_at":"2026-10-20T11:59:00.000Z"}`,
	}), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{github, aws, slack, "fake-pass-1", "fake-2", backup, "/Volumes", "PrivateClient", "Private Client", "Private client", "private customer", "private-client.env", "~/creds", "/private", "/srv/app", "/opt/client"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the snapshot carries %q: %s", leak, body)
		}
	}
	if len(snapshot.Questions) != 1 || len(snapshot.Events) != 2 {
		t.Fatalf("%d questions and %d events, want the planted 1 and 2", len(snapshot.Questions), len(snapshot.Events))
	}
	if got := *snapshot.Questions[0].Recommended; got != "Revoke <token> and backup_<token> under <path>, then <path>" {
		t.Errorf("recommended = %q", got)
	}
	if got, want := snapshot.Questions[0].Body, "; also <path>\nthen <path>, <path>; (<path>) ['<path>']"+
		" and <path> https://trains.example.com/docs/setup and 1/2 done\n<path>"; !strings.HasSuffix(got, want) {
		t.Errorf("body = %q, want it to end %q", got, want)
	}
}

// fixedCI provides the same runs for every project.
type fixedCI []wire.CiRun

func (runs fixedCI) LatestRuns(discover.Project) []wire.CiRun { return runs }

func TestBuildRedactsAndCapsTheProvidersCIRuns(t *testing.T) {
	link := func(s string) *string { return &s }
	success := "success"
	run := wire.CiRun{Repo: "example/app", Workflow: "ci", Status: "completed", Conclusion: &success, URL: link("https://github.com/example/app/actions/runs/1"), At: "2026-10-20T11:00:00Z"}
	runs := fixedCI{run, run, run, run, run, run}
	token := fakeToken("ghp_", 36)
	runs[1].Repo = ""                                       // nothing to name it by: left out
	runs[2].Workflow = "deploy " + strings.Repeat("x", 120) // cut
	runs[3].Workflow = "deploy " + testHome + "/keys, " + token
	runs[3].URL = link("https://ci.example.com/runs/1") // not github.com
	runs[4].URL = link("https://github.com" + testHome) // redaction would rewrite it
	env := testEnv()
	env.CI = runs

	p := project(t, nil)
	snapshot, err := Build(p, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.CI) != 5 || snapshot.CI[0] != run || snapshot.CI[4] != runs[5] {
		t.Fatalf("ci = %+v, want five runs, the empty-repo one left out", snapshot.CI)
	}
	if workflow := snapshot.CI[1].Workflow; !strings.HasPrefix(workflow, "deploy x") || utf16Length(workflow) != maxWorkflowUnits || !strings.HasSuffix(workflow, "…") {
		t.Errorf("workflow = %q, want it cut to %d", workflow, maxWorkflowUnits)
	}
	if workflow := snapshot.CI[2].Workflow; workflow != "deploy <path>, <token>" {
		t.Errorf("workflow = %q, want the path and the credential scrubbed like every outbound text", workflow)
	}
	if snapshot.CI[2].URL != nil || snapshot.CI[3].URL != nil || snapshot.CI[0].URL == nil {
		t.Errorf("urls = %v %v %v, want only the intact github.com link", snapshot.CI[0].URL, snapshot.CI[2].URL, snapshot.CI[3].URL)
	}
	without, _ := Build(p, testEnv())
	if without.SnapshotHash == snapshot.SnapshotHash || len(without.CI) != 0 || without.CI == nil {
		t.Errorf("without CI: ci %v, hash unchanged %v; want [] and a different hash", without.CI, without.SnapshotHash == snapshot.SnapshotHash)
	}
}

func TestBuildTreatsANilCIProviderAsNoCI(t *testing.T) {
	env := testEnv()
	env.CI = nil
	snapshot, err := Build(project(t, nil), env)
	if err != nil || snapshot.CI == nil || len(snapshot.CI) != 0 {
		t.Errorf("Build = ci %v, err %v; want [] and no error", snapshot.CI, err)
	}
}
