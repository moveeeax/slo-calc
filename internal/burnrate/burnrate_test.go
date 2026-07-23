package burnrate

import (
	"math"
	"testing"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
)

func TestCanonicalFactorsAt30d(t *testing.T) {
	window := 30 * budget.Day
	want := []float64{14.4, 6, 3, 1}
	for i, l := range DefaultLevels {
		got := l.Factor(window)
		if math.Abs(got-want[i]) > 1e-9 {
			t.Errorf("level %d factor = %v, want %v", i, got, want[i])
		}
	}
}

func TestAlertsThresholds(t *testing.T) {
	// objective 99.9% => error budget 0.001.
	alerts := Alerts(0.999, 30*budget.Day)
	if len(alerts) != 4 {
		t.Fatalf("want 4 alerts, got %d", len(alerts))
	}
	wantTh := []float64{0.0144, 0.006, 0.003, 0.001}
	for i, a := range alerts {
		if math.Abs(a.Threshold-wantTh[i]) > 1e-9 {
			t.Errorf("alert %d threshold = %v, want %v", i, a.Threshold, wantTh[i])
		}
	}
	if alerts[0].Severity != "page" || alerts[0].Long != "1h" || alerts[0].Short != "5m" {
		t.Errorf("first alert row wrong: %+v", alerts[0])
	}
}

func TestFactorsScaleWithWindow(t *testing.T) {
	// A 7d window keeps the budget-consumed fractions but scales factors.
	// page/1h: 0.02 * 7d / 1h = 0.02 * 168 = 3.36.
	got := DefaultLevels[0].Factor(7 * budget.Day)
	if math.Abs(got-3.36) > 1e-9 {
		t.Errorf("7d page/1h factor = %v, want 3.36", got)
	}
}

func TestWindowsDistinct(t *testing.T) {
	w := Windows()
	if len(w) != 7 {
		t.Fatalf("want 7 distinct windows, got %d: %v", len(w), w)
	}
	seen := map[time.Duration]bool{}
	for _, d := range w {
		if seen[d] {
			t.Errorf("duplicate window %v", d)
		}
		seen[d] = true
	}
}
