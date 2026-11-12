package accesslog

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// JSON writes the summary as indented JSON. Routes are cut to top when top
// is positive.
func (s Summary) JSON(w io.Writer, top int) error {
	s.Routes = cut(s.Routes, top)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// Table writes a human-readable summary with the top worst routes.
func (s Summary) Table(w io.Writer, top int) error {
	fmt.Fprintf(w, "analysed %d requests from %d lines (%d excluded, %d unparsable)", s.Requests, s.Lines, s.Excluded, s.Unparsable)
	if !s.First.IsZero() {
		fmt.Fprintf(w, ", %s to %s (%s)", s.First.UTC().Format(time.RFC3339), s.Last.UTC().Format(time.RFC3339), s.SpanS)
	} else {
		fmt.Fprint(w, ", no timestamps")
	}
	fmt.Fprint(w, "\n\n")

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "SLI\tOBJECTIVE\tACTUAL\tBURN\t%s BUDGET SPENT\tSTATUS\n", s.SLOWindow)
	writeSLI(tw, "availability", s.Availability)
	if s.Latency != nil {
		writeSLI(tw, "latency <= "+s.LatencyTarget, *s.Latency)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if s.Percentiles != nil {
		fmt.Fprintf(w, "\nlatency p50 %s  p90 %s  p99 %s (%d timed requests)\n",
			FormatDuration(s.Percentiles.P50), FormatDuration(s.Percentiles.P90), FormatDuration(s.Percentiles.P99), s.Timed)
	} else if s.Latency != nil {
		fmt.Fprintln(w, "\nno request durations in the logs: the latency SLI has no data")
	}

	routes := cut(s.Routes, top)
	if len(routes) == 0 {
		return nil
	}
	fmt.Fprintf(w, "\nworst routes (%d of %d)\n", len(routes), len(s.Routes))
	tw = tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "METHOD\tROUTE\tREQUESTS\t5XX\t5XX SHARE\tSLOW\tSLOW SHARE\tP50\tP90\tP99\n")
	for _, r := range routes {
		p50, p90, p99 := "-", "-", "-"
		if r.Latency != nil {
			p50, p90, p99 = FormatDuration(r.Latency.P50), FormatDuration(r.Latency.P90), FormatDuration(r.Latency.P99)
		}
		method := r.Method
		if method == "" {
			method = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%.1f%%\t%d\t%.1f%%\t%s\t%s\t%s\n",
			method, r.Route, r.Requests, r.Errors, r.ErrorShare*100, r.Slow, r.SlowShare*100, p50, p90, p99)
	}
	return tw.Flush()
}

func writeSLI(w io.Writer, label string, s SLI) {
	status := "OK"
	switch {
	case s.Total == 0:
		status = "NO DATA"
	case s.BurnRate > 1:
		// Faster than the pace that exactly spends the budget over the
		// SLO window: kept up, this traffic misses the objective.
		status = "BURNING"
	}
	fmt.Fprintf(w, "%s\t%.3f%%\t%.4f%%\t%.2fx\t%.1f%%\t%s\n",
		label, s.Objective*100, s.Availability*100, s.BurnRate, s.WindowSpent*100, status)
}

func cut(rs []RouteStat, top int) []RouteStat {
	if top > 0 && len(rs) > top {
		return rs[:top]
	}
	return rs
}

// FormatDuration renders a latency at millisecond precision, or in
// microseconds below a millisecond.
func FormatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return d.Round(time.Microsecond).String()
	}
	return d.Round(time.Millisecond).String()
}
