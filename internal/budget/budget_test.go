package budget

import (
	"math"
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
	for _, in := range []string{"", "abc", "10", "5x", "d5", "5.5h"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) expected error", in)
		}
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
