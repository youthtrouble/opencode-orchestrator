package snapshot

import (
	"errors"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
)

// maxBudgetAge is how old the cached usage reading may be and still be pushed. orch remote
// never collects usage itself, so an older reading would show a posture that may have long
// changed; the page shows no budget instead.
const maxBudgetAge = 30 * time.Minute

// Budget maps the cached usage reading to both vendors' postures, without collecting or
// refreshing it. It is nil when there is no cache or it is older than 30 minutes (by its
// generated_at, read with cfg.Now). A registry or cache that cannot be read is an error.
func Budget(cfg usage.Config) (*wire.Budget, error) {
	cached, err := usage.CachedRouting(cfg)
	switch {
	case errors.Is(err, usage.ErrNoCache):
		return nil, nil
	case err != nil:
		return nil, err
	}
	generatedAt, ok := parseTime(jsjson.Path(cached.Usage, "generated_at"))
	if !ok || cfg.Now().Sub(generatedAt) > maxBudgetAge {
		return nil, nil
	}
	postures := cached.Resolved.Postures
	return &wire.Budget{
		Anthropic: postureOf(postures.Anthropic, "session"),
		OpenAI:    postureOf(postures.OpenAI, "five_hour"),
	}, nil
}

// postureOf maps one vendor's resolved posture; fiveHourWindow names the vendor's 5-hour window
// in its usage entry (Claude "session", Codex "five_hour").
func postureOf(posture routing.VendorPosture, fiveHourWindow string) *wire.Posture {
	level := posture.Overall
	if level != routing.Green && level != routing.Amber && level != routing.Red {
		level = routing.Unknown
	}
	resetsAt, _ := parseTime(jsjson.Path(posture.Detail, fiveHourWindow, "resets_at"))
	return &wire.Posture{
		Level:       level,
		FiveHourPct: percentage(jsjson.Path(posture.Detail, fiveHourWindow, "pct")),
		WeeklyPct:   percentage(jsjson.Path(posture.Detail, "weekly", "pct")),
		ResetsAt:    isoOrNull(resetsAt),
	}
}

// percentage is a used percentage the schema accepts (a number in 0..100), else nil.
func percentage(value any) *float64 {
	pct, isNumber := value.(float64)
	if !isNumber || pct < 0 || pct > 100 {
		return nil
	}
	return &pct
}
