package snapshot

import (
	"testing"
	"time"
)

func TestDeriveStateFirstMatchingRuleWins(t *testing.T) {
	now := testNow
	running := lane{status: "running", alive: true, harness: "claude"}
	dead := lane{status: "running", alive: false}
	working := &heartbeat{door: "opencode", state: "working", updatedAt: now.Add(-time.Minute)}
	staleWorking := &heartbeat{door: "opencode", state: "working", updatedAt: now.Add(-16 * time.Minute)}
	slightlyAhead := &heartbeat{door: "opencode", state: "working", updatedAt: now.Add(clockSkewAllowance - time.Second)}
	farAhead := &heartbeat{door: "opencode", state: "working", updatedAt: now.Add(clockSkewAllowance + time.Second)}
	done := &heartbeat{state: "done", updatedAt: now.Add(-3 * time.Hour)}
	gated := &gate{reason: "push approval", question: "q1"}
	held := &gate{reason: "deploy window"}
	pagePaused := &gate{reason: pausedFromPage}

	cases := map[string]struct {
		in       inputs
		blocking int
		state    string
		reason   string
	}{
		"blocking question beats a dead lane": {inputs{lane: dead}, 1, stateNeedsYou, "1 blocking question"},
		"several blocking questions":          {inputs{}, 3, stateNeedsYou, "3 blocking questions"},
		"gate with a question":                {inputs{gate: gated, lane: dead}, 0, stateNeedsYou, "awaiting your answer: push approval"},
		"gate with a question and no reason":  {inputs{gate: &gate{question: "q1"}}, 0, stateNeedsYou, "awaiting your answer"},
		"failed lane beats a gate":            {inputs{lane: lane{status: "failed"}, gate: held}, 0, stateFailed, "the headless conductor failed"},
		"running lane with a dead pid":        {inputs{lane: dead, session: working}, 0, stateFailed, "the headless conductor stopped without reporting (its process is gone)"},
		"paused from the page":                {inputs{gate: pagePaused, lane: running}, 0, statePaused, "paused from Work Trains"},
		"other gate without a question":       {inputs{gate: held}, 0, statePaused, "awaiting human: deploy window"},
		"gate without a reason":               {inputs{gate: &gate{}}, 0, statePaused, "awaiting human"},
		"budget pause beats a running lane":   {inputs{budgetPaused: true, lane: running}, 0, statePaused, "both vendors red until a reset"},
		"running lane":                        {inputs{lane: running, session: done}, 0, stateWorking, "the headless conductor is running"},
		"fresh working heartbeat":             {inputs{session: working}, 0, stateWorking, "an interactive session is working"},
		"stale working heartbeat":             {inputs{session: staleWorking}, 0, stateIdle, "nothing is running"},
		"heartbeat ahead within the skew":     {inputs{session: slightlyAhead}, 0, stateWorking, "an interactive session is working"},
		"heartbeat far in the future":         {inputs{session: farAhead}, 0, stateIdle, "nothing is running"},
		"done heartbeat":                      {inputs{session: done}, 0, stateDone, "the last session finished"},
		"finished lane, nothing else":         {inputs{lane: lane{status: "finished"}}, 0, stateIdle, "nothing is running"},
		"starting lane is not yet failed":     {inputs{lane: lane{status: "starting"}}, 0, stateIdle, "nothing is running"},
		"nothing at all":                      {inputs{}, 0, stateIdle, "nothing is running"},
	}
	for name, c := range cases {
		state, reason := deriveState(c.in, c.blocking, now)
		if state != c.state || reason != c.reason {
			t.Errorf("%s: %q (%q), want %q (%q)", name, state, reason, c.state, c.reason)
		}
	}
}

func TestRunOfAndDoorOf(t *testing.T) {
	now := testNow
	started := now.Add(-time.Hour)
	progressed := now.Add(-2 * time.Minute)
	newestMessage := now.Add(-time.Minute)
	cases := map[string]struct {
		in              inputs
		kind            string
		since, activity *string
		door            *string
	}{
		"headless lane": {
			in:   inputs{lane: lane{status: "running", alive: true, harness: "claude", startedAt: started, lastProgressAt: progressed}, flipTo: "opencode", messages: []message{{at: newestMessage}}},
			kind: "headless", since: ptr(iso(started)), activity: ptr(iso(newestMessage)), door: ptr("claude"),
		},
		"codex harness is not a door": {
			in:   inputs{lane: lane{status: "running", alive: true, harness: "codex", startedAt: started}, flipTo: "opencode"},
			kind: "headless", since: ptr(iso(started)), door: ptr("opencode"),
		},
		"dead lane falls back to flip.to": {
			in:   inputs{lane: lane{status: "running", harness: "claude", lastProgressAt: progressed}, flipTo: "opencode"},
			kind: "none", activity: ptr(iso(progressed)), door: ptr("opencode"),
		},
		"live interactive session": {
			in:   inputs{session: &heartbeat{door: "claude", state: "waiting", updatedAt: progressed}},
			kind: "interactive", activity: ptr(iso(progressed)), door: ptr("claude"),
		},
		"ended session is not live": {
			in:   inputs{session: &heartbeat{door: "claude", state: "ended", updatedAt: progressed}},
			kind: "none", activity: ptr(iso(progressed)), door: ptr("claude"),
		},
		"nothing": {in: inputs{flipTo: "bogus"}, kind: "none"},
	}
	for name, c := range cases {
		run := runOf(c.in, now)
		if run.Kind != c.kind || !samePtr(run.Since, c.since) || !samePtr(run.LastActivityAt, c.activity) {
			t.Errorf("%s: run = %s since %s activity %s", name, run.Kind, show(run.Since), show(run.LastActivityAt))
		}
		if door := doorOf(c.in); !samePtr(door, c.door) {
			t.Errorf("%s: door = %s, want %s", name, show(door), show(c.door))
		}
	}
}

func ptr(s string) *string { return &s }

func samePtr(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func show(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}
