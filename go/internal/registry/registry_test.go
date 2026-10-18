package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real registry from this checkout, so the test fails if models.json stops parsing.
const checkoutRegistry = "../../../orchestrator/models.json"

func TestLoadKeepsFileKeyOrder(t *testing.T) {
	doc, err := Load(checkoutRegistry)
	if err != nil {
		t.Fatal(err)
	}
	keys := doc.Keys()
	if len(keys) < 3 || keys[0] != "$comment" || keys[1] != "last_verified" || keys[2] != "vendors" {
		t.Errorf("leading keys = %v, want [$comment last_verified vendors ...]", keys)
	}
	for _, required := range []string{"roles", "thresholds", "conductor"} {
		if _, ok := doc.Get(required); !ok {
			t.Errorf("registry has no %q key", required)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}

	var notObject *NotObjectError
	if _, err := Load(write("array.json", "[]")); !errors.As(err, &notObject) {
		t.Errorf("array registry: error = %v, want *NotObjectError", err)
	}
	if _, err := Load(write("broken.json", `{"roles":`)); err == nil || !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("broken registry: error = %v, want a parse error naming the file", err)
	}
	if _, err := Load(filepath.Join(dir, "absent.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing registry: error = %v, want not-exist", err)
	}
}
