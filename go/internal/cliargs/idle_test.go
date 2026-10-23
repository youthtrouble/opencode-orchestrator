package cliargs

import "testing"

func TestIdleMinutes(t *testing.T) {
	for _, raw := range []any{"", true, "-1", "+1", ".5", "1.", "1e3", "NaN", "Inf", " 1", "10080.1", "9999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999"} {
		if _, err := IdleMinutes(raw, "ORCH_IDLE_MIN"); err == nil {
			t.Errorf("accepted %v", raw)
		}
	}
	for raw, want := range map[string]float64{"0": 0, "00": 0, "0.25": 0.25, "20": 20, "10080": 10080} {
		if got, err := IdleMinutes(raw, "--idle-min"); err != nil || got != want {
			t.Errorf("%q = %v, %v", raw, got, err)
		}
	}
}
