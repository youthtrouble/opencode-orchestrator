package cliargs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// ParseArgs mirrors parseArgs in lib/run-record.mjs, used by the bridges, worker, supervisor,
// flip, kickoff and watch: `--k` in flagKeys is a flag; `--k v` with k in valueKeys takes the
// next token whatever it looks like (even "--x"); any other `--k` is a flag and consumes
// nothing; tokens without a "--" prefix are positional.
func ParseArgs(argv []string, valueKeys, flagKeys []string) (Opts, []string) {
	opts := Opts{}
	positional := []string{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		key, isOption := strings.CutPrefix(arg, "--")
		switch {
		case !isOption:
			positional = append(positional, arg)
		case slices.Contains(flagKeys, key):
			opts[key] = flagOpt
		case slices.Contains(valueKeys, key) && i+1 < len(argv):
			opts[key] = valueOpt(argv[i+1])
			i++
		default:
			opts[key] = flagOpt
		}
	}
	return opts, positional
}

// ErrNoPrompt is readPrompt's error when no prompt source is available; the bridges print
// its message and exit 2.
var ErrNoPrompt = errors.New("no prompt: pass --prompt, --prompt-file, positional text, or stdin")

// ErrPathArgBoolean is the message of the TypeError [ERR_INVALID_ARG_TYPE] Node throws when a
// bare value key (the JS value true) reaches path.resolve.
var ErrPathArgBoolean = errors.New(`The "paths[0]" argument must be of type string. Received type boolean (true)`)

// ErrPromptFileWithoutValue is a bare --prompt-file reaching path.resolve(true); the bridges
// print its message and exit 2, as the JS does.
var ErrPromptFileWithoutValue = ErrPathArgBoolean

// ErrPromptWithoutValue reports a bare --prompt, which the bridges refuse (exit 2) where the JS
// would run the model on the prompt "true" (a documented parity exception, PE-8).
var ErrPromptWithoutValue = errors.New("no prompt: --prompt needs a value")

// ReadPrompt mirrors readPrompt in lib/run-record.mjs. Sources in order: --prompt-file (read,
// resolved from the working directory; a read failure has Node's fs message), --prompt, positional tokens joined by spaces, then
// stdin when it is not a terminal (an empty pipe or /dev/null yields ""), else ErrNoPrompt.
func ReadPrompt(opts Opts, positional []string, stdin *os.File) (string, error) {
	if opts.Truthy("prompt-file") {
		file, ok := opts.Value("prompt-file")
		if !ok {
			return "", ErrPromptFileWithoutValue
		}
		return readPromptFile(file)
	}
	if opts.Truthy("prompt") {
		prompt, ok := opts.Value("prompt")
		if !ok {
			return "", ErrPromptWithoutValue
		}
		return prompt, nil
	}
	if len(positional) > 0 {
		return strings.Join(positional, " "), nil
	}
	if !IsTerminal(stdin) {
		prompt, err := ReadStdinText(stdin)
		if err != nil {
			return "", fmt.Errorf("read prompt from stdin: %w", err)
		}
		return prompt, nil
	}
	return "", ErrNoPrompt
}

// readPromptFile is fs.readFileSync(path.resolve(file), "utf8"), failing with Node's message:
// "ENOENT: no such file or directory, open '<abs>'", or "EISDIR: illegal operation on a
// directory, read" for a directory.
func readPromptFile(file string) (string, error) {
	abs, err := nodefs.Resolve(file)
	if err != nil {
		return "", err
	}
	data, err := nodefs.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return jsstr.DecodeUTF8(data), nil
}

// ReadStdinText reads all of stdin as Node's readFileSync(0, "utf8") does: malformed UTF-8
// becomes U+FFFD (see jsstr.DecodeUTF8) and a BOM is kept.
func ReadStdinText(stdin io.Reader) (string, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	return jsstr.DecodeUTF8(data), nil
}
