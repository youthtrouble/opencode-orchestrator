package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain/keychaintest"
)

// remoteSandbox points HOME and the remote config at temp dirs and installs a fake security,
// so no test reaches the real ~/.config/orch or login Keychain.
func remoteSandbox(t *testing.T) (home, configPath string, fake *keychaintest.Fake) {
	t.Helper()
	home = t.TempDir()
	configPath = filepath.Join(home, ".config", "orch", "remote.json")
	t.Setenv("HOME", home)
	t.Setenv("ORCH_REMOTE_CONFIG", configPath)
	return home, configPath, keychaintest.Install(t)
}

func TestRemoteRefusesABadCommandLineWithExit2(t *testing.T) {
	remoteSandbox(t)
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"token"},
		{"token", "mint"},
		{"init"},
		{"init", "--url", "https://trains.example.com", "extra"},
		{"init", "--bogus"},
		{"init", "--url", "http://trains.example.com"},
		{"status", "--yaml"},
		{"token", "rotate", "now"},
		{"service-token", "set", "x"},
	} {
		code, stdout, stderr := run(t, Remote, args, "")
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "orch remote: ") || !strings.Contains(stderr, "usage: orch remote init") {
			t.Errorf("remote %q: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestRemoteInitWritesConfigAndStateAndRefusesToOverwrite(t *testing.T) {
	home, configPath, _ := remoteSandbox(t)
	args := []string{"init", "--url", "https://trains.example.com", "--root", "~/src", "--root", "/work", "--exclude", "scratch"}
	code, stdout, stderr := run(t, Remote, args, "")
	if code != 0 || stderr != "" {
		t.Fatalf("init: code %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "wrote "+configPath+"\nmachine id: ") || !strings.Contains(stdout, "orch remote token rotate") {
		t.Errorf("init stdout = %q", stdout)
	}
	var written map[string]any
	data, err := os.ReadFile(configPath)
	if err != nil || json.Unmarshal(data, &written) != nil {
		t.Fatalf("config %s: %v", data, err)
	}
	if roots := written["roots"].([]any); len(roots) != 2 || roots[0] != "~/src" || written["poll_seconds"] != 30.0 || written["ci"] != true {
		t.Errorf("config = %s", data)
	}
	stateFile := filepath.Join(home, ".local", "state", "opencode", "orchestrator", "remote", "state.json")
	if info, err := os.Stat(stateFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file: %v, %v", info, err)
	}

	code, _, stderr = run(t, Remote, args, "")
	if code != 1 || stderr != "orch remote: "+configPath+" already exists; pass --force to replace it\n" {
		t.Errorf("second init: code %d, stderr %q", code, stderr)
	}
	machineID := regexp.MustCompile(`machine id: (\S+)`).FindStringSubmatch(stdout)[1]
	code, forced, _ := run(t, Remote, append(args, "--force"), "")
	if code != 0 || !strings.Contains(forced, "machine id: "+machineID+"\n") {
		t.Errorf("init --force: code %d, stdout %q; want the kept machine id %s", code, forced, machineID)
	}
}

func TestRemoteTokenRotateStoresTheTokenAndPrintsOnlyItsHash(t *testing.T) {
	_, _, fake := remoteSandbox(t)
	code, stdout, stderr := run(t, Remote, []string{"token", "rotate"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("rotate: code %d, stderr %q", code, stderr)
	}
	token, ok := fake.Stored("machine-token")
	if !ok || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(token) {
		t.Fatalf("stored token %q is not 32 bytes of unpadded base64url", token)
	}
	sum := sha256.Sum256([]byte(token))
	lines := strings.Split(stdout, "\n")
	if lines[0] != hex.EncodeToString(sum[:]) {
		t.Errorf("first line %q, want sha256 of the stored token", lines[0])
	}
	if lines[1] != "cd remote/worker && wrangler secret put MACHINE_TOKEN_SHA256 -c wrangler.deploy.jsonc   # paste the hash above" {
		t.Errorf("second line %q", lines[1])
	}
	if !strings.Contains(lines[2], "pushes get 403") {
		t.Errorf("third line %q does not warn that pushes fail until the Worker has the hash", lines[2])
	}
	if strings.Contains(stdout, token) || strings.Contains(fake.ArgvLog(), token) {
		t.Error("the token reached stdout or security's argv")
	}
	if strings.Count(fake.StdinLog(), token) != 1 || !strings.HasPrefix(fake.StdinLog(), "add-generic-password ") {
		t.Errorf("the token must reach security only once, on the stdin of -i: %q", fake.StdinLog())
	}
}

func TestRemoteTokenRevokeIsIdempotent(t *testing.T) {
	_, _, fake := remoteSandbox(t)
	fake.Put("machine-token", "old-token")
	code, stdout, _ := run(t, Remote, []string{"token", "revoke"}, "")
	if code != 0 || !strings.HasPrefix(stdout, "machine-token: deleted\naccess-client-id: not present\naccess-client-secret: not present\nWorker side: ") {
		t.Errorf("revoke: code %d, stdout %q", code, stdout)
	}
	if _, ok := fake.Stored("machine-token"); ok {
		t.Error("revoke left the machine token")
	}
	code, stdout, _ = run(t, Remote, []string{"token", "revoke"}, "")
	if code != 0 || !strings.HasPrefix(stdout, "machine-token: not present\n") {
		t.Errorf("second revoke: code %d, stdout %q", code, stdout)
	}
}

func TestRemoteServiceTokenSetReadsTwoLinesAndNeverEchoesThem(t *testing.T) {
	_, _, fake := remoteSandbox(t)
	code, stdout, stderr := run(t, Remote, []string{"service-token", "set"}, "client-id.access\r\nclient-secret-value")
	if code != 0 || stdout != "stored access-client-id and access-client-secret in the login Keychain\n" || stderr != "" {
		t.Fatalf("set: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if id, _ := fake.Stored("access-client-id"); id != "client-id.access" {
		t.Errorf("client id %q", id)
	}
	if secret, _ := fake.Stored("access-client-secret"); secret != "client-secret-value" {
		t.Errorf("client secret %q", secret)
	}
	if strings.Contains(fake.ArgvLog(), "client-secret-value") {
		t.Error("the secret reached security's argv")
	}

	code, _, stderr = run(t, Remote, []string{"service-token", "set"}, "only-the-id\n")
	if code != 1 || stderr != "orch remote: stdin ended before the Access client secret\n" {
		t.Errorf("one line: code %d, stderr %q", code, stderr)
	}
}

func TestRemoteServiceTokenSetValidatesBothBeforeStoringEither(t *testing.T) {
	_, _, fake := remoteSandbox(t)
	fake.Put("access-client-id", "old-id")
	code, _, stderr := run(t, Remote, []string{"service-token", "set"}, "new-id\nsecret with spaces\n")
	if code != 1 || !strings.Contains(stderr, "refusing to store access-client-secret") {
		t.Errorf("set: code %d, stderr %q", code, stderr)
	}
	if id, _ := fake.Stored("access-client-id"); id != "old-id" || fake.StdinLog() != "" {
		t.Errorf("a refused secret still changed the Keychain: id %q, stdin %q", id, fake.StdinLog())
	}
}

func TestRemoteServiceTokenSetNamesTheItemThatChangedWhenTheSecondStoreFails(t *testing.T) {
	_, _, fake := remoteSandbox(t)
	fake.RefuseStore("access-client-secret")
	code, _, stderr := run(t, Remote, []string{"service-token", "set"}, "new-id\nnew-secret\n")
	want := "orch remote: stored the new access-client-id, but not the access-client-secret, so the two no longer match; run orch remote service-token set again: "
	if code != 1 || !strings.HasPrefix(stderr, want) || strings.Contains(stderr, "new-secret") {
		t.Errorf("set: code %d, stderr %q", code, stderr)
	}
	if id, _ := fake.Stored("access-client-id"); id != "new-id" {
		t.Errorf("client id %q", id)
	}
}

func TestRemoteStatusJSONReportsPresenceButNoSecrets(t *testing.T) {
	_, _, fake := remoteSandbox(t)
	if code, _, stderr := run(t, Remote, []string{"init", "--url", "https://trains.example.com"}, ""); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	fake.Put("machine-token", "machine-token-secret")
	fake.Put("access-client-secret", "access-secret-value")
	code, stdout, stderr := run(t, Remote, []string{"status", "--json"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("status: code %d, stderr %q", code, stderr)
	}
	for _, secret := range []string{"machine-token-secret", "access-secret-value"} {
		if strings.Contains(stdout, secret) {
			t.Errorf("status --json shows the secret %q", secret)
		}
	}
	var report struct {
		Config    struct{ URL string }
		MachineID *string           `json:"machine_id"`
		Keychain  map[string]string `json:"keychain"`
		Projects  *int              `json:"projects"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	wantKeychain := map[string]string{"machine-token": "yes", "access-client-id": "no", "access-client-secret": "yes"}
	if report.Config.URL != "https://trains.example.com" || report.MachineID == nil || report.Projects != nil || !maps.Equal(report.Keychain, wantKeychain) {
		t.Errorf("status = %s", stdout)
	}
}

func TestRemoteStatusWithoutConfigAndALockedKeychain(t *testing.T) {
	_, configPath, fake := remoteSandbox(t)
	fake.Lock()
	code, stdout, stderr := run(t, Remote, []string{"status"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("status: code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"config:      " + configPath + " (missing; run orch remote init --url URL)\n",
		"machine id:  none (run orch remote init)\n",
		"keychain:    machine-token unknown, access-client-id unknown, access-client-secret unknown\n",
		"the login Keychain is locked",
		"last push:   never\n",
		"projects:    not yet",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, stdout)
		}
	}
}

func TestRemoteStatusReportsARefusedConfigAndTheRestThenExits1(t *testing.T) {
	_, configPath, _ := remoteSandbox(t)
	if code, _, stderr := run(t, Remote, []string{"init", "--url", "https://trains.example.com"}, ""); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, Remote, []string{"status"}, "")
	if code != 1 || !strings.Contains(stderr, "must be owner-only") {
		t.Errorf("status: code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"config:      refused: " + configPath + " has mode 0644", "machine id:  ", "keychain:    machine-token no"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, stdout)
		}
	}

	code, stdout, _ = run(t, Remote, []string{"status", "--json"}, "")
	var report struct {
		Config      *struct{}         `json:"config"`
		ConfigError *string           `json:"config_error"`
		MachineID   *string           `json:"machine_id"`
		Keychain    map[string]string `json:"keychain"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if code != 1 || report.Config != nil || report.ConfigError == nil || !strings.Contains(*report.ConfigError, "must be owner-only") ||
		report.MachineID == nil || report.Keychain["machine-token"] != "no" {
		t.Errorf("status --json: code %d, %s", code, stdout)
	}
}
