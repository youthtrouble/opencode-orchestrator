package snapshot

import (
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
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
// generated_at, read with cfg.Now). A registry that cannot be read is an error.
//
// It reads two files and runs nothing: the postures need only the reading and the registry's
// thresholds, so it skips usage.CachedRouting, whose role routing detects opencode's channels
// with sqlite3 or `opencode auth list` (and a dry run must not touch opencode's database).
func Budget(cfg usage.Config) (*wire.Budget, error) {
	reg, err := registry.Load(cfg.RegistryFile)
	if err != nil {
		return nil, err
	}
	cached := usage.ReadCached(cfg, maxBudgetAge)
	if cached == nil {
		return nil, nil
	}
	generatedAt, ok := parseTime(jsjson.Path(cached, "generated_at"))
	if !ok || cfg.Now().Sub(generatedAt) > maxBudgetAge {
		return nil, nil
	}
	postures := routing.VendorPostures(cached, reg, cfg.Now())
	openai := postureOf(postures.OpenAI, "five_hour")
	openai.Credits = creditsOf(postures.OpenAI.Detail)
	return &wire.Budget{
		Anthropic: postureOf(postures.Anthropic, "session"),
		OpenAI:    openai,
	}, nil
}

// creditsOf maps the OpenAI credits fact of the Codex usage entry; nil when Codex reported
// none. A balance longer than the schema's 40 characters is sent as null.
func creditsOf(codex any) *wire.Credits {
	fact := routing.OpenAICredits(codex)
	if fact == nil {
		return nil
	}
	balance := fact.Balance
	if balance != nil && len(*balance) > maxBalanceLength {
		balance = nil
	}
	return &wire.Credits{
		Balance:          balance,
		HasCredits:       fact.HasCredits,
		Unlimited:        fact.Unlimited,
		PlanLimitReached: fact.PlanLimitReached,
		BankedResets:     int(fact.BankedResets),
	}
}

// maxBalanceLength is the schema's maxLength of a credits balance (ASCII digits, so bytes
// count as UTF-16 units).
const maxBalanceLength = 40

// postureOf maps one vendor's resolved posture; fiveHourWindow names the vendor's 5-hour window
// in its usage entry (Claude "session", Codex "five_hour").
func postureOf(posture routing.VendorPosture, fiveHourWindow string) *wire.Posture {
	level := posture.Overall
	if level != routing.Green && level != routing.Amber && level != routing.Red {
		level = routing.Unknown
	}
	resetsAt, _ := parseTime(jsjson.Path(posture.Detail, fiveHourWindow, "resets_at"))
	weeklyResetsAt, _ := parseTime(jsjson.Path(posture.Detail, "weekly", "resets_at"))
	return &wire.Posture{
		Level:          level,
		FiveHourPct:    percentage(jsjson.Path(posture.Detail, fiveHourWindow, "pct")),
		WeeklyPct:      percentage(jsjson.Path(posture.Detail, "weekly", "pct")),
		ResetsAt:       isoOrNull(resetsAt),
		WeeklyResetsAt: isoOrNull(weeklyResetsAt),
		Window:         drivingWindow(posture),
	}
}

// drivingWindow names the window whose reset lifts the vendor's level. The weekly window counts
// toward the level only once it is red (routing's weeklyBlock), and when both are red its reset
// is the one that unblocks the vendor; otherwise, and when nothing is known, it is the 5-hour one.
func drivingWindow(posture routing.VendorPosture) string {
	if weekly, ok := posture.Window("weekly"); ok && weekly == routing.Red {
		return wire.WindowWeekly
	}
	return wire.WindowFiveHour
}

// percentage is a used percentage the schema accepts (a number in 0..100), else nil.
func percentage(value any) *float64 {
	pct, isNumber := value.(float64)
	if !isNumber || pct < 0 || pct > 100 {
		return nil
	}
	return &pct
}
