package procs

import "testing"

func TestParseProcStatAfterLastParenthesis(t *testing.T) {
	got, err := parseProcStat(42, "42 (a ) tricky ( name)) Z 17 0 0 0")
	if err != nil || got.pid != 42 || got.parent != 17 || !got.zombie {
		t.Fatalf("entry %+v, %v", got, err)
	}
	for _, stat := range []string{"", "42 no comm Z 17", "42 (x) Z", "42 (x) Z no", "42 (x) ZZ 1"} {
		if _, err := parseProcStat(42, stat); err == nil {
			t.Errorf("accepted %q", stat)
		}
	}
}
