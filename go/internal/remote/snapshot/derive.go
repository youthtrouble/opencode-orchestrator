package snapshot

import (
	"fmt"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/session"
)

// liveSessionAge is how recent a heartbeat must be to count as a live interactive session.
const liveSessionAge = 15 * time.Minute

// clockSkewAllowance is how far in the future a heartbeat may be and still count as live.
// Heartbeats are written on this Mac, so the only honest skew is a clock step (an NTP
// correction) between write and read, which is seconds; a heartbeat further ahead is a
// hand edit or a wrong clock and would otherwise read as working until the clock caught up.
const clockSkewAllowance = 2 * time.Minute

// pausedFromPage is the gate reason a Work Trains pause writes (flip gate).
const pausedFromPage = "paused from Work Trains"

// Project states (plan D12).
const (
	stateNeedsYou = "needs_you"
	stateFailed   = "failed"
	statePaused   = "paused"
	stateWorking  = "working"
	stateDone     = "done"
	stateIdle     = "idle"
)

// deriveState is the project state and a short reason for it; the first rule that matches
// wins (plan D12). blocking counts the open blocking questions to the human.
func deriveState(in inputs, blocking int, now time.Time) (state, reason string) {
	switch {
	case blocking == 1:
		return stateNeedsYou, "1 blocking question"
	case blocking > 1:
		return stateNeedsYou, fmt.Sprintf("%d blocking questions", blocking)
	case in.gate != nil && in.gate.question != "":
		return stateNeedsYou, withDetail("awaiting your answer", in.gate.reason)
	case in.lane.status == "failed":
		return stateFailed, "the headless conductor failed"
	case in.lane.status == "running" && !in.lane.alive:
		return stateFailed, "the headless conductor stopped without reporting (its process is gone)"
	case in.gate != nil && in.gate.reason == pausedFromPage:
		return statePaused, pausedFromPage
	case in.gate != nil:
		return statePaused, withDetail("awaiting human", in.gate.reason)
	case in.budgetPaused:
		return statePaused, "both vendors red until a reset"
	case in.lane.headless():
		return stateWorking, "the headless conductor is running"
	case liveSession(in.session, now) && in.session.state == "working":
		return stateWorking, "an interactive session is working"
	case in.session != nil && in.session.state == "done":
		return stateDone, "the last session finished"
	}
	return stateIdle, "nothing is running"
}

func withDetail(summary, detail string) string {
	if detail == "" {
		return summary
	}
	return summary + ": " + detail
}

// liveSession reports a heartbeat younger than liveSessionAge, and not dated beyond
// clockSkewAllowance in the future, whose session has not ended.
func liveSession(beat *heartbeat, now time.Time) bool {
	if beat == nil || beat.state == "ended" {
		return false
	}
	age := now.Sub(beat.updatedAt)
	return age < liveSessionAge && age >= -clockSkewAllowance
}

// runOf is how the project is driven (plan D12). A heartbeat records no session start, so an
// interactive run has no since.
func runOf(in inputs, now time.Time) wire.Run {
	run := wire.Run{Kind: "none", LastActivityAt: isoOrNull(lastActivity(in))}
	switch {
	case in.lane.headless():
		run.Kind, run.Since = "headless", isoOrNull(in.lane.startedAt)
	case liveSession(in.session, now):
		run.Kind = "interactive"
	}
	return run
}

// lastActivity is the latest of the lane's last progress, the newest heartbeat and the newest
// mailbox message; zero when there is none.
func lastActivity(in inputs) time.Time {
	latest := in.lane.lastProgressAt
	if in.session != nil && in.session.updatedAt.After(latest) {
		latest = in.session.updatedAt
	}
	for _, msg := range in.messages {
		if msg.at.After(latest) {
			latest = msg.at
		}
	}
	return latest
}

// doorOf is the door driving the project: the running lane's harness, else the door flip.json
// last flipped to, else the newest heartbeat's door. A harness that is not a door (codex)
// does not count.
func doorOf(in inputs) *string {
	candidates := []string{in.flipTo}
	if in.lane.headless() {
		candidates = []string{in.lane.harness, in.flipTo}
	}
	if in.session != nil {
		candidates = append(candidates, in.session.door)
	}
	for _, door := range candidates {
		if session.IsDoor(door) {
			return &door
		}
	}
	return nil
}
