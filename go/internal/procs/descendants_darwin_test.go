package procs

import (
	"strings"
	"testing"
)

func TestProcessSnapshotParsingAndCap(t *testing.T) {
	entries, err := parseProcessTable(" 1 0 Ss\n 2 1 Z+\n 3 2 S+\n")
	if err != nil || len(entries) != 3 || !entries[1].zombie {
		t.Fatalf("snapshot %+v %v", entries, err)
	}
	for _, table := range []string{"", "1 2", "1 2 S extra", "a 2 S", "1 b S"} {
		if _, err := parseProcessTable(table); err == nil {
			t.Errorf("accepted %q", table)
		}
	}
	var output processTableBuffer
	data := strings.Repeat("x", (4<<20)+1)
	if n, err := output.Write([]byte(data)); n != len(data) || err != nil || !output.overflow || output.Len() != 4<<20 {
		t.Errorf("cap n=%d len=%d overflow=%v err=%v", n, output.Len(), output.overflow, err)
	}
}
