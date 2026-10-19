package bridge

import (
	"strings"
	"testing"
)

// The expectations are Node 26's messages for spawn("printf", ["--x", arg]).
func TestCheckNullBytesIsNodesSpawnCheck(t *testing.T) {
	const prefix = "TypeError [ERR_INVALID_ARG_VALUE]: The argument 'args[1]' must be a string without null bytes. Received "
	cases := []struct{ arg, received string }{
		{"a\x00b", `'a\x00b'`},
		{"it's\x00", `"it's\x00"`},
		{"q\x00'\"`", `'q\x00\'"` + "`'"},
		{"q\x00'\"${", `'q\x00\'"${'`},
		{"x\x00\n\t\x01\x7fé\U0001F600", `'x\x00\n\t\x01\x7Fé😀'`},
		{"\x00\u0085\u009f \\", `'\x00\x85\x9F` + " " + `\\'`},
		{"\x00\n" + strings.Repeat("b", 75), `'\x00\n' +` + "\n  '" + strings.Repeat("b", 75) + "'"},
		{"\x00" + strings.Repeat("b", 75) + "\n", `'\x00` + strings.Repeat("b", 75) + `\n'`},
		{strings.Repeat("a", 200) + "\x00", "'" + strings.Repeat("a", 127) + "..."},
	}
	for _, c := range cases {
		err := checkNullBytes([]string{"--x", c.arg})
		if err == nil || crashLine(err) != prefix+c.received {
			t.Errorf("checkNullBytes(%q)\n got %v\nwant %s", c.arg, err, prefix+c.received)
		}
	}
	if err := checkNullBytes([]string{"--x", "fine"}); err != nil {
		t.Errorf("checkNullBytes without a NUL: %v", err)
	}
}
