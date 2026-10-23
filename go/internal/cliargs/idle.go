package cliargs

import (
	"fmt"
	"regexp"
	"strconv"
)

var idleDecimal = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// IdleMinutes validates the watchdog's decimal minutes, without JS numeric coercion.
// source is the option or environment variable named in a usage error.
func IdleMinutes(raw any, source string) (float64, error) {
	s, ok := raw.(string)
	if ok && idleDecimal.MatchString(s) {
		minutes, err := strconv.ParseFloat(s, 64)
		if err == nil && minutes >= 0 && minutes <= 10080 {
			return minutes, nil
		}
	}
	return 0, fmt.Errorf("%s must be minutes >= 0 (0 disables); got %v", source, raw)
}
