// Package budget holds the pure error-budget arithmetic. It has no
// dependency on Prometheus or I/O so the maths can be unit-tested exactly.
package budget

import (
	"math"
	"time"
)

// Result is the computed state of a single SLO over one window.
type Result struct {
	Name string `json:"name"`
	// Objective is the target as a fraction, e.g. 0.999.
	Objective float64 `json:"objective"`
	// Window is the SLO window, e.g. 30d.
	Window string `json:"window"`
	// Good and Total are the raw event counts observed in the window.
	Good  float64 `json:"good"`
	Total float64 `json:"total"`
	// Availability is Good/Total as a fraction in [0,1].
	Availability float64 `json:"availability"`
	// ErrorBudget is the fraction of bad events the objective permits,
	// i.e. 1-Objective.
	ErrorBudget float64 `json:"error_budget"`
	// BudgetConsumed is the fraction of the error budget already spent.
	// 0 means the budget is untouched, 1 means fully exhausted, >1 means
	// the objective has been breached over the window.
	BudgetConsumed float64 `json:"budget_consumed"`
	// BudgetRemaining is 1-BudgetConsumed. It goes negative once the SLO
	// has been breached over the window.
	BudgetRemaining float64 `json:"budget_remaining"`
	// BurnRate is how fast the budget is being spent relative to the pace
	// that would exactly exhaust it over the window. A burn rate of 1
	// exhausts the budget precisely at the end of the window.
	BurnRate float64 `json:"burn_rate"`
}

// Compute derives the error-budget state for one SLO.
//
//	availability = good / total
//	errorRatio   = 1 - availability
//	errorBudget  = 1 - objective
//	consumed     = errorRatio / errorBudget
//	burnRate     = errorRatio / errorBudget   (== consumed over the full window)
//
// objective is a fraction in (0,1). When total is zero the SLO is treated
// as perfectly available, since no traffic means no observed errors.
func Compute(name string, objective, good, total float64, window time.Duration) Result {
	availability := 1.0
	if total > 0 {
		availability = good / total
	}
	if availability < 0 {
		availability = 0
	}
	if availability > 1 {
		availability = 1
	}
	errorRatio := 1 - availability
	errorBudget := 1 - objective
	consumed := 0.0
	burn := 0.0
	if errorBudget > 0 {
		consumed = errorRatio / errorBudget
		burn = errorRatio / errorBudget
	}
	return Result{
		Name:            name,
		Objective:       objective,
		Window:          shortDur(window),
		Good:            good,
		Total:           total,
		Availability:    availability,
		ErrorBudget:     errorBudget,
		BudgetConsumed:  consumed,
		BudgetRemaining: 1 - consumed,
		BurnRate:        burn,
	}
}

// Healthy reports whether the SLO still has error budget left.
func (r Result) Healthy() bool {
	return r.BudgetConsumed <= 1+1e-9
}

// shortDur renders a duration the way Prometheus windows are written,
// collapsing whole days and weeks.
func shortDur(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d%Week == 0 {
		return itoa(int(d/Week)) + "w"
	}
	if d%Day == 0 {
		return itoa(int(d/Day)) + "d"
	}
	if d%time.Hour == 0 {
		return itoa(int(d/time.Hour)) + "h"
	}
	if d%time.Minute == 0 {
		return itoa(int(d/time.Minute)) + "m"
	}
	return d.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Round rounds x to n decimal places. Handy for stable output.
func Round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}
