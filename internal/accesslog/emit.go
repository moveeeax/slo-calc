package accesslog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/moveeeax/slo-calc/internal/spec"
)

// Metrics names the Prometheus series the service will expose once it is
// instrumented (or once a log exporter such as mtail produces them). The
// defaults are the names client_golang's promhttp middleware and most HTTP
// instrumentation libraries use.
type Metrics struct {
	// Name prefixes the generated SLO names: <name>-availability,
	// <name>-latency.
	Name string
	// Requests is a counter with a status-code label.
	Requests string
	// CodeLabel is that status-code label ("code" in promhttp).
	CodeLabel string
	// Duration is a histogram in seconds; its _bucket and _count series
	// are used.
	Duration string
	// Selector is a label matcher list without braces, such as
	// job="api",handler!="/healthz", applied to every series.
	Selector string
	// Labels are attached to the generated rules.
	Labels map[string]string
}

// DefaultMetrics follows promhttp's naming.
var DefaultMetrics = Metrics{
	Name:      "service",
	Requests:  "http_requests_total",
	CodeLabel: "code",
	Duration:  "http_request_duration_seconds",
}

var metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Validate rejects names that would render into invalid PromQL.
func (m Metrics) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("slo name must not be empty")
	}
	for _, n := range []string{m.Requests, m.Duration} {
		if !metricName.MatchString(n) {
			return fmt.Errorf("invalid metric name %q", n)
		}
	}
	if !labelName.MatchString(m.CodeLabel) {
		return fmt.Errorf("invalid label name %q", m.CodeLabel)
	}
	if strings.ContainsAny(m.Selector, "{}") {
		return fmt.Errorf("selector %q must not include braces", m.Selector)
	}
	return nil
}

// BuildSpec writes an slo-calc spec for the SLIs measured from the logs,
// expressed over the metrics that will keep measuring them. The latency SLO
// is included only when target is non-zero; its le bucket is the target in
// seconds, so the histogram must have a bucket at exactly that boundary.
func BuildSpec(m Metrics, objective, latencyObjective float64, target time.Duration) *spec.Spec {
	s := &spec.Spec{}
	s.SLOs = append(s.SLOs, spec.SLO{
		Name:      m.Name + "-availability",
		Objective: pct(objective),
		Type:      spec.Ratio,
		Good:      increase(m.Requests, m.Selector, fmt.Sprintf(`%s!~"5.."`, m.CodeLabel)),
		Total:     increase(m.Requests, m.Selector, ""),
		Labels:    m.Labels,
	})
	if target > 0 {
		le := strconv.FormatFloat(target.Seconds(), 'f', -1, 64)
		s.SLOs = append(s.SLOs, spec.SLO{
			Name:      m.Name + "-latency",
			Objective: pct(latencyObjective),
			Type:      spec.Latency,
			Good:      increase(m.Duration+"_bucket", m.Selector, fmt.Sprintf(`le=%q`, le)),
			Total:     increase(m.Duration+"_count", m.Selector, ""),
			Labels:    m.Labels,
		})
	}
	return s
}

// pct turns a fraction back into the spec's percentage, trimming the float
// residue of the round trip (0.999*100 is 99.900000000000006).
func pct(f float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(f*100, 'f', 10, 64), 64)
	return v
}

func increase(metric, selector, extra string) string {
	var ms []string
	if selector != "" {
		ms = append(ms, selector)
	}
	if extra != "" {
		ms = append(ms, extra)
	}
	sel := ""
	if len(ms) > 0 {
		sel = "{" + strings.Join(ms, ",") + "}"
	}
	return fmt.Sprintf("sum(increase(%s%s[$window]))", metric, sel)
}
