package budget

import (
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"
)

// referenceBudgetMinutes is an independent implementation of the wall-clock
// error budget, deliberately sharing no code with the package under test:
//
//	minutes = window_nanoseconds / 60e9 * (1 - objectivePercent/100)
//
// The objective arrives as an exact decimal string, so the whole thing is
// done in exact rational arithmetic with no floating point at all. Any
// disagreement with Compute is therefore a bug in Compute, not a rounding
// argument between two equally-approximate implementations.
func referenceBudgetMinutes(t *testing.T, objectivePercent string, window time.Duration) float64 {
	t.Helper()
	pct, ok := new(big.Rat).SetString(objectivePercent)
	if !ok {
		t.Fatalf("bad objective %q", objectivePercent)
	}
	frac := new(big.Rat).Quo(pct, new(big.Rat).SetInt64(100))
	errBudget := new(big.Rat).Sub(new(big.Rat).SetInt64(1), frac)
	minutes := new(big.Rat).Quo(
		new(big.Rat).SetInt64(int64(window)),
		new(big.Rat).SetInt64(int64(time.Minute)),
	)
	got, _ := new(big.Rat).Mul(minutes, errBudget).Float64()
	return got
}

func objectiveFraction(t *testing.T, percent string) float64 {
	t.Helper()
	p, err := strconv.ParseFloat(percent, 64)
	if err != nil {
		t.Fatalf("bad objective %q: %v", percent, err)
	}
	return p / 100
}

// TestErrorBudgetMinutesCanonical pins the headline numbers of the whole
// tool against values worked out by hand, not by running the code:
//
//	30d = 30*24*60 = 43200 min;  43200 * 0.001  = 43.2
//	28d = 28*24*60 = 40320 min;  40320 * 0.001  = 40.32
//
// If any of these move, the calculator is lying to its users.
func TestErrorBudgetMinutesCanonical(t *testing.T) {
	cases := []struct {
		objective string
		window    time.Duration
		want      float64 // hand-derived, see comment per row
	}{
		{"99.9", 30 * Day, 43.2},    // 43200 * 0.001
		{"99.9", 28 * Day, 40.32},   // 40320 * 0.001
		{"99.9", 7 * Day, 10.08},    // 10080 * 0.001
		{"99.9", Week, 10.08},       // same window, written as 1w
		{"99.9", Day, 1.44},         // 1440 * 0.001
		{"99.9", 90 * Day, 129.6},   // 129600 * 0.001
		{"99.9", 365 * Day, 525.6},  // 525600 * 0.001
		{"99.95", 30 * Day, 21.6},   // 43200 * 0.0005
		{"99.99", 30 * Day, 4.32},   // 43200 * 0.0001
		{"99.99", 365 * Day, 52.56}, // 525600 * 0.0001
		{"99.999", 30 * Day, 0.432}, // 43200 * 0.00001
		{"99", 30 * Day, 432},       // 43200 * 0.01
		{"99", 7 * Day, 100.8},      // 10080 * 0.01
		{"95", 24 * time.Hour, 72},  // 1440 * 0.05
		{"90", 30 * Day, 4320},      // 43200 * 0.1
		{"99.9", time.Hour, 0.06},   // 60 * 0.001
		{"99.9", 5 * time.Minute, 0.005},
	}
	for _, c := range cases {
		r := Compute("svc", objectiveFraction(t, c.objective), 0, 0, c.window)
		if math.Abs(r.ErrorBudgetMinutes-c.want) > 1e-9 {
			t.Errorf("ErrorBudgetMinutes(%s%%, %v) = %v, want %v",
				c.objective, c.window, r.ErrorBudgetMinutes, c.want)
		}
		// With no errors observed, the whole budget is still unspent.
		if math.Abs(r.BudgetRemainingMinutes-c.want) > 1e-9 {
			t.Errorf("BudgetRemainingMinutes(%s%%, %v) = %v, want %v (untouched)",
				c.objective, c.window, r.BudgetRemainingMinutes, c.want)
		}
	}
}

// TestErrorBudgetMinutesDifferential sweeps objectives against windows and
// checks every combination against the exact-rational reference above.
func TestErrorBudgetMinutesDifferential(t *testing.T) {
	objectives := []string{"50", "90", "95", "99", "99.5", "99.9", "99.95", "99.99", "99.999", "99.9999"}
	windows := []time.Duration{
		time.Minute, 5 * time.Minute, 30 * time.Minute, time.Hour, 6 * time.Hour,
		12 * time.Hour, Day, 3 * Day, Week, 2 * Week, 28 * Day, 30 * Day,
		31 * Day, 90 * Day, 180 * Day, 365 * Day,
	}
	for _, o := range objectives {
		for _, w := range windows {
			want := referenceBudgetMinutes(t, o, w)
			got := Compute("svc", objectiveFraction(t, o), 0, 0, w).ErrorBudgetMinutes
			// Compute rounds to the microsecond (1e-6 min), so allow a
			// half-step of slack and nothing more.
			if math.Abs(got-want) > 5.1e-7 {
				t.Errorf("ErrorBudgetMinutes(%s%%, %v) = %.12f, exact reference %.12f (delta %g)",
					o, w, got, want, got-want)
			}
		}
	}
}

// TestBudgetRemainingMinutesTracksConsumption checks the remaining-minutes
// figure against the budget scaled by hand-computed consumption fractions.
func TestBudgetRemainingMinutesTracksConsumption(t *testing.T) {
	// 99.9% over 30d: 43.2 minutes of budget in total.
	cases := []struct {
		good, total float64
		wantRemain  float64
		wantMinutes float64
	}{
		// 0 errors: nothing spent.
		{1_000_000, 1_000_000, 1.0, 43.2},
		// 0.05% errors => half the 0.1% budget spent.
		{999_500, 1_000_000, 0.5, 21.6},
		// 0.1% errors => exactly exhausted.
		{999_000, 1_000_000, 0.0, 0},
		// 0.2% errors => 2x over: 43.2 minutes into the red.
		{998_000, 1_000_000, -1.0, -43.2},
		// 0.25% errors => 2.5x: remaining = 43.2 * (1-2.5) = -64.8
		{997_500, 1_000_000, -1.5, -64.8},
	}
	for _, c := range cases {
		r := Compute("svc", 0.999, c.good, c.total, 30*Day)
		if math.Abs(r.BudgetRemaining-c.wantRemain) > 1e-9 {
			t.Errorf("good=%v: BudgetRemaining = %v, want %v", c.good, r.BudgetRemaining, c.wantRemain)
		}
		if math.Abs(r.BudgetRemainingMinutes-c.wantMinutes) > 1e-6 {
			t.Errorf("good=%v: BudgetRemainingMinutes = %v, want %v",
				c.good, r.BudgetRemainingMinutes, c.wantMinutes)
		}
	}
}

// TestWindowMinutes checks the plain day/hour/minute conversion, which is
// where an off-by-one in the unit table would show up first.
func TestWindowMinutes(t *testing.T) {
	cases := map[time.Duration]float64{
		time.Minute:      1,
		time.Hour:        60,
		Day:              1440,
		Week:             10080,
		28 * Day:         40320,
		30 * Day:         43200,
		31 * Day:         44640,
		365 * Day:        525600,
		90 * time.Second: 1.5,
	}
	for w, want := range cases {
		if got := Compute("svc", 0.999, 0, 0, w).WindowMinutes; got != want {
			t.Errorf("WindowMinutes(%v) = %v, want %v", w, got, want)
		}
	}
}

// TestObjectiveBoundaries covers the values that break naive budget maths:
// a 100% objective (zero-width budget), a 0% objective (the whole window is
// budget), and a percentage handed in where a fraction was expected.
func TestObjectiveBoundaries(t *testing.T) {
	t.Run("100% objective with errors is breached", func(t *testing.T) {
		r := Compute("svc", 1.0, 500, 1000, 30*Day)
		if r.ErrorBudget != 0 || r.ErrorBudgetMinutes != 0 {
			t.Errorf("100%% objective should permit zero budget, got %v (%v min)",
				r.ErrorBudget, r.ErrorBudgetMinutes)
		}
		if r.Healthy() {
			t.Errorf("100%% objective at 50%% availability must not be healthy: %+v", r)
		}
		if r.BudgetConsumed != 1 || r.BudgetRemaining != 0 {
			t.Errorf("zero-width budget with errors should read as fully spent, got consumed=%v remaining=%v",
				r.BudgetConsumed, r.BudgetRemaining)
		}
	})

	t.Run("100% objective with no errors is healthy", func(t *testing.T) {
		r := Compute("svc", 1.0, 1000, 1000, 30*Day)
		if !r.Healthy() {
			t.Errorf("100%% objective at 100%% availability should be healthy: %+v", r)
		}
		if r.BudgetConsumed != 0 {
			t.Errorf("consumed = %v, want 0", r.BudgetConsumed)
		}
	})

	t.Run("0% objective makes the whole window budget", func(t *testing.T) {
		r := Compute("svc", 0, 0, 1000, 30*Day)
		if r.ErrorBudget != 1 {
			t.Errorf("errorBudget = %v, want 1", r.ErrorBudget)
		}
		if r.ErrorBudgetMinutes != 43200 {
			t.Errorf("errorBudgetMinutes = %v, want 43200", r.ErrorBudgetMinutes)
		}
		if !r.Healthy() {
			t.Errorf("a 0%% objective can never be breached: %+v", r)
		}
	})

	t.Run("objective above 1 is clamped, never negative budget", func(t *testing.T) {
		// 99.9 passed as a fraction is the classic percentage/fraction
		// mix-up. It must not yield an error budget of -98.9.
		r := Compute("svc", 99.9, 900, 1000, 30*Day)
		if r.ErrorBudget < 0 || r.ErrorBudgetMinutes < 0 {
			t.Errorf("objective > 1 produced a negative budget: %+v", r)
		}
		if r.Objective != 1 {
			t.Errorf("objective = %v, want it clamped to 1", r.Objective)
		}
	})

	t.Run("negative objective is clamped to zero", func(t *testing.T) {
		r := Compute("svc", -0.5, 0, 1000, 30*Day)
		if r.Objective != 0 || r.ErrorBudget != 1 {
			t.Errorf("negative objective not clamped: %+v", r)
		}
	})

	t.Run("zero-length window has zero budget", func(t *testing.T) {
		r := Compute("svc", 0.999, 0, 0, 0)
		if r.WindowMinutes != 0 || r.ErrorBudgetMinutes != 0 {
			t.Errorf("zero window should give zero minutes: %+v", r)
		}
	})

	t.Run("good above total cannot exceed 100% availability", func(t *testing.T) {
		// increase() extrapolation routinely reports slightly more good
		// events than total ones.
		r := Compute("svc", 0.999, 1001, 1000, 30*Day)
		if r.Availability != 1 || r.BudgetConsumed != 0 {
			t.Errorf("availability should clamp at 1: %+v", r)
		}
	})
}
