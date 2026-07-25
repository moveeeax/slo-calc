package burnrate

import (
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
)

// referenceFactor recomputes a burn-rate multiplier in exact rational
// arithmetic, sharing no code with the implementation:
//
//	factor = budgetConsumedFraction * sloWindow / longWindow
//
// consumedNum/consumedDen expresses the budget-consumed fraction exactly
// (2% is 1/50), so no floating point enters the reference at all.
func referenceFactor(consumedNum, consumedDen int64, sloWindow, long time.Duration) *big.Rat {
	consumed := big.NewRat(consumedNum, consumedDen)
	ratio := new(big.Rat).SetFrac64(int64(sloWindow), int64(long))
	return new(big.Rat).Mul(consumed, ratio)
}

// exactConsumed spells out DefaultLevels' consumed fractions as exact
// rationals. Keeping them here rather than reading Level.Consumed is the
// point: the test must not inherit a mistake from the table it checks.
var exactConsumed = [][2]int64{
	{2, 100},  // page  1h/5m  -> 2% of the budget
	{5, 100},  // page  6h/30m -> 5%
	{10, 100}, // tick  1d/2h  -> 10%
	{10, 100}, // tick  3d/6h  -> 10%
}

// TestFactorsDifferential sweeps every level against every plausible SLO
// window and compares Factor with the exact rational reference.
func TestFactorsDifferential(t *testing.T) {
	windows := []time.Duration{
		time.Hour, 12 * time.Hour, budget.Day, 7 * budget.Day, budget.Week,
		14 * budget.Day, 28 * budget.Day, 30 * budget.Day, 31 * budget.Day,
		90 * budget.Day, 180 * budget.Day, 365 * budget.Day,
	}
	for _, w := range windows {
		for i, l := range DefaultLevels {
			want, _ := referenceFactor(exactConsumed[i][0], exactConsumed[i][1], w, l.Long).Float64()
			got := l.Factor(w)
			if math.Abs(got-want) > 1e-12*math.Max(1, math.Abs(want)) {
				t.Errorf("level %d (%v/%v) factor at window %v = %v, exact reference %v",
					i, l.Long, l.Short, w, got, want)
			}
		}
	}
}

// TestCanonicalTableMatchesSREWorkbook checks the published 30-day table
// row by row, including the short window being exactly one twelfth of the
// long window — the workbook's rule, and an easy place to fat-finger a
// constant.
func TestCanonicalTableMatchesSREWorkbook(t *testing.T) {
	want := []struct {
		severity    string
		long, short time.Duration
		factor      float64
		consumed    float64
	}{
		{"page", time.Hour, 5 * time.Minute, 14.4, 0.02},
		{"page", 6 * time.Hour, 30 * time.Minute, 6, 0.05},
		{"ticket", budget.Day, 2 * time.Hour, 3, 0.10},
		{"ticket", 3 * budget.Day, 6 * time.Hour, 1, 0.10},
	}
	if len(DefaultLevels) != len(want) {
		t.Fatalf("table has %d rows, want %d", len(DefaultLevels), len(want))
	}
	for i, l := range DefaultLevels {
		w := want[i]
		if l.Severity != w.severity || l.Long != w.long || l.Short != w.short {
			t.Errorf("row %d = %s %v/%v, want %s %v/%v",
				i, l.Severity, l.Long, l.Short, w.severity, w.long, w.short)
		}
		if math.Abs(l.Consumed-w.consumed) > 1e-12 {
			t.Errorf("row %d consumed = %v, want %v", i, l.Consumed, w.consumed)
		}
		if got := l.Factor(30 * budget.Day); math.Abs(got-w.factor) > 1e-9 {
			t.Errorf("row %d factor at 30d = %v, want %v", i, got, w.factor)
		}
		// The workbook derives every short window as long/12.
		if l.Short*12 != l.Long {
			t.Errorf("row %d short window %v is not one twelfth of %v", i, l.Short, l.Long)
		}
	}
}

// TestThresholdsDifferential checks the alert thresholds — the numbers that
// actually end up in the Prometheus rules — against exact arithmetic across
// a sweep of objectives and windows.
func TestThresholdsDifferential(t *testing.T) {
	objectives := []string{"90", "95", "99", "99.5", "99.9", "99.95", "99.99", "99.999"}
	windows := []time.Duration{
		budget.Day, 7 * budget.Day, 28 * budget.Day, 30 * budget.Day, 90 * budget.Day, 365 * budget.Day,
	}
	for _, o := range objectives {
		pct, ok := new(big.Rat).SetString(o)
		if !ok {
			t.Fatalf("bad objective %q", o)
		}
		errBudget := new(big.Rat).Sub(
			new(big.Rat).SetInt64(1),
			new(big.Rat).Quo(pct, new(big.Rat).SetInt64(100)),
		)
		f, err := strconv.ParseFloat(o, 64)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range windows {
			alerts := Alerts(f/100, w)
			if len(alerts) != len(DefaultLevels) {
				t.Fatalf("got %d alerts, want %d", len(alerts), len(DefaultLevels))
			}
			for i, a := range alerts {
				ref := referenceFactor(exactConsumed[i][0], exactConsumed[i][1], w, DefaultLevels[i].Long)
				want, _ := new(big.Rat).Mul(ref, errBudget).Float64()
				// Alerts rounds the threshold to 8 decimals for PromQL, so
				// allow a half-step there and nothing more.
				if math.Abs(a.Threshold-want) > 5.1e-9 {
					t.Errorf("objective %s%% window %v row %d: threshold = %.12f, exact reference %.12f",
						o, w, i, a.Threshold, want)
				}
			}
		}
	}
}

// TestUnreachableThresholds documents the trap that a loose objective sets:
// the rule is emitted, promtool accepts it, and it can never fire because an
// error ratio cannot exceed 1.
func TestUnreachableThresholds(t *testing.T) {
	// 99.9% over 30d: every threshold is far below 1.
	for i, a := range Alerts(0.999, 30*budget.Day) {
		if !a.Reachable {
			t.Errorf("row %d of a 99.9%% SLO should be reachable (threshold %v)", i, a.Threshold)
		}
	}
	// 90% over 30d: the 14.4x page row needs an error ratio above 1.44.
	alerts := Alerts(0.90, 30*budget.Day)
	if math.Abs(alerts[0].Threshold-1.44) > 1e-9 {
		t.Fatalf("expected a 1.44 threshold, got %v", alerts[0].Threshold)
	}
	if alerts[0].Reachable {
		t.Errorf("a threshold of %v cannot be exceeded by a ratio and must be flagged", alerts[0].Threshold)
	}
	for i, a := range alerts[1:] {
		if !a.Reachable {
			t.Errorf("row %d (threshold %v) is below 1 and should be reachable", i+1, a.Threshold)
		}
	}
}
