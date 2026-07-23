// Package report evaluates a spec against Prometheus and renders the result
// as a table or JSON.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/burnrate"
	"github.com/moveeeax/slo-calc/internal/prom"
	"github.com/moveeeax/slo-calc/internal/spec"
)

// Entry is one SLO's evaluated budget plus its burn-rate alert thresholds.
type Entry struct {
	budget.Result
	Alerts []burnrate.Alert `json:"alerts"`
}

// Report is the full evaluation of a spec at an instant.
type Report struct {
	At      time.Time `json:"at"`
	Window  string    `json:"window"`
	Entries []Entry   `json:"slos"`
}

// Evaluate queries each SLO's good/total over the window and computes its
// error budget. The $window placeholder is substituted with the SLO window.
func Evaluate(ctx context.Context, q prom.Querier, s *spec.Spec, window time.Duration, at time.Time) (*Report, error) {
	win := shortDur(window)
	rep := &Report{At: at, Window: win}
	for i := range s.SLOs {
		slo := s.SLOs[i]
		good, err := q.Query(ctx, spec.Render(slo.Good, win), at)
		if err != nil {
			return nil, fmt.Errorf("slo %q good query: %w", slo.Name, err)
		}
		total, err := q.Query(ctx, spec.Render(slo.Total, win), at)
		if err != nil {
			return nil, fmt.Errorf("slo %q total query: %w", slo.Name, err)
		}
		res := budget.Compute(slo.Name, slo.Objective/100, good, total, window)
		rep.Entries = append(rep.Entries, Entry{
			Result: res,
			Alerts: burnrate.Alerts(slo.Objective/100, window),
		})
	}
	return rep, nil
}

// JSON writes the report as indented JSON.
func (r *Report) JSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Table writes a human-readable summary.
func (r *Report) Table(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "SLO\tOBJECTIVE\tAVAIL\tBUDGET LEFT\tBURN\tSTATUS\n")
	for _, e := range r.Entries {
		status := "OK"
		if !e.Healthy() {
			status = "BREACHED"
		}
		fmt.Fprintf(tw, "%s\t%.3f%%\t%.4f%%\t%.1f%%\t%.2fx\t%s\n",
			e.Name,
			e.Objective*100,
			e.Availability*100,
			snap(e.BudgetRemaining*100),
			e.BurnRate,
			status,
		)
	}
	return tw.Flush()
}

// snap collapses tiny floating-point residues to a clean zero so the table
// never prints "-0.0%".
func snap(v float64) float64 {
	if v < 1e-6 && v > -1e-6 {
		return 0
	}
	return v
}

func shortDur(d time.Duration) string {
	switch {
	case d%budget.Week == 0:
		return itoa(int(d/budget.Week)) + "w"
	case d%budget.Day == 0:
		return itoa(int(d/budget.Day)) + "d"
	case d%time.Hour == 0:
		return itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return itoa(int(d/time.Minute)) + "m"
	default:
		return d.String()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
