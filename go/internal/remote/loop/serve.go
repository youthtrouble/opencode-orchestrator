package loop

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	// recheckEvery is how often Serve looks at local state between pulls.
	recheckEvery = 15 * time.Second
	// The backoff after a failed cycle: equal jitter over a ceiling that starts at
	// firstBackoff and doubles per consecutive failure up to maxBackoff.
	firstBackoff = 5 * time.Second
	maxBackoff   = 5 * time.Minute
)

// Serve runs cycles until ctx is done, then returns. After a good cycle it waits the poll
// interval, pushing what becomes due every 15 s meanwhile; after a failed one it backs off.
// A cancelled ctx cuts any HTTP call short, and the cycle's state is still saved.
func (l *Loop) Serve(ctx context.Context) {
	l.logf("serve started")
	failures := 0
	for ctx.Err() == nil {
		report, err := l.Cycle(ctx)
		switch {
		case ctx.Err() != nil:
			// stopping: the loop condition ends it
		case err != nil:
			failures++
			delay := l.backoff(failures)
			l.logf("cycle failed (%s): %s; retrying in %s", Kind(err), l.Redact(err.Error()), delay.Round(time.Second))
			l.Sleep(ctx, delay) // an early return is ctx ending, which the loop checks
		default:
			if failures > 0 {
				l.logf("recovered after %d failed cycle(s)", failures)
				failures = 0
			}
			l.recheckUntil(ctx, l.Now().Add(report.PollAfter))
		}
	}
	l.logf("serve stopped")
}

// recheckUntil waits until deadline, pushing what is due every recheckEvery. It returns early
// when ctx ends or a push fails, so the next full cycle retries it (and backs off if it fails
// again).
func (l *Loop) recheckUntil(ctx context.Context, deadline time.Time) {
	for {
		remaining := deadline.Sub(l.Now())
		if remaining <= 0 {
			return
		}
		if l.Sleep(ctx, min(recheckEvery, remaining)) != nil {
			return
		}
		if !l.Now().Before(deadline) {
			return // the next cycle pushes anyway
		}
		if _, err := l.PushDue(ctx); err != nil {
			if ctx.Err() == nil {
				l.logf("push failed (%s): %s", Kind(err), l.Redact(err.Error()))
			}
			return
		}
	}
}

// backoff is the wait after the given number of consecutive failed cycles.
func (l *Loop) backoff(failures int) time.Duration {
	ceiling := firstBackoff
	for i := 1; i < failures && ceiling < maxBackoff; i++ {
		ceiling *= 2
	}
	return l.Jitter(min(ceiling, maxBackoff))
}

// SleepContext waits d or until ctx is done: the Sleep of a real Loop.
func SleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EqualJitter is a uniformly random duration in [ceiling/2, ceiling]: the Jitter of a real Loop.
// Full jitter ([0, ceiling]) could retry a failed cycle at once, and twice in a row, hitting the
// Worker and the Keychain back to back; the floor keeps every retry at least half the ceiling
// (2.5 s at first) away, while the random half still spreads retries out.
func EqualJitter(ceiling time.Duration) time.Duration {
	half := ceiling / 2
	return half + time.Duration(rand.Int64N(int64(ceiling-half)+1))
}
