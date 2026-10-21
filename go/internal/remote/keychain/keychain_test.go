package keychain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain/keychaintest"
)

const secret = "c2VjcmV0LXRva2VuLWZvci10ZXN0cy1vbmx5"

func TestStorePassesTheValueOnStdinOnly(t *testing.T) {
	fake := keychaintest.Install(t)
	if err := keychain.Store(keychain.MachineToken, secret); err != nil {
		t.Fatal(err)
	}
	if stored, _ := fake.Stored("machine-token"); stored != secret {
		t.Errorf("stored %q, want the value", stored)
	}
	if strings.Contains(fake.ArgvLog(), secret) {
		t.Errorf("the value reached argv:\n%s", fake.ArgvLog())
	}
	want := "add-generic-password -U -s opencode-orchestrator.work-trains -a machine-token -w " + secret + "\n"
	if got := fake.StdinLog(); got != want {
		t.Errorf("stdin = %q, want %q", got, want)
	}
}

func TestStoreRefusesValuesSecurityWouldSplitOrInterpret(t *testing.T) {
	fake := keychaintest.Install(t)
	for _, value := range []string{"", "two words", "tab\there", "line\n", `quo"te`, "apo'strophe", `back\slash`, "bell\a", "caf\u00e9"} {
		var unsafe *keychain.UnsafeValueError
		if err := keychain.Store(keychain.AccessClientSecret, value); !errors.As(err, &unsafe) {
			t.Errorf("Store(%q) = %v, want an UnsafeValueError", value, err)
		}
	}
	if fake.ArgvLog() != "" {
		t.Errorf("security ran for a refused value:\n%s", fake.ArgvLog())
	}
}

func TestReadExistsAndDelete(t *testing.T) {
	fake := keychaintest.Install(t)
	if _, err := keychain.Read(keychain.AccessClientID); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("Read of a missing item = %v, want ErrNotFound", err)
	}
	if ok, err := keychain.Exists(keychain.AccessClientID); ok || err != nil {
		t.Errorf("Exists of a missing item = %v, %v", ok, err)
	}
	fake.Put("access-client-id", "client.access")
	if got, err := keychain.Read(keychain.AccessClientID); got != "client.access" || err != nil {
		t.Errorf("Read = %q, %v", got, err)
	}
	if ok, err := keychain.Exists(keychain.AccessClientID); !ok || err != nil {
		t.Errorf("Exists = %v, %v", ok, err)
	}
	if err := keychain.Delete(keychain.AccessClientID); err != nil {
		t.Fatal(err)
	}
	if err := keychain.Delete(keychain.AccessClientID); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestALockedKeychainIsReportedWithSecuritysDiagnostic(t *testing.T) {
	fake := keychaintest.Install(t)
	fake.Lock()
	err := keychain.Store(keychain.MachineToken, secret)
	var failure *keychain.CommandError
	if !errors.Is(err, keychain.ErrKeychainLocked) || !errors.As(err, &failure) {
		t.Fatalf("Store = %v, want ErrKeychainLocked in a CommandError", err)
	}
	if failure.Status != 36 || !strings.Contains(failure.Stderr, "User interaction is not allowed") {
		t.Errorf("CommandError = %+v", failure)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error carries the value: %v", err)
	}
}

func TestAMissingSecurityToolIsACommandError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var failure *keychain.CommandError
	if _, err := keychain.Exists(keychain.MachineToken); !errors.As(err, &failure) || failure.Status != -1 {
		t.Errorf("Exists without security = %v, want a CommandError with status -1", err)
	}
}
