package cli

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/loop"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
)

// onceWorker is a Worker with nothing to pull that accepts every push, and records the
// credentials each request carried.
type onceWorker struct {
	mu      sync.Mutex
	bearers []string
	secrets []string
	pushed  int
}

func (w *onceWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bearers = append(w.bearers, r.Header.Get("Authorization"))
	w.secrets = append(w.secrets, r.Header.Get("CF-Access-Client-Secret"))
	rw.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/machine/pull":
		io.WriteString(rw, `{"v":1,"server_time":"2026-10-21T09:00:00.000Z","poll_after_seconds":30,"answers":[],"actions":[]}`)
	case "/api/v1/machine/push":
		var push wire.PushRequest
		if err := wire.Decode(bytes.NewReader(body), &push); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			io.WriteString(rw, `{"error":"invalid_request","message":""}`)
			return
		}
		results := []wire.PushResult{}
		for _, project := range push.Projects {
			results = append(results, wire.PushResult{Key: project.Key, Result: "accepted"})
		}
		w.pushed += len(push.Projects)
		json.NewEncoder(rw).Encode(wire.PushResponse{V: 1, ServerTime: "2026-10-21T09:00:00.000Z", Projects: results})
	default:
		rw.WriteHeader(http.StatusNotFound)
		io.WriteString(rw, `{"error":"not_found","message":""}`)
	}
}

// onceSetup initialises orch remote against url with two projects and the Keychain items set.
func onceSetup(t *testing.T, url string) (home string) {
	home, _, fake := remoteSandbox(t)
	workTrainsProjects(t, home)
	if code, _, stderr := run(t, Remote, []string{"init", "--url", url}, ""); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	fake.Put("machine-token", "fake-machine-token-7f3a")
	fake.Put("access-client-id", "fake-client-id.access")
	fake.Put("access-client-secret", "fake-client-secret-91bc")
	return home
}

func TestRemoteOncePullsPushesAndPrintsTheSummary(t *testing.T) {
	worker := &onceWorker{}
	server := httptest.NewServer(worker)
	defer server.Close()
	home := onceSetup(t, server.URL)

	code, stdout, stderr := run(t, Remote, []string{"once"}, "")
	if code != 0 || stdout != "pulled 0, applied 0, pushed 2, deferred 0\n" {
		t.Fatalf("once: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "orch remote: pushed 2 project(s), 0 deferred") {
		t.Errorf("once stderr = %q, want the push logged", stderr)
	}
	if worker.pushed != 2 || worker.bearers[0] != "Bearer fake-machine-token-7f3a" || worker.secrets[0] != "fake-client-secret-91bc" {
		t.Errorf("worker saw %d projects, bearers %q", worker.pushed, worker.bearers)
	}
	st, err := state.Load(state.File(home))
	if err != nil || st.LastPushAt == nil || st.LastPullAt == nil || len(st.Projects) != 2 {
		t.Errorf("state = %+v, %v", st, err)
	}

	code, stdout, _ = run(t, Remote, []string{"once", "--json"}, "")
	var summary map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &summary) != nil || summary["pushed"] != 0.0 || summary["error"] != nil {
		t.Errorf("once --json: code %d, stdout %q", code, stdout)
	}
}

func TestRemoteOnceExits1AndRecordsTheErrorWhenTheWorkerIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	home := onceSetup(t, url)

	code, stdout, stderr := run(t, Remote, []string{"once", "--json"}, "")
	var summary struct{ Error *string }
	if code != 1 || json.Unmarshal([]byte(stdout), &summary) != nil || summary.Error == nil || !strings.Contains(*summary.Error, "network") {
		t.Fatalf("once: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.HasPrefix(stderr, "orch remote: GET /api/v1/machine/pull: network: ") {
		t.Errorf("stderr = %q", stderr)
	}
	if st, err := state.Load(state.File(home)); err != nil || st.LastError == nil || st.LastError.Kind != "network" {
		t.Errorf("state last_error = %+v, %v", st.LastError, err)
	}
}

// A Worker, or something in front of it, may echo what it was sent. once must print and store
// only fixed words for each such response: the echoed Access client secret below is not
// token-shaped, so no redactor would catch it.
func TestRemoteOnceNeverPrintsOrStoresResponseText(t *testing.T) {
	const secret = "fake-client-secret-91bc" // what onceSetup puts in the Keychain
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		kind    string
	}{{
		name: "an error code that is not the protocol's",
		handler: func(rw http.ResponseWriter, r *http.Request) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusForbidden)
			json.NewEncoder(rw).Encode(map[string]string{"error": r.Header.Get("CF-Access-Client-Secret"), "message": ""})
		},
		kind: "http_status",
	}, {
		name: "an unknown field named after the secret",
		handler: func(rw http.ResponseWriter, r *http.Request) {
			rw.Header().Set("Content-Type", "application/json")
			io.WriteString(rw, `{"v":1,"server_time":"2026-10-21T09:00:00.000Z","poll_after_seconds":30,"answers":[],"actions":[],"`+r.Header.Get("CF-Access-Client-Secret")+`":1}`)
		},
		kind: "protocol",
	}, {
		name: "a malformed redirect location with user info",
		handler: func(rw http.ResponseWriter, r *http.Request) {
			rw.Header().Set("Location", "https://user:"+r.Header.Get("CF-Access-Client-Secret")+"@example.com/%zz")
			rw.WriteHeader(http.StatusFound)
		},
		kind: "access_denied",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			home := onceSetup(t, server.URL)

			code, stdout, stderr := run(t, Remote, []string{"once", "--json"}, "")
			var summary struct{ Error *string }
			if code != 1 || json.Unmarshal([]byte(stdout), &summary) != nil || summary.Error == nil {
				t.Fatalf("once: code %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			stateData, err := os.ReadFile(state.File(home))
			if err != nil {
				t.Fatal(err)
			}
			for sink, text := range map[string]string{"stdout": stdout, "stderr": stderr, "state.json": string(stateData)} {
				if strings.Contains(text, secret) {
					t.Errorf("%s holds the echoed secret: %s", sink, text)
				}
			}
			if st, err := state.Load(state.File(home)); err != nil || st.LastError == nil || st.LastError.Kind != tc.kind || st.LastError.Message != *summary.Error {
				t.Errorf("last_error = %+v, %v; want kind %s and the printed error", st.LastError, err, tc.kind)
			}
		})
	}
}

func TestRemoteOnceNamesTheMissingKeychainItem(t *testing.T) {
	home, _, _ := remoteSandbox(t)
	workTrainsProjects(t, home)
	if code, _, stderr := run(t, Remote, []string{"init", "--url", "https://trains.example.com"}, ""); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	code, _, stderr := run(t, Remote, []string{"once"}, "")
	if code != 1 || !strings.Contains(stderr, "orch remote: no machine-token in the login Keychain; run orch remote token rotate") {
		t.Errorf("once without a token: code %d, stderr %q", code, stderr)
	}
}

func TestRemoteServeWithALiveLockExits1(t *testing.T) {
	home, _, _ := remoteSandbox(t)
	if code, _, stderr := run(t, Remote, []string{"init", "--url", "https://trains.example.com"}, ""); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	lock, err := loop.AcquireLock(filepath.Join(state.Dir(home), "serve.lock"), 4242)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	code, stdout, stderr := run(t, Remote, []string{"serve"}, "")
	if code != 1 || stdout != "" || stderr != "orch remote: another orch remote serve is running (pid 4242)\n" {
		t.Errorf("serve: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestRemoteOnceWhileServeHoldsTheLockExits1AndSendsNothing(t *testing.T) {
	worker := &onceWorker{}
	server := httptest.NewServer(worker)
	defer server.Close()
	home := onceSetup(t, server.URL)
	lockFile := filepath.Join(state.Dir(home), "serve.lock")
	lock, err := loop.AcquireLock(lockFile, 4242)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, Remote, []string{"once"}, "")
	if code != 1 || stdout != "" || stderr != "orch remote: orch remote serve is running; it syncs on its own (pid 4242)\n" {
		t.Errorf("once: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if len(worker.bearers) != 0 {
		t.Errorf("once sent %d request(s) while serve held the lock", len(worker.bearers))
	}

	lock.Release()
	if code, _, stderr := run(t, Remote, []string{"once"}, ""); code != 0 {
		t.Fatalf("once after serve stopped: code %d, stderr %q", code, stderr)
	}
	relock, err := loop.AcquireLock(lockFile, 4343)
	if err != nil {
		t.Fatalf("once kept the lock: %v", err)
	}
	relock.Release()
}

func TestRemoteOnceRedactsTheHomeDirectoryFromItsError(t *testing.T) {
	server := httptest.NewServer(&onceWorker{})
	defer server.Close()
	home := onceSetup(t, server.URL)
	// A read-only state directory fails the state save, an error that names the file's path.
	stateDir := state.Dir(home)
	lock, err := loop.AcquireLock(filepath.Join(stateDir, "serve.lock"), 4242)
	if err != nil {
		t.Fatal(err)
	}
	lock.Release() // the lock file must exist before its directory turns read-only
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stateDir, 0o700) })

	code, stdout, stderr := run(t, Remote, []string{"once", "--json"}, "")
	var summary struct{ Error *string }
	if code != 1 || json.Unmarshal([]byte(stdout), &summary) != nil || summary.Error == nil {
		t.Fatalf("once: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, home) || strings.Contains(stderr, home) {
		t.Errorf("once printed the home directory:\nstdout %s\nstderr %s", stdout, stderr)
	}
	if !strings.Contains(*summary.Error, "save the state") || !strings.Contains(*summary.Error, "~/") {
		t.Errorf("error = %q, want the save failure with HOME as ~", *summary.Error)
	}
	if !strings.HasSuffix(stderr, "orch remote: "+*summary.Error+"\n") {
		t.Errorf("stderr = %q, want it to end with the same redacted error", stderr)
	}
}

func TestRemoteServeNeedsInit(t *testing.T) {
	remoteSandbox(t)
	code, _, stderr := run(t, Remote, []string{"serve"}, "")
	if code != 1 || !strings.Contains(stderr, "run orch remote init --url URL") {
		t.Errorf("serve: code %d, stderr %q", code, stderr)
	}
}

func TestRemoteLaunchdPrintsThePlistAndTheInstallCommandsAndWritesNothing(t *testing.T) {
	home, _, _ := remoteSandbox(t)
	before := listTree(t, home)
	code, stdout, stderr := run(t, Remote, []string{"launchd"}, "")
	if code != 0 {
		t.Fatalf("launchd: code %d, stderr %q", code, stderr)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(home, ".local", "state", "opencode", "orchestrator", "remote", "remote.log")
	if got, want := plistValues(t, stdout), []string{
		"key:Label", "string:org.opencode-orchestrator.work-trains",
		"key:ProgramArguments", "string:" + self, "string:remote", "string:serve",
		"key:RunAtLoad", "true:", "key:KeepAlive", "true:", "key:ThrottleInterval", "integer:30",
		"key:StandardOutPath", "string:" + logFile, "key:StandardErrorPath", "string:" + logFile,
		"key:EnvironmentVariables", "key:PATH", "string:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin",
	}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plist values:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	plistFile := filepath.Join(home, "Library", "LaunchAgents", "org.opencode-orchestrator.work-trains.plist")
	wantErr := "# Install: save the plist above as '" + plistFile + "', then run\n" +
		"#   launchctl bootstrap gui/$(id -u) '" + plistFile + "'\n" +
		"# Uninstall:\n" +
		"#   launchctl bootout gui/$(id -u)/org.opencode-orchestrator.work-trains\n"
	if stderr != wantErr {
		t.Errorf("stderr = %q, want %q", stderr, wantErr)
	}
	if !maps.Equal(before, listTree(t, home)) {
		t.Error("launchd wrote under HOME")
	}

	_, stdout, stderr = run(t, Remote, []string{"launchd", "--label", "com.example.trains"}, "")
	if !strings.Contains(stdout, "<string>com.example.trains</string>") || !strings.Contains(stderr, "bootout gui/$(id -u)/com.example.trains\n") {
		t.Errorf("--label: stdout %q, stderr %q", stdout, stderr)
	}
}

func TestRemoteLaunchdQuotesThePlistPathForTheShell(t *testing.T) {
	remoteSandbox(t)
	home := filepath.Join(t.TempDir(), "Jo O'Brien's home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	code, _, stderr := run(t, Remote, []string{"launchd"}, "")
	if code != 0 {
		t.Fatalf("launchd: code %d, stderr %q", code, stderr)
	}
	const prefix = "#   launchctl bootstrap gui/$(id -u) "
	var quoted string
	for _, line := range strings.Split(stderr, "\n") {
		if rest, found := strings.CutPrefix(line, prefix); found {
			quoted = rest
		}
	}
	plistFile := filepath.Join(home, "Library", "LaunchAgents", "org.opencode-orchestrator.work-trains.plist")
	if want := `'` + strings.ReplaceAll(plistFile, `'`, `'\''`) + `'`; quoted != want {
		t.Fatalf("bootstrap path = %q, want %q", quoted, want)
	}
	// The shell must read the quoted path back as one word, exactly the plist's path.
	words, err := exec.Command("/bin/sh", "-c", `printf '%s\n' `+quoted).Output()
	if err != nil || string(words) != plistFile+"\n" {
		t.Errorf("sh read %q, %v; want %q as one word", words, err, plistFile)
	}
}

// plistValues parses a plist as XML and lists each leaf element as "name:text", in order.
func plistValues(t *testing.T, plist string) []string {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(plist))
	decoder.Strict = true
	var values []string
	var name, text string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return values
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, plist)
		}
		switch tok := token.(type) {
		case xml.StartElement:
			name, text = tok.Name.Local, ""
		case xml.CharData:
			text += string(tok)
		case xml.EndElement:
			if tok.Name.Local == name {
				values = append(values, name+":"+text)
			}
			name = ""
		}
	}
}
