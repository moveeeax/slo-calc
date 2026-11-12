package accesslog

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func analyseFixture(t *testing.T, f Filter) Summary {
	t.Helper()
	p := mustParser(t, Combined, 0)
	fh, err := os.Open("testdata/combined.log")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	agg := NewAggregator(Options{Objective: 0.999, LatencyObjective: 0.99, LatencyTarget: 300 * time.Millisecond, SLOWindow: 30 * budget.Day}, nil, f)
	if err := agg.ReadFrom(p, fh); err != nil {
		t.Fatal(err)
	}
	return agg.Summary()
}

func TestSummaryFixture(t *testing.T) {
	s := analyseFixture(t, Filter{
		ExcludePaths:     []*regexp.Regexp{regexp.MustCompile(`^/healthz`)},
		ExcludeAgents:    []*regexp.Regexp{regexp.MustCompile(`(?i)bot`)},
		ExcludeCancelled: true,
	})
	// 9 lines: 1 garbage, healthz + Googlebot + the 499 excluded, 5 left.
	if s.Lines != 9 || s.Unparsable != 1 || s.Excluded != 3 || s.Requests != 5 {
		t.Fatalf("counters: %+v", s.Stats)
	}
	if s.Errors != 2 || s.Timed != 5 || s.Slow != 2 {
		t.Errorf("errors/timed/slow: %+v", s.Stats)
	}
	if s.Span != 50*time.Minute {
		t.Errorf("span = %v", s.Span)
	}
	// availability 3/5 = 0.6, error ratio 0.4, budget 0.001 => burn 400x.
	a := s.Availability
	if !near(a.Availability, 0.6) || !near(budget.Round(a.BurnRate, 6), 400) {
		t.Errorf("availability SLI: %+v", a.Result)
	}
	// 400x for 50 minutes out of 30 days: 400*50/43200.
	if !near(a.WindowSpent, budget.Round(400.0*50/43200, 6)) {
		t.Errorf("window budget spent = %v", a.WindowSpent)
	}
	// Two of the five timed requests (410ms, 950ms) are over 300ms.
	if s.Latency == nil || !near(s.Latency.Availability, 0.6) || s.Latency.Total != 5 {
		t.Errorf("latency SLI: %+v", s.Latency)
	}
	// Sorted durations 12ms 20ms 100ms 410ms 950ms.
	if s.Percentiles.P50 != 100*time.Millisecond || s.Percentiles.P90 != 950*time.Millisecond || s.Percentiles.P99 != 950*time.Millisecond {
		t.Errorf("percentiles: %+v", s.Percentiles)
	}

	worst := s.Routes[0]
	if worst.Method != "POST" || worst.Route != "/orders" || worst.Errors != 1 || worst.Slow != 2 {
		t.Errorf("worst route: %+v", worst)
	}
	if !near(worst.ErrorShare, 0.5) || !near(worst.SlowShare, 1) {
		t.Errorf("worst route shares: %+v", worst)
	}
	var ids *RouteStat
	for i := range s.Routes {
		if s.Routes[i].Route == "/orders/:id" {
			ids = &s.Routes[i]
		}
	}
	if ids == nil || ids.Requests != 2 {
		t.Errorf("/orders/8213 and /orders/8214 should share one route: %+v", s.Routes)
	}
}

func TestSummaryWithoutExclusions(t *testing.T) {
	s := analyseFixture(t, Filter{})
	if s.Requests != 8 || s.Excluded != 0 {
		t.Errorf("counters: %+v", s.Stats)
	}
	// The 499 is not a server error.
	if s.Errors != 2 {
		t.Errorf("errors = %d", s.Errors)
	}
}

func TestBad(t *testing.T) {
	cases := []struct {
		r    Record
		want bool
	}{
		{Record{Status: 200}, false},
		{Record{Status: 404}, false},
		{Record{Status: 499, Cancelled: true}, false},
		{Record{Status: 500}, true},
		{Record{Status: 504}, true},
		{Record{Status: 0}, true},
		{Record{Status: 0, Cancelled: true}, false},
	}
	for _, c := range cases {
		if got := Bad(c.r); got != c.want {
			t.Errorf("Bad(%+v) = %v", c.r, got)
		}
	}
}

func TestNoTrafficAndNoTimestamps(t *testing.T) {
	agg := NewAggregator(Options{Objective: 0.99, LatencyObjective: 0.99, LatencyTarget: time.Second, SLOWindow: budget.Day}, nil, Filter{})
	agg.Add(Record{Status: 500, Path: "/x"})
	s := agg.Summary()
	if s.Span != 0 || s.Availability.WindowSpent != 0 {
		t.Errorf("no timestamps should give a zero span: %+v", s)
	}
	if s.Latency.Total != 0 || s.Percentiles != nil {
		t.Errorf("no durations should give an empty latency SLI: %+v", s.Latency)
	}
	var b bytes.Buffer
	if err := s.Table(&b, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "NO DATA") || !strings.Contains(b.String(), "no timestamps") {
		t.Errorf("table:\n%s", b.String())
	}
}

func TestOversizeLineIsSkipped(t *testing.T) {
	line := `1.2.3.4 - - [01/Nov/2026:10:00:00 +0000] "GET /a HTTP/1.1" 200 1 "-" "ua"` + "\n"
	in := line + strings.Repeat("x", maxLineBytes+10) + "\n" + line
	agg := NewAggregator(Options{Objective: 0.99, LatencyObjective: 0.99}, nil, Filter{})
	if err := agg.ReadFrom(mustParser(t, Combined, 0), strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	s := agg.Summary()
	if s.Lines != 3 || s.Unparsable != 1 || s.Requests != 2 {
		t.Errorf("counters: %+v", s.Stats)
	}
}

func TestPercentileRank(t *testing.T) {
	var ds []time.Duration
	for i := 100; i >= 1; i-- {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	p := percentiles(ds)
	if p.P50 != 50*time.Millisecond || p.P90 != 90*time.Millisecond || p.P99 != 99*time.Millisecond {
		t.Errorf("percentiles of 1..100ms: %+v", p)
	}
	if rank(nil, 0.5) != 0 {
		t.Error("empty rank should be 0")
	}
}

func TestSummaryJSON(t *testing.T) {
	s := analyseFixture(t, Filter{})
	var b bytes.Buffer
	if err := s.JSON(&b, 2); err != nil {
		t.Fatal(err)
	}
	var back struct {
		Requests    int `json:"requests"`
		Percentiles struct {
			P50 float64 `json:"p50_ms"`
		} `json:"percentiles"`
		Routes []json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Requests != 8 || len(back.Routes) != 2 || back.Percentiles.P50 == 0 {
		t.Errorf("json:\n%s", b.String())
	}
}

func TestOptionsValidate(t *testing.T) {
	for _, o := range []Options{
		{Objective: 1, LatencyObjective: 0.99},
		{Objective: 0, LatencyObjective: 0.99},
		{Objective: math.NaN(), LatencyObjective: 0.99},
		{Objective: 0.99, LatencyObjective: 1.5},
	} {
		if err := o.Validate(); err == nil {
			t.Errorf("%+v should be rejected", o)
		}
	}
}
