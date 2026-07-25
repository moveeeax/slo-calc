package budget

import (
	"math"
	"strings"
	"testing"
	"time"
)

func almost(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestComputeExactBudget(t *testing.T) {
	// 99.9% objective, exactly one budget's worth of errors spent.
	r := Compute("svc", 0.999, 999000, 1000000, 30*Day)
	almost(t, "availability", r.Availability, 0.999)
	almost(t, "errorBudget", r.ErrorBudget, 0.001)
	almost(t, "consumed", r.BudgetConsumed, 1.0)
	almost(t, "remaining", r.BudgetRemaining, 0.0)
	almost(t, "burn", r.BurnRate, 1.0)
	if !r.Healthy() {
		t.Errorf("SLO exactly at budget should still be healthy")
	}
	if r.Window != "30d" {
		t.Errorf("window = %q, want 30d", r.Window)
	}
}

func TestComputeBreached(t *testing.T) {
	// Twice the allowed error rate: budget overspent, burn rate 2x.
	r := Compute("svc", 0.999, 998000, 1000000, 30*Day)
	almost(t, "consumed", r.BudgetConsumed, 2.0)
	almost(t, "remaining", r.BudgetRemaining, -1.0)
	almost(t, "burn", r.BurnRate, 2.0)
	if r.Healthy() {
		t.Errorf("overspent SLO should be unhealthy")
	}
}

func TestComputeNoTraffic(t *testing.T) {
	r := Compute("svc", 0.99, 0, 0, 30*Day)
	almost(t, "availability", r.Availability, 1.0)
	almost(t, "consumed", r.BudgetConsumed, 0.0)
	if !r.Healthy() {
		t.Errorf("no traffic should be healthy")
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"5m":    5 * time.Minute,
		"6h":    6 * time.Hour,
		"30d":   30 * Day,
		"1w":    Week,
		"1h30m": 90 * time.Minute,
		"90s":   90 * time.Second,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseDurationErrors(t *testing.T) {
	for _, in := range []string{"", "abc", "10", "5x", "d5", "5.5h", "-5m"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) expected error", in)
		}
	}
}

// TestParseDurationOverflow guards the int64-nanosecond ceiling. Before this
// was checked, "100000000000d" parsed happily into 1734576h44m48.761856s —
// a wrapped-around window that then produced a confidently wrong budget.
func TestParseDurationOverflow(t *testing.T) {
	overflowing := []string{
		"100000000000d",        // wraps to a small positive duration
		"9223372036854775807s", // MaxInt64 seconds
		"1000000w",             // ~19000 years
		"106752d",              // one day past the ~292-year ceiling
		// A sum that overflows even though no single term does: MaxInt64 ns
		// is under 366*292 days.
		strings.Repeat("292d", 366),
	}
	for _, in := range overflowing {
		got, err := ParseDuration(in)
		if err == nil {
			t.Errorf("ParseDuration(%.40q...) = %v, want an overflow error", in, got)
		}
	}

	// The largest representable duration must still parse, and must not be
	// rejected by an over-eager guard. MaxInt64 ns is 106751d + change, so
	// 106750 days is comfortably inside the ceiling.
	if d, err := ParseDuration("106750d"); err != nil {
		t.Errorf("ParseDuration(106750d) should fit in a Duration: %v", err)
	} else if d != 106750*Day {
		t.Errorf("ParseDuration(106750d) = %v, want %v", d, 106750*Day)
	}
}

func TestShortDur(t *testing.T) {
	cases := map[time.Duration]string{
		30 * Day:         "30d",
		Week:             "1w",
		6 * time.Hour:    "6h",
		30 * time.Minute: "30m",
	}
	for d, want := range cases {
		if got := shortDur(d); got != want {
			t.Errorf("shortDur(%v) = %q, want %q", d, got, want)
		}
	}
}
