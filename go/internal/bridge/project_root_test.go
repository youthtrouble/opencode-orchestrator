package bridge

import "testing"

func TestBridgeProjectRootDoesNotChangeOrdinaryFileResolution(t *testing.T) {
	const nested = "/project/.orchestrator/briefs"
	root, err := resolveProjectRoot(nested)
	if err != nil || root != "/project" {
		t.Fatalf("bridge cwd: %q, %v", root, err)
	}
	file, err := resolvePath(nested)
	if err != nil || file != nested {
		t.Fatalf("ordinary file: %q, %v", file, err)
	}
	if _, err := resolveProjectRoot(true); err != errPathArgBoolean {
		t.Fatalf("bare --cwd: %v; want %v", err, errPathArgBoolean)
	}
}
