package burnrate

import (
	"strings"
	"testing"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/spec"

	"gopkg.in/yaml.v3"
)

func sampleSpec(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Parse([]byte(`
slos:
  - name: api-availability
    objective: 99.9
    type: ratio
    good: sum(increase(http_requests_total{code!~"5.."}[$window]))
    total: sum(increase(http_requests_total[$window]))
  - name: api-latency
    objective: 99.0
    type: latency
    good: sum(increase(http_request_duration_seconds_bucket{le="0.3"}[$window]))
    total: sum(increase(http_request_duration_seconds_count[$window]))
`))
	if err != nil {
		t.Fatalf("parse sample spec: %v", err)
	}
	return s
}

func TestBuildRulesShape(t *testing.T) {
	rf := BuildRules(sampleSpec(t), 30*budget.Day)
	if len(rf.Groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(rf.Groups))
	}
	g := rf.Groups[0]
	if g.Name != "slo:api-availability" {
		t.Errorf("group name = %q", g.Name)
	}
	var records, alerts int
	for _, r := range g.Rules {
		switch {
		case r.Record != "":
			records++
			if r.Labels["slo"] != "api-availability" {
				t.Errorf("recording rule missing slo label: %+v", r)
			}
		case r.Alert != "":
			alerts++
		}
	}
	if records != 7 {
		t.Errorf("want 7 recording rules, got %d", records)
	}
	if alerts != 2 {
		t.Errorf("want 2 alerting rules (page+ticket), got %d", alerts)
	}
}

func TestBuildRulesValidYAMLAndThreshold(t *testing.T) {
	rf := BuildRules(sampleSpec(t), 30*budget.Day)
	out, err := yaml.Marshal(rf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// It must round-trip back into the promtool rule structure.
	var back RuleFile
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("rules are not valid YAML: %v", err)
	}
	if len(back.Groups) != 2 {
		t.Fatalf("round-trip lost groups: %d", len(back.Groups))
	}
	// The page alert for a 99.9%% SLO must trip at the 14.4x threshold.
	text := string(out)
	if !strings.Contains(text, "> 0.0144") {
		t.Errorf("expected 14.4x page threshold 0.0144 in rules:\n%s", text)
	}
	// Latency SLO (99%%) page threshold: 14.4 * 0.01 = 0.144.
	if !strings.Contains(text, "> 0.144") {
		t.Errorf("expected latency page threshold 0.144 in rules")
	}
	// Recording rules reference both windows an alert conjunction needs.
	if !strings.Contains(text, "slo:sli_error:ratio_rate1h") ||
		!strings.Contains(text, "slo:sli_error:ratio_rate5m") {
		t.Errorf("missing expected recording metric names")
	}
}

// TestUserLabelsCannotHijackTheSLOLabel is a regression test. The generated
// alert expressions select series by {slo="<name>"}, so a user label named
// "slo" used to relabel the recording rules out from under them, producing a
// promtool-valid ruleset whose alerts could never match anything.
func TestUserLabelsCannotHijackTheSLOLabel(t *testing.T) {
	s, err := spec.Parse([]byte(`
slos:
  - name: api
    objective: 99.9
    good: g[$window]
    total: t[$window]
    labels:
      slo: hijacked
      severity: none
      team: platform
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rf := BuildRules(s, 30*budget.Day)
	if len(rf.Groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(rf.Groups))
	}
	for _, r := range rf.Groups[0].Rules {
		if got := r.Labels["slo"]; got != "api" {
			t.Errorf("rule %q%q has slo=%q, want %q — alerts select on this label",
				r.Record, r.Alert, got, "api")
		}
		if r.Labels["team"] != "platform" {
			t.Errorf("user label team was dropped: %+v", r.Labels)
		}
		// The severity an alert rule carries is the one the table assigns,
		// not one a user label smuggled in.
		if r.Alert != "" && r.Labels["severity"] == "none" {
			t.Errorf("alert %q took its severity from a user label: %+v", r.Alert, r.Labels)
		}
	}
	// And the alert expressions must actually reference that label value.
	out, err := yaml.Marshal(rf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `{slo="api"}`) {
		t.Errorf("alert expressions do not select slo=\"api\":\n%s", out)
	}
}

func TestErrorRatioExprSubstitutesWindow(t *testing.T) {
	s := sampleSpec(t)
	expr := errorRatioExpr(s.SLOs[0], "5m")
	if strings.Contains(expr, "$window") {
		t.Errorf("window placeholder not substituted: %s", expr)
	}
	if !strings.Contains(expr, "[5m]") {
		t.Errorf("expected [5m] window in expr: %s", expr)
	}
}
