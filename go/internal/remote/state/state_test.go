package state

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestEnsureCreatesAMachineIDOnceAndKeepsIt(t *testing.T) {
	file := File(t.TempDir())
	first, err := Ensure(file)
	if err != nil {
		t.Fatal(err)
	}
	if !uuidV4.MatchString(first.MachineID) {
		t.Errorf("machine id %q is not a lowercase UUID v4", first.MachineID)
	}
	for path, want := range map[string]os.FileMode{filepath.Dir(file): 0o700, file: 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want %04o", path, info.Mode().Perm(), err, want)
		}
	}
	again, err := Ensure(file)
	if err != nil || again.MachineID != first.MachineID {
		t.Errorf("Ensure again = %q, %v; want the kept id %q", again.MachineID, err, first.MachineID)
	}
}

func TestEnsureReportsACorruptFileAndKeepsIt(t *testing.T) {
	const id = `"machine_id":"0f2c7a43-5d3e-4b8a-9c1d-2e3f4a5b6c7d"`
	cases := map[string]string{
		"not JSON":              `{"machine_id":`,
		"unknown field":         `{` + id + `,"projects":{},"extra":1}`,
		"trailing data":         `{` + id + `,"projects":{}} {}`,
		"null":                  `null`,
		"empty object":          `{}`,
		"empty machine id":      `{"machine_id":"","projects":{}}`,
		"null machine id":       `{"machine_id":null,"projects":{}}`,
		"not a UUID":            `{"machine_id":"x","projects":{}}`,
		"uppercase UUID":        `{"machine_id":"0F2C7A43-5D3E-4B8A-9C1D-2E3F4A5B6C7D","projects":{}}`,
		"not version 4":         `{"machine_id":"0f2c7a43-5d3e-1b8a-9c1d-2e3f4a5b6c7d","projects":{}}`,
		"missing projects":      `{` + id + `}`,
		"null projects":         `{` + id + `,"projects":null}`,
		"projects not object":   `{` + id + `,"projects":[]}`,
		"null project entry":    `{` + id + `,"projects":{"k":null}}`,
		"empty project key":     `{` + id + `,"projects":{"":{"last_hash":"h","last_accepted_at":null,"retry_after":null}}}`,
		"project unknown field": `{` + id + `,"projects":{"k":{"extra":1}}}`,
	}
	for name, payload := range cases {
		file := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		var corrupt *CorruptError
		if st, err := Ensure(file); !errors.As(err, &corrupt) || st.MachineID != "" {
			t.Errorf("%s: Ensure = %q, %v; want a CorruptError", name, st.MachineID, err)
		}
		if data, err := os.ReadFile(file); err != nil || string(data) != payload {
			t.Errorf("%s: the corrupt file was changed to %q, %v", name, data, err)
		}
	}
}

func TestLoadAcceptsAValidStateWithProjects(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	payload := `{"machine_id":"0f2c7a43-5d3e-4b8a-9c1d-2e3f4a5b6c7d","projects":{"k":{"last_hash":"h",` +
		`"last_accepted_at":null,"retry_after":null}},"last_push_at":null,"last_pull_at":null,"last_error":null}`
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := Load(file); err != nil || st.Projects["k"].LastHash != "h" {
		t.Errorf("Load = %+v, %v", st, err)
	}
}

func TestConcurrentEnsureAgreesOnOneMachineID(t *testing.T) {
	file := File(t.TempDir())
	const callers = 64
	ids := make([]string, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			<-start
			st, err := Ensure(file)
			ids[i], errs[i] = st.MachineID, err
		})
	}
	close(start)
	wg.Wait()
	assertOneID(t, file, ids, errs)
}

// ensureHelperEnv makes this test binary, re-run as a child, call Ensure on the named file once
// a start file appears, and print the machine id.
const ensureHelperEnv = "STATE_TEST_ENSURE_FILE"

func TestConcurrentEnsureAcrossProcessesAgreesOnOneMachineID(t *testing.T) {
	file := File(t.TempDir())
	startFile := filepath.Join(t.TempDir(), "start")
	const processes = 16
	commands := make([]*exec.Cmd, processes)
	outputs := make([]*strings.Builder, processes)
	for i := range processes {
		outputs[i] = &strings.Builder{}
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestEnsureHelperProcess$")
		commands[i].Env = append(os.Environ(), ensureHelperEnv+"="+file, "STATE_TEST_START_FILE="+startFile)
		commands[i].Stdout, commands[i].Stderr = outputs[i], outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(startFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, processes)
	errs := make([]error, processes)
	for i, command := range commands {
		errs[i] = command.Wait()
		ids[i] = strings.TrimSpace(outputs[i].String())
	}
	assertOneID(t, file, ids, errs)
}

func TestEnsureHelperProcess(t *testing.T) {
	file := os.Getenv(ensureHelperEnv)
	if file == "" {
		t.Skip("run as a child of TestConcurrentEnsureAcrossProcessesAgreesOnOneMachineID")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, err := os.Stat(os.Getenv("STATE_TEST_START_FILE")); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("no start file")
		}
	}
	st, err := Ensure(file)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Print(st.MachineID)
	os.Exit(0) // before the test framework prints PASS, so stdout is only the id
}

// assertOneID checks every caller succeeded with the same canonical id, the id that stayed on
// disk, and that no creator's temp file was left behind.
func assertOneID(t *testing.T, file string, ids []string, errs []error) {
	t.Helper()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v (%s)", i, err, ids[i])
		}
	}
	stored, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if id != stored.MachineID {
			t.Fatalf("caller %d got machine id %q, the file holds %q", i, id, stored.MachineID)
		}
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if entries, err := os.ReadDir(filepath.Dir(file)); err != nil || len(entries) != 1 {
		t.Errorf("state dir holds %v, %v; want only state.json", entries, err)
	}
}

func TestDirSitsBesideTheUsageCache(t *testing.T) {
	if got := Dir("/home/u"); got != "/home/u/.local/state/opencode/orchestrator/remote" {
		t.Errorf("Dir = %s", got)
	}
}
