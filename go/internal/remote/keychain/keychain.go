// Package keychain keeps the Work Trains secrets in the macOS login Keychain through the
// security(1) tool, which is looked up on PATH (tests put a fake first).
//
// A secret never appears in argv, where ps(1) would show it: Store runs `security -i` and
// writes its add-generic-password command line to security's stdin. Error messages carry
// security's stderr but never the value.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Service is the generic-password service every Work Trains item is stored under.
const Service = "opencode-orchestrator.work-trains"

// Account names one Work Trains secret.
type Account string

// The Work Trains secrets.
const (
	MachineToken       Account = "machine-token"
	AccessClientID     Account = "access-client-id"
	AccessClientSecret Account = "access-client-secret"
)

// Accounts lists every Work Trains secret, in display order.
var Accounts = []Account{MachineToken, AccessClientID, AccessClientSecret}

// timeout bounds each security run. A locked Keychain can make security wait on a GUI prompt;
// 30 s matches the usage reader's Keychain timeout.
const timeout = 30 * time.Second

// security exits with the low byte of the Security framework's OSStatus.
const (
	exitItemNotFound          = 44 // errSecItemNotFound, -25300
	exitInteractionNotAllowed = 36 // errSecInteractionNotAllowed, -25308: the Keychain is locked
)

var (
	// ErrNotFound is an item that is not in the Keychain.
	ErrNotFound = errors.New("keychain item not found")
	// ErrKeychainLocked is a Keychain that refused access without user interaction.
	ErrKeychainLocked = errors.New("the login Keychain is locked; unlock it and retry")
)

// UnsafeValueError is a value Store refuses because it could not be passed on security's
// command line verbatim: security -i splits its input on whitespace and interprets quotes and
// backslashes. Only printable ASCII is accepted.
type UnsafeValueError struct{ Account Account }

func (e *UnsafeValueError) Error() string {
	return fmt.Sprintf("refusing to store %s: the value is empty or holds whitespace, a quote, a backslash or a character outside printable ASCII", e.Account)
}

// CommandError is a security run that failed for a reason other than ErrNotFound.
type CommandError struct {
	Command string // the security subcommand
	Account Account
	Status  int    // the exit status, or -1 when security did not exit normally
	Stderr  string // security's diagnostic, trimmed; never holds the stored value
	Err     error  // ErrKeychainLocked, or the start/wait failure
}

func (e *CommandError) Error() string {
	if errors.Is(e.Err, ErrKeychainLocked) {
		return fmt.Sprintf("security %s (%s): %v (status %d: %s)", e.Command, e.Account, ErrKeychainLocked, e.Status, e.Stderr)
	}
	detail := e.Stderr
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	return fmt.Sprintf("security %s (%s) failed with status %d: %s", e.Command, e.Account, e.Status, detail)
}

func (e *CommandError) Unwrap() error { return e.Err }

// Read returns the secret stored for account.
func Read(account Account) (string, error) {
	stdout, err := run("find-generic-password", account, nil, "find-generic-password", "-s", Service, "-a", string(account), "-w")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(stdout, "\n"), nil
}

// Exists reports whether account has an item, without reading the secret.
func Exists(account Account) (bool, error) {
	_, err := run("find-generic-password", account, nil, "find-generic-password", "-s", Service, "-a", string(account))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	}
	return false, err
}

// Validate reports whether Store would accept value for account: nil, or an UnsafeValueError.
// A caller storing several items validates them all first, so a refusal changes none.
func Validate(account Account, value string) error {
	if !safeValue(value) {
		return &UnsafeValueError{Account: account}
	}
	return nil
}

// Store creates or replaces the item for account with value.
func Store(account Account, value string) error {
	if err := Validate(account, value); err != nil {
		return err
	}
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", Service, account, value)
	if _, err := run("add-generic-password", account, []byte(command), "-i"); err != nil {
		return redact(err, value)
	}
	// security -i reads commands until EOF and does not document that a failed command changes
	// its exit status, so reading the item back is what proves the write.
	stored, err := Read(account)
	switch {
	case errors.Is(err, ErrNotFound), err == nil && stored != value:
		return &CommandError{Command: "add-generic-password", Account: account, Status: 0, Stderr: "security -i did not store the new value"}
	case err != nil:
		return err
	}
	return nil
}

// Delete removes the item for account; ErrNotFound when there is none.
func Delete(account Account) error {
	_, err := run("delete-generic-password", account, nil, "delete-generic-password", "-s", Service, "-a", string(account))
	return err
}

// safeValue accepts printable ASCII without quotes or backslashes. Tokens are base64url or
// opaque ASCII; anything else security -w might print back hex-encoded. Whether security -i
// gives other characters (a leading '-', '#', ';') a meaning is undocumented; the read-back in
// Store is the safety net, failing loudly rather than keeping a mis-parsed value.
func safeValue(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c <= ' ' || c > '~' || c == '"' || c == '\'' || c == '\\' {
			return false
		}
	}
	return true
}

// run executes security with args, feeding stdin when given, and maps its exit status to
// ErrNotFound, ErrKeychainLocked or a CommandError. stdout is returned only on success.
func run(command string, account Account, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", args...)
	cmd.WaitDelay = time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	failure := &CommandError{Command: command, Account: account, Status: -1, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.Exited() {
		failure.Status = exitErr.ExitCode()
		switch failure.Status {
		case exitItemNotFound:
			return "", ErrNotFound
		case exitInteractionNotAllowed:
			failure.Err = ErrKeychainLocked
		}
	}
	return "", failure
}

// redact removes value from a failed Store's diagnostic, in case security echoed its input.
func redact(err error, value string) error {
	var failure *CommandError
	if errors.As(err, &failure) {
		failure.Stderr = strings.ReplaceAll(failure.Stderr, value, "[redacted]")
	}
	return err
}
