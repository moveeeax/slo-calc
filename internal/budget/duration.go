package budget

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Day and Week extend Go's time units, which stop at hours.
const (
	Day  = 24 * time.Hour
	Week = 7 * Day
)

// maxDuration is the largest value a time.Duration (int64 nanoseconds) can
// hold, about 292 years.
const maxDuration = time.Duration(math.MaxInt64)

// ParseDuration parses a Prometheus-style duration such as "5m", "6h",
// "30d" or "2w". Unlike time.ParseDuration it understands days and weeks,
// which SLO windows routinely use.
//
// Values too large to fit in a time.Duration are rejected rather than
// silently wrapping around into a small or negative window.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	num := strings.Builder{}
	matched := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			num.WriteByte(c)
			continue
		}
		if num.Len() == 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		n, err := strconv.Atoi(num.String())
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		num.Reset()
		var unit time.Duration
		switch c {
		case 's':
			unit = time.Second
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		case 'd':
			unit = Day
		case 'w':
			unit = Week
		default:
			return 0, fmt.Errorf("invalid duration %q: unknown unit %q", s, string(c))
		}
		if time.Duration(n) > maxDuration/unit {
			return 0, fmt.Errorf("invalid duration %q: %d%s overflows the maximum duration of %v", s, n, string(c), maxDuration)
		}
		part := time.Duration(n) * unit
		if part > maxDuration-total {
			return 0, fmt.Errorf("invalid duration %q: sum overflows the maximum duration of %v", s, maxDuration)
		}
		total += part
		matched = true
	}
	if num.Len() != 0 || !matched {
		return 0, fmt.Errorf("invalid duration %q: trailing number without unit", s)
	}
	return total, nil
}
