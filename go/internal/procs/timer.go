package procs

import (
	"math"
	"time"
)

// maxTimerMs is Node's TIMEOUT_MAX.
const maxTimerMs = math.MaxInt32

// TimerDelay is the delay Node's setTimeout gives ms milliseconds: 1 ms when ms is NaN, below
// 1 or above TIMEOUT_MAX. Node also prints a warning for the clamp; orch does not (PE-9).
func TimerDelay(ms float64) time.Duration {
	if !(ms >= 1 && ms <= maxTimerMs) {
		ms = 1
	}
	return time.Duration(ms * float64(time.Millisecond))
}
