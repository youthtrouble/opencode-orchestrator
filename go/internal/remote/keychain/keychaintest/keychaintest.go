// Package keychaintest puts a fake security(1) first on PATH, so tests exercise the keychain
// package and the commands built on it without touching the real login Keychain. The fake
// keeps each item in a file, and logs every argv and every stdin it receives.
package keychaintest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// script emulates the security subcommands keychain uses, including `-i` (commands on stdin),
// with security's exit statuses: 44 for a missing item, 36 for a locked Keychain.
const script = `#!/bin/sh
set -f
dir=%DIR%
printf '%s\n' "$*" >> "$dir/argv.log"
if [ -e "$dir/locked" ]; then
  echo 'security: SecKeychainSearchCopyNext: User interaction is not allowed.' >&2
  exit 36
fi
if [ "$1" = "-i" ]; then
  input=$(cat)
  printf '%s\n' "$input" >> "$dir/stdin.log"
  set -- $input
fi
command=$1
shift
account=
value=
print_value=
while [ $# -gt 0 ]; do
  case $1 in
    -s) [ "$2" = opencode-orchestrator.work-trains ] || exit 9; shift 2 ;;
    -a) account=$2; shift 2 ;;
    -w) if [ "$command" = add-generic-password ]; then value=$2; shift 2; else print_value=1; shift; fi ;;
    *) shift ;;
  esac
done
[ -n "$account" ] || exit 9
item="$dir/store/$account"
not_found() {
  echo 'security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.' >&2
  exit 44
}
case $command in
  add-generic-password)
    if [ -e "$dir/refuse-$account" ]; then
      echo 'security: SecKeychainItemCreateFromContent: write refused.' >&2
      exit 1
    fi
    printf '%s' "$value" > "$item" ;;
  find-generic-password)
    [ -e "$item" ] || not_found
    if [ -n "$print_value" ]; then cat "$item"; echo; else echo 'keychain: "login.keychain-db"'; fi ;;
  delete-generic-password)
    [ -e "$item" ] || not_found
    rm "$item"
    echo 'password has been deleted.' ;;
  *) exit 9 ;;
esac
`

// Fake is an installed fake security.
type Fake struct {
	t   *testing.T
	dir string
}

// Install writes the fake and sets PATH to its directory plus /bin, so the real
// /usr/bin/security is out of reach for the rest of the test.
func Install(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{t: t, dir: t.TempDir()}
	bin := filepath.Join(f.dir, "bin")
	for _, dir := range []string{bin, filepath.Join(f.dir, "store")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body := strings.ReplaceAll(script, "%DIR%", "'"+f.dir+"'")
	if err := os.WriteFile(filepath.Join(bin, "security"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin")
	return f
}

// Put stores value for account as if security had.
func (f *Fake) Put(account, value string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "store", account), []byte(value), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// Stored returns the value held for account, and whether there is one.
func (f *Fake) Stored(account string) (string, bool) {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "store", account))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data), true
}

// Lock makes every later security run fail as a locked Keychain does.
func (f *Fake) Lock() {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "locked"), nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// RefuseStore makes every later add of account fail, as a Keychain write error would.
func (f *Fake) RefuseStore(account string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "refuse-"+account), nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// ArgvLog is every argv the fake received, one run per line.
func (f *Fake) ArgvLog() string { return f.readLog("argv.log") }

// StdinLog is everything the fake read on stdin in `-i` mode.
func (f *Fake) StdinLog() string { return f.readLog("stdin.log") }

func (f *Fake) readLog(name string) string {
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	return string(data)
}
