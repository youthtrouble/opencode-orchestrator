//go:build !linux && !darwin

package procs

// waitExited cannot wait for an exit without reaping on this platform; Wait then reaps first
// and kills the leftovers of a cancelled group after.
func waitExited(int) error {
	return errNoExitWatch
}
