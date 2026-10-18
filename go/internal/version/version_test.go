package version

import (
	"regexp"
	"runtime/debug"
	"testing"
)

func TestNumberIsASemanticVersion(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Number()) {
		t.Errorf("Number() = %q, want MAJOR.MINOR.PATCH", Number())
	}
}

func TestDescribe(t *testing.T) {
	revision := debug.BuildSetting{Key: "vcs.revision", Value: "5ae996bc0ffee"}
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"no vcs info", nil, "0.5.0"},
		{"clean checkout", []debug.BuildSetting{revision, {Key: "vcs.modified", Value: "false"}}, "0.5.0 (5ae996b)"},
		{"dirty checkout", []debug.BuildSetting{revision, {Key: "vcs.modified", Value: "true"}}, "0.5.0 (5ae996b+dirty)"},
	}
	for _, c := range cases {
		if got := describe("0.5.0", c.settings); got != c.want {
			t.Errorf("%s: describe = %q, want %q", c.name, got, c.want)
		}
	}
}
