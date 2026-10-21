package snapshot

import (
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
)

func TestCleanerRedactsThenCutsWithinTheCap(t *testing.T) {
	clean := cleaner{home: "/srv/owner-home"}
	cases := []struct {
		in       string
		maxUnits int
		want     string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven chars", 10, "eleven ch…"},
		{"see /srv/owner-home/a.md", 40, "see ~/a.md"},
		{"mail owner@example.com", 40, "mail <email>"},
		{"key sk-" + strings.Repeat("A", 24), 40, "key <token>"},
		// 😀 is two UTF-16 units: the cut never splits it.
		{"ab😀cd", 4, "ab…"},
		{"ab😀cd", 5, "ab😀…"},
		{"", 10, ""},
	}
	for _, c := range cases {
		if got := clean.text(c.in, c.maxUnits); got != c.want {
			t.Errorf("text(%q, %d) = %q, want %q", c.in, c.maxUnits, got, c.want)
		}
	}
}

func TestCleanerMarksTextRedactCutAtItsOwnCap(t *testing.T) {
	long := strings.Repeat("y", 3000)
	if got := incident.Redact("/h", long); utf16Length(got) != redactCutUnits {
		t.Fatalf("incident.Redact cut at %d units, not redactCutUnits (%d)", utf16Length(got), redactCutUnits)
	}
	got := cleaner{home: "/h"}.text(long, maxBodyUnits)
	if utf16Length(got) != maxBodyUnits || !strings.HasSuffix(got, ellipsis) {
		t.Errorf("a body cut by Redact is %d units, ends %q; want %d ending in …", utf16Length(got), got[len(got)-8:], maxBodyUnits)
	}
}

func TestOptionalIsNullForEmptyText(t *testing.T) {
	clean := cleaner{home: "/h"}
	if got := clean.optional("", 10); got != nil {
		t.Errorf("optional(\"\") = %q, want nil", *got)
	}
	if got := clean.optional("x", 10); got == nil || *got != "x" {
		t.Errorf("optional(\"x\") = %v", got)
	}
}
