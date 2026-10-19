package procs

import "strings"

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
