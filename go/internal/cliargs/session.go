package cliargs

import (
	"fmt"
	"slices"
	"strings"
)

// SessionOptions are the options one `session` command accepts.
type SessionOptions struct {
	Values []string // options that take the next token
	Flags  []string // options that take none
}

// ParseSession mirrors parseOptions in bin/session.mjs: every token must be a known option,
// and a value option takes the next token, which may not start with "--". A later occurrence
// of a key replaces an earlier one. The error is the usage line's message.
func ParseSession(argv []string, accepted SessionOptions) (Opts, error) {
	opts := Opts{}
	for i := 0; i < len(argv); i++ {
		key, isOption := strings.CutPrefix(argv[i], "--")
		switch {
		case isOption && slices.Contains(accepted.Flags, key):
			opts[key] = flagOpt
		case !isOption || !slices.Contains(accepted.Values, key):
			return nil, fmt.Errorf("unknown argument %s", argv[i])
		case i+1 == len(argv) || strings.HasPrefix(argv[i+1], "--"):
			return nil, fmt.Errorf("--%s needs a value", key)
		default:
			opts[key] = valueOpt(argv[i+1])
			i++
		}
	}
	return opts, nil
}
