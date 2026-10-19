package jsjson

import "testing"

// Messages probed with Node v26.5.1 in strict mode.
func TestPropertyErrorsAreV8Messages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{CannotAssign(5.0, "lane"), "Cannot create property 'lane' on number '5'"},
		{CannotAssign("abc", "storage_error"), "Cannot create property 'storage_error' on string 'abc'"},
		{CannotAssign(true, "x"), "Cannot create property 'x' on boolean 'true'"},
		{CannotAssign(nil, "events"), "Cannot set properties of null (setting 'events')"},
		{CannotReadOfNull("events"), "Cannot read properties of null (reading 'events')"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("message = %q, want %q", got, tc.want)
		}
		if got := ErrorString(tc.err); got != "TypeError: "+tc.want {
			t.Errorf("String(err) = %q", got)
		}
	}
	if got := ErrorString(ErrInvalidTime); got != "RangeError: Invalid time value" {
		t.Errorf("String(RangeError) = %q", got)
	}
}
