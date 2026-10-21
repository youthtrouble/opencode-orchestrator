package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/policy"
)

// Policy implements `orch policy show|set key=value...|unattended [--cwd DIR]`, byte-identical
// to bin/policy.mjs. The command is the first token, even "--cwd"; --cwd is its first
// occurrence anywhere and takes the next token, whatever it is.
func Policy(args []string, stdio Stdio) int {
	args = decodeArgv(args)
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	root, err := policyRoot(args)
	if err != nil {
		return crash(stdio, err)
	}
	file := policy.File(root)
	switch command {
	case "show", "":
		return printEffectivePolicy(file, stdio)
	case "set", "unattended":
		return setPolicy(file, command, args, stdio)
	}
	fmt.Fprintln(stdio.Err, "commands: show | set key=value | unattended")
	return 2
}

// registryAutonomy is `loadRegistry().autonomy` exactly as the JS evaluates it, unlike
// registry.Load, which other commands rely on to refuse a registry that is not an object: Node's
// fs error for an unreadable file, a SyntaxError for unparsable text, a TypeError for a registry
// that is null, and nil (undefined) for any other value without an autonomy property.
func registryAutonomy() (any, error) {
	p, err := paths.Resolve(os.Getenv)
	if err != nil {
		return nil, err
	}
	doc, err := parseJSONFile(p.RegistryFile)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, &jsTypeError{message: "Cannot read properties of null (reading 'autonomy')"}
	}
	return jsjson.Path(doc, "autonomy"), nil
}

// policyRoot is `path.resolve(ci >= 0 ? argv[ci + 1] : process.cwd())`, evaluated before the
// command is looked at: a trailing --cwd fails every command.
func policyRoot(args []string) (string, error) {
	i := slices.Index(args, "--cwd")
	switch {
	case i < 0:
		return nodefs.Resolve(".")
	case i+1 == len(args):
		return "", errPathArgUndefined
	}
	return nodefs.Resolve(args[i+1])
}

// setPolicy applies the pairs to the override file and prints the new effective policy.
// `set` takes every token after the command that contains "=", a --cwd value included.
func setPolicy(file, command string, args []string, stdio Stdio) int {
	pairs := policy.UnattendedPairs
	if command == "set" {
		pairs = slices.DeleteFunc(slices.Clone(args[1:]), func(arg string) bool { return !strings.Contains(arg, "=") })
	}
	doc, err := policy.Set(policy.Read(file), pairs)
	var refused *policy.RefusedError
	var invalid *policy.InvalidValueError
	switch {
	case errors.As(err, &refused), errors.As(err, &invalid), errors.Is(err, policy.ErrForcePush):
		fmt.Fprintln(stdio.Err, err)
		return 2
	case err != nil:
		return crash(stdio, err)
	}
	data, err := jsjson.MarshalIndent(doc, "  ")
	if err != nil {
		return crash(stdio, err)
	}
	if err := nodefs.MkdirAll(filepath.Dir(file)); err != nil {
		return crash(stdio, err)
	}
	if err := nodefs.WriteFile(file, append(data, '\n')); err != nil {
		return crash(stdio, err)
	}
	return printEffectivePolicy(file, stdio)
}

// printEffectivePolicy reads the registry at each call, as effective() does, so `set` has
// written its file before a registry failure ends the run.
func printEffectivePolicy(file string, stdio Stdio) int {
	autonomy, err := registryAutonomy()
	if err != nil {
		return crash(stdio, err)
	}
	data, err := jsjson.MarshalIndent(policy.Effective(autonomy, file), "  ")
	if err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "%s\n", data)
	return 0
}
