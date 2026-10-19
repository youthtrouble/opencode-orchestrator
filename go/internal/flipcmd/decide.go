package flipcmd

import (
	"errors"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flip"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// ErrActiveRequired is decide's refusal of an absent or empty --active (exit 2).
var ErrActiveRequired = errors.New("--active opencode|claude required")

// RoutingSource is getRouting({}): the current routing, which may collect usage and write
// the usage cache when the cache is missing or stale.
type RoutingSource func() (*usage.Routing, error)

// Decide says whether the conductor at --active should flip, hand back, stay or pause, from
// the routing and the time of the last flip. The routing is read first and its errors are
// returned as they are; the clock is read after it, as the JS reads Date.now().
func (p Project) Decide(opts cliargs.Opts, routing RoutingSource) (flip.Decision, error) {
	if !opts.Truthy("active") {
		return flip.Decision{}, ErrActiveRequired
	}
	r, err := routing()
	if err != nil {
		return flip.Decision{}, err
	}
	lastFlipAt := lastFlipAt(p.ReadFlip())
	return flip.Decide(flip.Input{
		Active:     jsjson.TemplateString(opts.JS("active")),
		Postures:   r.Resolved.Postures,
		Registry:   r.Registry,
		Now:        p.Now(),
		LastFlipAt: lastFlipAt,
	}), nil
}

// lastFlipAt is `prev?.at ? Date.parse(prev.at) : null`. Only an object's own at counts: a
// string or array prev has a function at whose text never parses, and other values have none.
// A truthy at is parsed as Date.parse(String(at)) in the ECMAScript date formats only (V8's
// legacy forms are no previous flip, PE-15); unparsable is nil, as NaN never cools down.
func lastFlipAt(prev any) *time.Time {
	at := jsjson.Path(prev, "at")
	if !jsjson.Truthy(at) {
		return nil
	}
	ms, ok := jsjson.ParseDate(jsjson.TemplateString(at))
	if !ok {
		return nil
	}
	t := time.UnixMilli(ms)
	return &t
}
