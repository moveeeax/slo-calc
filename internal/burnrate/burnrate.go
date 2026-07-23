// Package burnrate builds multi-window, multi-burn-rate alert definitions
// following the Google SRE workbook, and renders them as Prometheus rules.
package burnrate

import (
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
)

// Level describes one row of the multi-burn-rate table.
type Level struct {
	Severity string
	// Long and Short are the two windows the alert requires to fire
	// together (long window catches sustained burn, short window makes
	// the alert reset quickly once burning stops).
	Long  time.Duration
	Short time.Duration
	// Consumed is the fraction of the total error budget this row is
	// designed to trip on, over one SLO window.
	Consumed float64
}

// DefaultLevels is the canonical table for a 30-day SLO window. At 30d the
// burn-rate factors work out to the familiar 14.4, 6, 3 and 1.
var DefaultLevels = []Level{
	{Severity: "page", Long: time.Hour, Short: 5 * time.Minute, Consumed: 0.02},
	{Severity: "page", Long: 6 * time.Hour, Short: 30 * time.Minute, Consumed: 0.05},
	{Severity: "ticket", Long: budget.Day, Short: 2 * time.Hour, Consumed: 0.10},
	{Severity: "ticket", Long: 3 * budget.Day, Short: 6 * time.Hour, Consumed: 0.10},
}

// Alert is a fully-resolved burn-rate alert for one SLO.
type Alert struct {
	Severity string  `json:"severity"`
	Long     string  `json:"long_window"`
	Short    string  `json:"short_window"`
	Factor   float64 `json:"burn_rate"`
	// Threshold is the error-ratio value both windows must exceed. It is
	// Factor multiplied by the error budget fraction (1-objective).
	Threshold float64 `json:"threshold"`
}

// Factor is the burn-rate multiplier for a level given the SLO window.
// burnRate = consumed * sloWindow / longWindow, which reproduces the
// canonical 14.4/6/3/1 numbers when sloWindow is 30d.
func (l Level) Factor(sloWindow time.Duration) float64 {
	if l.Long <= 0 {
		return 0
	}
	return l.Consumed * float64(sloWindow) / float64(l.Long)
}

// Alerts resolves the default table for one SLO objective (a fraction such
// as 0.999) over the given window.
func Alerts(objective float64, sloWindow time.Duration) []Alert {
	errBudget := 1 - objective
	out := make([]Alert, 0, len(DefaultLevels))
	for _, l := range DefaultLevels {
		f := l.Factor(sloWindow)
		out = append(out, Alert{
			Severity:  l.Severity,
			Long:      shortDur(l.Long),
			Short:     shortDur(l.Short),
			Factor:    budget.Round(f, 4),
			Threshold: budget.Round(f*errBudget, 8),
		})
	}
	return out
}

// Windows returns the distinct rate windows referenced by the default table,
// which is exactly the set of recording rules a rules file needs.
func Windows() []time.Duration {
	seen := map[time.Duration]bool{}
	var out []time.Duration
	for _, l := range DefaultLevels {
		for _, w := range []time.Duration{l.Long, l.Short} {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

func shortDur(d time.Duration) string {
	switch {
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
