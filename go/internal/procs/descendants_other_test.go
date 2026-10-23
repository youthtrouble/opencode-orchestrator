//go:build !linux && !darwin

package procs

import (
	"errors"
	"testing"
)

func TestUnsupportedDescendantProbe(t *testing.T) {
	if _, err := LiveDescendants(1); !errors.Is(err, ErrProbeUnsupported) {
		t.Errorf("probe %v", err)
	}
}
