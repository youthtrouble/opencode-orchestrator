// Package paths names the files and directories orch shares with the JS runtime, so both
// read and write the same state.
package paths

import (
	"fmt"
	"os/user"
	"path/filepath"
)

// Paths are the per-user locations, resolved once from the environment.
type Paths struct {
	Home string
	// OrchHome is the JS ROOT: the orchestrator tree holding models.json and bin/. The JS
	// derives it from its own script location; orch reads ORCH_HOME, defaulting to the
	// installed tree.
	OrchHome     string
	RegistryFile string // ORCH_REGISTRY, default <OrchHome>/models.json (JS REGISTRY_FILE)
	StateDir     string // ~/.local/state/opencode/orchestrator (JS STATE_DIR)
	UsageFile    string // <StateDir>/usage.json (JS USAGE_FILE)
}

// Resolve computes Paths from the environment variables HOME, ORCH_HOME and ORCH_REGISTRY.
// HOME is used as given, falling back to the user database's home if absent or empty;
// ORCH_HOME and ORCH_REGISTRY are made absolute, like the JS path.resolve. Unlike
// os.homedir(), an empty HOME counts as absent here; where that matters (the incident log,
// which must never land in a home the caller emptied HOME to avoid), use NodeHome.
func Resolve(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = userHome(); err != nil {
			return Paths{}, err
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
	stateDir := StateDirIn(home)
	return Paths{
		Home:         home,
		OrchHome:     orchHome,
		RegistryFile: registry,
		StateDir:     stateDir,
		UsageFile:    filepath.Join(stateDir, "usage.json"),
	}, nil
}

// NodeHome is os.homedir(): HOME exactly as set, even when it is empty; only an unset HOME
// falls back to the user database's home.
func NodeHome(lookupEnv func(string) (string, bool)) (string, error) {
	if home, set := lookupEnv("HOME"); set {
		return home, nil
	}
	return userHome()
}

// userHome is the current user's home from the user database, as Node reads it when HOME is
// unset (os.UserHomeDir only consults HOME on Unix).
func userHome() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("paths: home directory: %w", err)
	}
	return current.HomeDir, nil
}

// StateDirIn is the orchestrator state directory under home (JS STATE_DIR); an empty home
// gives the relative path Node's path.join gives.
func StateDirIn(home string) string {
	return filepath.Join(home, ".local", "state", "opencode", "orchestrator")
}

// IncidentsFileIn is the incident log under home (JS INCIDENTS). Pass the home NodeHome
// returns: for an explicitly empty HOME the log is relative to the working directory, as in
// the JS, never the user's own log.
func IncidentsFileIn(home string) string {
	return filepath.Join(StateDirIn(home), "incidents.jsonl")
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
