package accesslog

import (
	"strings"
	"testing"
	"time"

	"github.com/moveeeax/slo-calc/internal/spec"

	"go.yaml.in/yaml/v3"
)

func TestBuildSpecRoundTrips(t *testing.T) {
	m := DefaultMetrics
	m.Name = "api"
	m.Selector = `job="api"`
	sp := BuildSpec(m, 0.999, 0.99, 250*time.Millisecond)
	raw, err := yaml.Marshal(sp)
	if err != nil {
		t.Fatal(err)
	}
	back, err := spec.Parse(raw)
	if err != nil {
		t.Fatalf("emitted spec does not validate: %v\n%s", err, raw)
	}
	if len(back.SLOs) != 2 {
		t.Fatalf("want availability and latency SLOs, got %d", len(back.SLOs))
	}
	a, l := back.SLOs[0], back.SLOs[1]
	if a.Name != "api-availability" || a.Objective != 99.9 || a.Type != spec.Ratio {
		t.Errorf("availability slo: %+v", a)
	}
	if a.Good != `sum(increase(http_requests_total{job="api",code!~"5.."}[$window]))` ||
		a.Total != `sum(increase(http_requests_total{job="api"}[$window]))` {
		t.Errorf("availability queries: %q / %q", a.Good, a.Total)
	}
	if l.Name != "api-latency" || l.Objective != 99 || l.Type != spec.Latency {
		t.Errorf("latency slo: %+v", l)
	}
	if !strings.Contains(l.Good, `http_request_duration_seconds_bucket{job="api",le="0.25"}`) ||
		l.Total != `sum(increase(http_request_duration_seconds_count{job="api"}[$window]))` {
		t.Errorf("latency queries: %q / %q", l.Good, l.Total)
	}
	if strings.Contains(string(raw), "labels") {
		t.Errorf("empty labels should be omitted:\n%s", raw)
	}
}

func TestBuildSpecNoSelectorNoLatency(t *testing.T) {
	sp := BuildSpec(DefaultMetrics, 0.995, 0.995, 0)
	if len(sp.SLOs) != 1 {
		t.Fatalf("no latency target should mean no latency slo: %+v", sp.SLOs)
	}
	if sp.SLOs[0].Total != "sum(increase(http_requests_total[$window]))" {
		t.Errorf("total = %q", sp.SLOs[0].Total)
	}
	if sp.SLOs[0].Objective != 99.5 {
		t.Errorf("objective = %v", sp.SLOs[0].Objective)
	}
}

func TestMetricsValidate(t *testing.T) {
	bad := []func(*Metrics){
		func(m *Metrics) { m.Name = "" },
		func(m *Metrics) { m.Requests = "http-requests" },
		func(m *Metrics) { m.Duration = "1x" },
		func(m *Metrics) { m.CodeLabel = "status code" },
		func(m *Metrics) { m.Selector = `{job="api"}` },
	}
	for i, mod := range bad {
		m := DefaultMetrics
		mod(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("case %d: %+v should be rejected", i, m)
		}
	}
	if err := DefaultMetrics.Validate(); err != nil {
		t.Errorf("defaults rejected: %v", err)
	}
}
