package cliargs

import (
	"path/filepath"
	"strings"
)

// MailboxArgs is what the argv loop of bin/mailbox.mjs produces.
type MailboxArgs struct {
	Command string            // first token: post | list | ack ("" when argv is empty)
	Values  map[string]string // JS `opts`, without ref
	Flags   map[string]bool   // JS `flags` Set: keys given without a value
	Refs    []string          // every --ref value, in order
}

// ParseMailbox mirrors bin/mailbox.mjs: a key followed by nothing or by a "--" token is a
// flag (kept apart from the values, so `--json` and `--json x` can coexist); `--ref` repeats;
// positional tokens are ignored.
func ParseMailbox(argv []string) MailboxArgs {
	command, rest := splitCommand(argv)
	args := MailboxArgs{Command: command, Values: map[string]string{}, Flags: map[string]bool{}, Refs: []string{}}
	scanKeyedOptions(rest, func(key string) { args.Flags[key] = true }, func(key, value string) {
		if key == "ref" {
			args.Refs = append(args.Refs, value)
		} else {
			args.Values[key] = value
		}
	})
	return args
}

// ProjectRoot mirrors `path.resolve(opts.root || process.env.ORCHESTRATOR_PROJECT || process.cwd())`.
func (a MailboxArgs) ProjectRoot(getenv func(string) string) (string, error) {
	root := a.Values["root"]
	if root == "" {
		root = getenv("ORCHESTRATOR_PROJECT")
	}
	// filepath.Abs("") is the working directory, which is the last fallback.
	return filepath.Abs(root)
}

// IncidentArgs is what the argv loop of bin/incident.mjs produces.
type IncidentArgs struct {
	Command string   // first token: record | list | report | share
	Opts    Opts     // flags and values share one namespace, as in the JS
	Ctx     []string // every --ctx key=value, in order
}

// ParseIncident mirrors bin/incident.mjs: the same flag rule as the mailbox, but a flag is
// stored in Opts as true; `--ctx` repeats. Outstanding parity deviation: a bare `--ctx` is
// recorded as Opts["ctx"] while values keep collecting; JS replaces its array with true and
// then throws. Reconcile before releasing the incident command.
func ParseIncident(argv []string) IncidentArgs {
	command, rest := splitCommand(argv)
	args := IncidentArgs{Command: command, Opts: Opts{}, Ctx: []string{}}
	scanKeyedOptions(rest, func(key string) { args.Opts[key] = flagOpt }, func(key, value string) {
		if key == "ctx" {
			args.Ctx = append(args.Ctx, value)
		} else {
			args.Opts[key] = valueOpt(value)
		}
	})
	return args
}

// scanKeyedOptions is the loop bin/mailbox.mjs and bin/incident.mjs share: `--k` takes the next
// token as its value unless there is none or it starts with "--"; other tokens are skipped.
func scanKeyedOptions(argv []string, onFlag func(key string), onValue func(key, value string)) {
	for i := 0; i < len(argv); i++ {
		key, isOption := strings.CutPrefix(argv[i], "--")
		if !isOption {
			continue
		}
		if i+1 == len(argv) || strings.HasPrefix(argv[i+1], "--") {
			onFlag(key)
			continue
		}
		onValue(key, argv[i+1])
		i++
	}
}

// ManifestArgs is what the argv loop of bin/manifest.mjs produces.
type ManifestArgs struct {
	Command    string // create | verify | diff
	Values     map[string]string
	Positional []string
}

// ParseManifest mirrors bin/manifest.mjs: every `--k` consumes the next token, even one that
// starts with "--". A trailing `--k` removes any earlier value, matching the JS assignment
// of undefined (readers only test truthiness).
func ParseManifest(argv []string) ManifestArgs {
	command, rest := splitCommand(argv)
	args := ManifestArgs{Command: command, Values: map[string]string{}, Positional: []string{}}
	for i := 0; i < len(rest); i++ {
		key, isOption := strings.CutPrefix(rest[i], "--")
		if !isOption {
			args.Positional = append(args.Positional, rest[i])
			continue
		}
		if i+1 < len(rest) {
			args.Values[key] = rest[i+1]
		} else {
			delete(args.Values, key)
		}
		i++
	}
	return args
}

// UsageArgs is what bin/usage.mjs reads from argv: exact-token membership, position-free.
type UsageArgs struct {
	Refresh bool
	JSON    bool
}

// ParseUsage mirrors `new Set(process.argv.slice(2))` in bin/usage.mjs. Its `--brief` is
// accepted and unused in the JS, so it is not modelled.
func ParseUsage(argv []string) UsageArgs {
	var args UsageArgs
	for _, arg := range argv {
		switch arg {
		case "--refresh":
			args.Refresh = true
		case "--json":
			args.JSON = true
		}
	}
	return args
}

// KickoffArgs is what bin/kickoff.mjs reads from argv.
type KickoffArgs struct {
	Briefs []string
	Opts   Opts // door, cwd (values); headless, dry-run, help (flags)
}

// ParseKickoff mirrors bin/kickoff.mjs: every `--brief X` with a non-empty X is collected first;
// then each "--brief" token and the token after it are dropped (even when that token was empty
// and so not collected) and the remainder goes through ParseArgs.
func ParseKickoff(argv []string) KickoffArgs {
	var briefs []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--brief" && i+1 < len(argv) && argv[i+1] != "" {
			i++
			briefs = append(briefs, argv[i])
		}
	}
	var remaining []string
	for i, arg := range argv {
		if arg == "--brief" || (i > 0 && argv[i-1] == "--brief") {
			continue
		}
		remaining = append(remaining, arg)
	}
	opts, _ := ParseArgs(remaining, []string{"door", "cwd"}, []string{"headless", "dry-run", "help"})
	return KickoffArgs{Briefs: briefs, Opts: opts}
}

// splitCommand mirrors `const [cmd, ...rest] = process.argv.slice(2)`.
func splitCommand(argv []string) (string, []string) {
	if len(argv) == 0 {
		return "", nil
	}
	return argv[0], argv[1:]
}
