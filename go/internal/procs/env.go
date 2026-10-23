package procs

import (
	"path/filepath"
	"strings"
)

// WithPWD returns env with exactly one PWD naming the child's directory, keeping every
// other entry in order without mutating env. An empty dir leaves env unchanged. Relative
// directories are resolved against the physical working directory, never the parent's PWD.
func WithPWD(env []string, dir string) ([]string, error) {
	if dir == "" {
		return env, nil
	}
	abs, err := absoluteIn(dir, "")
	if err != nil {
		return nil, err
	}
	return append(WithoutEnv(env, "PWD"), "PWD="+filepath.Clean(abs)), nil
}

// WithoutEnv returns env without any entry for key, keeping every other entry (duplicates
// included) in order. It is the counterpart of {...process.env, KEY: undefined}, which Node
// turns into an absent variable. Keys match exactly and case-sensitively.
func WithoutEnv(env []string, key string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			kept = append(kept, entry)
		}
	}
	return kept
}
