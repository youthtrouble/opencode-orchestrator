package cliargs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
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

// ErrPromptFileWithoutValue carries the message Node prints when a bare --prompt-file reaches
// path.resolve(true); the bridges print it and exit 2, as the JS does.
var ErrPromptFileWithoutValue = errors.New(`The "paths[0]" argument must be of type string. Received type boolean (true)`)

// ErrPromptWithoutValue reports a bare --prompt. Outstanding parity deviation: the JS returns
// boolean true and crashes at spawn or stdin. Reconcile before releasing a consuming command.
var ErrPromptWithoutValue = errors.New("no prompt: --prompt needs a value")

// ReadPrompt mirrors readPrompt in lib/run-record.mjs. Sources in order: --prompt-file (read,
// resolved from the working directory), --prompt, positional tokens joined by spaces, then
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
	if !isTerminal(stdin) {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read prompt from stdin: %w", err)
		}
		return decodePromptUTF8(data), nil
	}
	return "", ErrNoPrompt
}

func readPromptFile(file string) (string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", fmt.Errorf("resolve prompt file: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return decodePromptUTF8(data), nil
}

// decodePromptUTF8 matches Node's Buffer.toString("utf8"): one replacement per malformed
// sequence, including a valid but incomplete prefix, not one per byte or invalid byte run.
func decodePromptUTF8(data []byte) string {
	var text strings.Builder
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			// Consume the incomplete prefix, but leave the byte that makes it invalid
			// for the next iteration (it may start a valid character).
			for size < len(data) && !utf8.FullRune(data[:size+1]) {
				size++
			}
		}
		text.WriteRune(r)
		data = data[size:]
	}
	return text.String()
}
