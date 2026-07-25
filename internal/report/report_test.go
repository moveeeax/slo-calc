package report

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/spec"
)

// fakeQuerier answers from a map keyed by the exact rendered expression.
type fakeQuerier map[string]float64

func (f fakeQuerier) Query(_ context.Context, expr string, _ time.Time) (float64, error) {
	return f[expr], nil
}

func testSpec(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Parse([]byte(`
slos:
  - name: ratio-slo
    objective: 99.9
    type: ratio
    good: good_events[$window]
    total: total_events[$window]
  - name: latency-slo
    objective: 99.0
    type: latency
    good: fast_requests[$window]
    total: all_requests[$window]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func TestEvaluateRatioAndLatency(t *testing.T) {
	q := fakeQuerier{
		"good_events[30d]":   999000,
		"total_events[30d]":  1000000,
		"fast_requests[30d]": 985000, // 98.5% < 99% objective => breached
		"all_requests[30d]":  1000000,
	}
	rep, err := Evaluate(context.Background(), q, testSpec(t), 30*budget.Day, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(rep.Entries))
	}
	ratio := rep.Entries[0]
	if ratio.Name != "ratio-slo" || !ratio.Healthy() {
		t.Errorf("ratio slo should be healthy at exactly budget: %+v", ratio.Result)
	}
	if len(ratio.Alerts) != 4 {
		t.Errorf("expected 4 burn-rate alerts, got %d", len(ratio.Alerts))
	}
	lat := rep.Entries[1]
	if lat.Healthy() {
		t.Errorf("latency slo at 98.5%% under a 99%% objective should be breached")
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	q := fakeQuerier{
		"good_events[30d]":   999000,
		"total_events[30d]":  1000000,
		"fast_requests[30d]": 999000,
		"all_requests[30d]":  1000000,
	}
	rep, err := Evaluate(context.Background(), q, testSpec(t), 30*budget.Day, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := rep.JSON(&buf); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("report JSON does not round-trip: %v", err)
	}
	if len(back.Entries) != 2 || back.Window != "30d" {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestFormatMinutes(t *testing.T) {
	cases := map[float64]string{
		0:     "0m",
		43.2:  "43.2m",
		40.32: "40.3m",
		1.44:  "1.4m",
		0.5:   "30.0s", // below a minute, shown in seconds
		0.06:  "3.6s",
		-43.2: "-43.2m", // an overspent budget keeps its sign
		-0.5:  "-30.0s",
		119.9: "119.9m",
		120:   "2.0h", // two hours and up, shown in hours
		432:   "7.2h", // a 99% SLO's monthly budget
		4320:  "72.0h",
		-432:  "-7.2h",
	}
	for in, want := range cases {
		if got := FormatMinutes(in); got != want {
			t.Errorf("FormatMinutes(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestTableShowsWallClockBudget checks that the headline figure a user
// actually reads — "99.9% over 30d buys you 43.2 minutes" — reaches the
// table, and that a breached SLO shows a negative remainder.
func TestTableShowsWallClockBudget(t *testing.T) {
	q := fakeQuerier{
		"good_events[30d]":   999500, // half the 0.1% budget spent
		"total_events[30d]":  1000000,
		"fast_requests[30d]": 980000, // 98% under a 99% objective => 2x over
		"all_requests[30d]":  1000000,
	}
	rep, err := Evaluate(context.Background(), q, testSpec(t), 30*budget.Day, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	ratio := rep.Entries[0]
	if math.Abs(ratio.ErrorBudgetMinutes-43.2) > 1e-9 {
		t.Errorf("99.9%% over 30d should budget 43.2 minutes, got %v", ratio.ErrorBudgetMinutes)
	}
	if math.Abs(ratio.BudgetRemainingMinutes-21.6) > 1e-6 {
		t.Errorf("half-spent budget should leave 21.6 minutes, got %v", ratio.BudgetRemainingMinutes)
	}
	lat := rep.Entries[1]
	// 99% over 30d budgets 432 minutes; 2% errors is 2x over, so the
	// remainder is 432 * (1-2) = -432.
	if math.Abs(lat.ErrorBudgetMinutes-432) > 1e-9 {
		t.Errorf("99%% over 30d should budget 432 minutes, got %v", lat.ErrorBudgetMinutes)
	}
	if math.Abs(lat.BudgetRemainingMinutes-(-432)) > 1e-6 {
		t.Errorf("2x overspent should leave -432 minutes, got %v", lat.BudgetRemainingMinutes)
	}

	var buf bytes.Buffer
	if err := rep.Table(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"ERR BUDGET", "43.2m", "21.6m", "-7.2h", "BREACHED"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTableRenders(t *testing.T) {
	q := fakeQuerier{
		"good_events[30d]":   999000,
		"total_events[30d]":  1000000,
		"fast_requests[30d]": 999000,
		"all_requests[30d]":  1000000,
	}
	rep, _ := Evaluate(context.Background(), q, testSpec(t), 30*budget.Day, time.Unix(0, 0))
	var buf bytes.Buffer
	if err := rep.Table(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "ratio-slo") || !strings.Contains(out, "BURN") {
		t.Errorf("table missing expected content:\n%s", out)
	}
}
