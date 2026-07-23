package report

import (
	"bytes"
	"context"
	"encoding/json"
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
