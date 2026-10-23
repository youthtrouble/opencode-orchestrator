//go:build !linux && !darwin

package procs

// LiveDescendants is unsupported; callers must treat the error as unknown evidence.
func LiveDescendants(pid int) ([]int, error) { return nil, ErrProbeUnsupported }
