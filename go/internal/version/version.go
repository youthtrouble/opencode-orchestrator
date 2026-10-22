// Package version reports the orch release. The VERSION file is the single source of truth;
// the VCS revision comes from the build info Go embeds, so no -ldflags are needed and
// `go install` and local builds agree.
package version

import (
	_ "embed"
	"runtime/debug"
	"strings"
)

//go:embed VERSION
var versionFile string

// Number is the release number from VERSION, in "X.Y.Z" form.
func Number() string {
	return strings.TrimSpace(versionFile)
}

// String is what `orch version` prints: "X.Y.Z (abc1234)", "X.Y.Z (abc1234+dirty)", or just
// "X.Y.Z" when the binary carries no VCS information (tests, builds outside a checkout).
func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Number()
	}
	return describe(Number(), info.Settings)
}

func describe(number string, settings []debug.BuildSetting) string {
	var revision string
	var modified bool
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return number
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if modified {
		revision += "+dirty"
	}
	return number + " (" + revision + ")"
}
