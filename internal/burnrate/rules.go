package burnrate

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/moveeeax/slo-calc/internal/spec"
)

// Rule is one Prometheus recording or alerting rule.
type Rule struct {
	Record      string            `yaml:"record,omitempty"`
	Alert       string            `yaml:"alert,omitempty"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// RuleGroup is a named group of rules.
type RuleGroup struct {
	Name  string `yaml:"name"`
	Rules []Rule `yaml:"rules"`
}

// RuleFile is a Prometheus rules document (promtool-compatible).
type RuleFile struct {
	Groups []RuleGroup `yaml:"groups"`
}

const errorMetric = "slo:sli_error:ratio_rate"

// recordName is the recording-rule metric name for a rate window.
func recordName(w time.Duration) string { return errorMetric + shortDur(w) }

// BuildRules turns a spec into a full multi-burn-rate rules file: one group
// per SLO, holding the per-window error-ratio recording rules plus one
// alerting rule per severity.
func BuildRules(s *spec.Spec, sloWindow time.Duration) RuleFile {
	rf := RuleFile{}
	for i := range s.SLOs {
		slo := s.SLOs[i]
		g := RuleGroup{Name: "slo:" + slo.Name}

		// Recording rules: error ratio at every window the table needs.
		for _, w := range Windows() {
			g.Rules = append(g.Rules, Rule{
				Record: recordName(w),
				Expr:   errorRatioExpr(slo, shortDur(w)),
				Labels: withSLO(slo, nil),
			})
		}

		// Alerting rules: one per severity, ORing that severity's rows.
		objective := slo.Objective / 100
		for _, sev := range []string{"page", "ticket"} {
			var terms []string
			for _, a := range Alerts(objective, sloWindow) {
				if a.Severity != sev {
					continue
				}
				terms = append(terms, burnTerm(slo.Name, a))
			}
			if len(terms) == 0 {
				continue
			}
			g.Rules = append(g.Rules, Rule{
				Alert:  "SLOBurn" + title(sev),
				Expr:   strings.Join(terms, "\nor\n"),
				Labels: withSLO(slo, map[string]string{"severity": sev}),
				Annotations: map[string]string{
					"summary": fmt.Sprintf("SLO %s is burning its %g%% error budget too fast (%s)", slo.Name, slo.Objective, sev),
				},
			})
		}
		rf.Groups = append(rf.Groups, g)
	}
	return rf
}

// errorRatioExpr renders (total-good)/total for a given rate window.
func errorRatioExpr(slo spec.SLO, window string) string {
	total := "(" + spec.Render(slo.Total, window) + ")"
	good := "(" + spec.Render(slo.Good, window) + ")"
	return fmt.Sprintf("(%s - %s)\n/\n%s", total, good, total)
}

// burnTerm renders one long+short window conjunction for an alert.
func burnTerm(name string, a Alert) string {
	th := formatFloat(a.Threshold)
	long := fmt.Sprintf("%s%s{slo=%q} > %s", errorMetric, a.Long, name, th)
	short := fmt.Sprintf("%s%s{slo=%q} > %s", errorMetric, a.Short, name, th)
	return fmt.Sprintf("(\n  %s\n  and\n  %s\n)", long, short)
}

func withSLO(slo spec.SLO, extra map[string]string) map[string]string {
	m := map[string]string{"slo": slo.Name}
	for k, v := range slo.Labels {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// formatFloat renders a float in plain decimal notation (never 1e-4), which
// PromQL requires for literal thresholds.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// title upper-cases the first byte of an ASCII word.
func title(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}
