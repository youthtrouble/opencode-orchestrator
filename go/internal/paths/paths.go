// Package paths names the files and directories orch shares with the JS runtime, so both
// read and write the same state.
package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Paths are the per-user locations, resolved once from the environment.
type Paths struct {
	Home string
	// OrchHome is the JS ROOT: the orchestrator tree holding models.json and bin/. The JS
	// derives it from its own script location; orch reads ORCH_HOME, defaulting to the
	// installed tree.
	OrchHome      string
	RegistryFile  string // ORCH_REGISTRY, default <OrchHome>/models.json (JS REGISTRY_FILE)
	StateDir      string // ~/.local/state/opencode/orchestrator (JS STATE_DIR)
	UsageFile     string // <StateDir>/usage.json (JS USAGE_FILE)
	IncidentsFile string // <StateDir>/incidents.jsonl (JS INCIDENTS)
}

// Resolve computes Paths from the environment variables HOME, ORCH_HOME and ORCH_REGISTRY.
// HOME is used as given, falling back to the OS user home if absent or empty, like
// os.homedir(); ORCH_HOME and ORCH_REGISTRY are made absolute, like the JS path.resolve.
func Resolve(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			// On Unix UserHomeDir only consults HOME; Node also consults the user database.
			current, lookupErr := user.Current()
			if lookupErr != nil {
				return Paths{}, fmt.Errorf("paths: home directory: %w", lookupErr)
			}
			home = current.HomeDir
		}
	}
	orchHome, err := absOrDefault(getenv("ORCH_HOME"), filepath.Join(home, ".config", "opencode", "orchestrator"))
	if err != nil {
		return Paths{}, fmt.Errorf("paths: ORCH_HOME: %w", err)
	}
	registry, err := absOrDefault(getenv("ORCH_REGISTRY"), filepath.Join(orchHome, "models.json"))
	if err != nil {
		return Paths{}, fmt.Errorf("paths: ORCH_REGISTRY: %w", err)
	}
	stateDir := filepath.Join(home, ".local", "state", "opencode", "orchestrator")
	return Paths{
		Home:          home,
		OrchHome:      orchHome,
		RegistryFile:  registry,
		StateDir:      stateDir,
		UsageFile:     filepath.Join(stateDir, "usage.json"),
		IncidentsFile: filepath.Join(stateDir, "incidents.jsonl"),
	}, nil
}

func absOrDefault(value, fallback string) (string, error) {
	if value == "" {
		return fallback, nil
	}
	return filepath.Abs(value)
}

// ProjectDir is <project>/.orchestrator, the per-project state both runtimes share.
func ProjectDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".orchestrator")
}

// RunsDir is where bridge run records go (lib/run-record.mjs runsDir).
func RunsDir(projectRoot string) string {
	return filepath.Join(ProjectDir(projectRoot), "runs")
}

// MailboxDir holds messages.jsonl and acks.jsonl (lib/mailbox.mjs mailboxDir).
func MailboxDir(projectRoot string) string {
	return filepath.Join(ProjectDir(projectRoot), "mailbox")
}
