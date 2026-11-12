// Package digest builds a periodic SLO burn report meant to be read as a
// chat message: budget left, multi-window burn rate, and which SLO moved
// since the previous evaluation.
package digest

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/burnrate"
	"github.com/moveeeax/slo-calc/internal/prom"
	"github.com/moveeeax/slo-calc/internal/report"
	"github.com/moveeeax/slo-calc/internal/spec"
)

// Window is the error ratio and burn rate over one short rate window.
type Window struct {
	Window     string  `json:"window"`
	ErrorRatio float64 `json:"error_ratio"`
	BurnRate   float64 `json:"burn_rate"`
}

// Row is one fast-burn (page) row of the multi-burn-rate table, evaluated.
type Row struct {
	burnrate.Alert
	// Tripped is true when both the long and the short window are above
	// the threshold, which is exactly when the generated SLOBurnPage rule
	// fires.
	Tripped bool `json:"tripped"`
}

// Item is one SLO in the digest.
type Item struct {
	budget.Result
	Windows []Window `json:"burn_windows"`
	// FastBurn holds the page rows (1h/5m and 6h/30m at a 30d window).
	FastBurn []Row `json:"fast_burn"`
	// Tripped is true when any fast-burn row is tripped.
	Tripped bool `json:"tripped"`
	// PreviousRemaining is BudgetRemaining at At-Compare; nil when the
	// comparison is off.
	PreviousRemaining *float64 `json:"previous_budget_remaining,omitempty"`
	// Delta is the change in budget remaining, in percentage points of
	// the budget. Negative means budget was spent.
	Delta float64 `json:"budget_remaining_delta_pp"`
	// Moved is true when |Delta| reached the movement threshold.
	Moved bool `json:"moved"`
}

// Notable reports whether the SLO belongs in the compact digest rather than
// only in the detail.
func (it Item) Notable() bool {
	return it.Tripped || !it.Healthy() || it.Moved
}

// Digest is one evaluation of a spec.
type Digest struct {
	At      time.Time `json:"at"`
	Window  string    `json:"window"`
	Compare string    `json:"compare,omitempty"`
	Items   []Item    `json:"slos"`
}

// Options control an evaluation.
type Options struct {
	// Window is the SLO window.
	Window time.Duration
	// At is the evaluation instant.
	At time.Time
	// Compare is how far back the previous evaluation is taken from; zero
	// turns the comparison off.
	Compare time.Duration
	// MovedPP is the change in budget remaining, in percentage points,
	// from which an SLO counts as moved.
	MovedPP float64
}

// Evaluate computes the digest. It runs the regular budget report at At
// (and at At-Compare), plus good/total queries over each page-row window.
func Evaluate(ctx context.Context, q prom.Querier, s *spec.Spec, o Options) (*Digest, error) {
	now, err := report.Evaluate(ctx, q, s, o.Window, o.At)
	if err != nil {
		return nil, err
	}
	var prev *report.Report
	if o.Compare > 0 {
		prev, err = report.Evaluate(ctx, q, s, o.Window, o.At.Add(-o.Compare))
		if err != nil {
			return nil, fmt.Errorf("previous evaluation: %w", err)
		}
	}

	d := &Digest{At: o.At, Window: now.Window}
	if o.Compare > 0 {
		d.Compare = budget.FormatWindow(o.Compare)
	}
	for i, e := range now.Entries {
		slo := s.SLOs[i]
		it := Item{Result: e.Result}

		ratios := map[string]float64{}
		for _, w := range fastWindows(e.Alerts) {
			ratio, err := errorRatio(ctx, q, slo, w, o.At)
			if err != nil {
				return nil, err
			}
			ratios[w] = ratio
			burn := 0.0
			if e.ErrorBudget > 0 {
				burn = ratio / e.ErrorBudget
			}
			it.Windows = append(it.Windows, Window{Window: w, ErrorRatio: budget.Round(ratio, 8), BurnRate: budget.Round(burn, 4)})
		}
		for _, a := range e.Alerts {
			if a.Severity != "page" {
				continue
			}
			row := Row{Alert: a, Tripped: ratios[a.Long] > a.Threshold && ratios[a.Short] > a.Threshold}
			it.FastBurn = append(it.FastBurn, row)
			it.Tripped = it.Tripped || row.Tripped
		}

		if prev != nil {
			p := prev.Entries[i].BudgetRemaining
			it.PreviousRemaining = &p
			it.Delta = budget.Round((e.BudgetRemaining-p)*100, 4)
			it.Moved = math.Abs(it.Delta) >= o.MovedPP
		}
		d.Items = append(d.Items, it)
	}
	return d, nil
}

// fastWindows lists the page rows' windows, shortest first.
func fastWindows(alerts []burnrate.Alert) []string {
	var out []string
	seen := map[string]bool{}
	add := func(w string) {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, a := range alerts {
		if a.Severity == "page" {
			add(a.Short)
		}
	}
	for _, a := range alerts {
		if a.Severity == "page" {
			add(a.Long)
		}
	}
	return out
}

// errorRatio is (total-good)/total over window at the instant, clamped to
// [0,1]. No traffic counts as no errors, as in the main report.
func errorRatio(ctx context.Context, q prom.Querier, slo spec.SLO, window string, at time.Time) (float64, error) {
	good, err := q.Query(ctx, spec.Render(slo.Good, window), at)
	if err != nil {
		return 0, fmt.Errorf("slo %q good query over %s: %w", slo.Name, window, err)
	}
	total, err := q.Query(ctx, spec.Render(slo.Total, window), at)
	if err != nil {
		return 0, fmt.Errorf("slo %q total query over %s: %w", slo.Name, window, err)
	}
	if total <= 0 {
		return 0, nil
	}
	r := 1 - good/total
	switch {
	case math.IsNaN(r) || r < 0:
		return 0, nil
	case r > 1:
		return 1, nil
	}
	return r, nil
}

// Quiet is true when every budget is healthy, no fast-burn row is tripped
// and nothing moved: the case where a message would only be noise.
func (d *Digest) Quiet() bool {
	for _, it := range d.Items {
		if it.Notable() {
			return false
		}
	}
	return true
}

// Loud is true when any fast-burn row is tripped. Deliverers use it to
// decide whether the message should notify.
func (d *Digest) Loud() bool {
	for _, it := range d.Items {
		if it.Tripped {
			return true
		}
	}
	return false
}

// Burning lists SLOs whose fast-burn row is tripped or whose budget is
// exhausted: the ones a release gate should refuse to ship on.
func (d *Digest) Burning() []string {
	var out []string
	for _, it := range d.Items {
		if it.Tripped || !it.Healthy() {
			out = append(out, it.Name)
		}
	}
	return out
}

// Summary is the compact message: a headline and one line per SLO that
// tripped, breached or moved.
func (d *Digest) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "SLO digest at %s, %s window", d.At.UTC().Format("2006-01-02 15:04 MST"), d.Window)
	if d.Compare != "" {
		fmt.Fprintf(&b, ", change over the last %s", d.Compare)
	}
	b.WriteString("\n")
	quiet := 0
	for _, it := range d.Items {
		if !it.Notable() {
			quiet++
			continue
		}
		b.WriteString(it.line(d.Compare))
		b.WriteString("\n")
	}
	switch {
	case quiet == 1 && len(d.Items) == 1:
		b.WriteString("the SLO is healthy, nothing moved\n")
	case quiet == len(d.Items):
		fmt.Fprintf(&b, "all %d SLOs healthy, nothing moved\n", quiet)
	case quiet == 1:
		b.WriteString("1 other SLO healthy and unchanged\n")
	case quiet > 1:
		fmt.Fprintf(&b, "%d other SLOs healthy and unchanged\n", quiet)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (it Item) line(compare string) string {
	var tag string
	switch {
	case it.Tripped:
		tag = "FAST BURN"
	case !it.Healthy():
		tag = "BREACHED"
	default:
		tag = "MOVED"
	}
	s := fmt.Sprintf("[%s] %s: budget left %s", tag, it.Name, pctStr(it.BudgetRemaining))
	if it.PreviousRemaining != nil {
		s += fmt.Sprintf(" (%s over %s)", ppStr(it.Delta), compare)
	}
	for _, r := range it.FastBurn {
		if r.Tripped {
			s += fmt.Sprintf(", %s/%s burn %s/%s over %gx", r.Long, r.Short,
				burnOf(it.Windows, r.Long), burnOf(it.Windows, r.Short), r.Factor)
		}
	}
	return s
}

// Detail is the per-SLO breakdown, meant for a thread under the summary.
func (d *Digest) Detail() string {
	var b strings.Builder
	for i, it := range d.Items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s (objective %g%%)\n", it.Name, budget.Round(it.Objective*100, 6))
		fmt.Fprintf(&b, "  availability %.4f%% over %s, budget left %s (%s of %s), burn %.2fx\n",
			it.Availability*100, it.Window, pctStr(it.BudgetRemaining),
			report.FormatMinutes(it.BudgetRemainingMinutes), report.FormatMinutes(it.ErrorBudgetMinutes), it.BurnRate)
		if it.PreviousRemaining != nil {
			fmt.Fprintf(&b, "  %s ago: budget left %s, now %s (%s)\n",
				d.Compare, pctStr(*it.PreviousRemaining), pctStr(it.BudgetRemaining), ppStr(it.Delta))
		}
		var ws []string
		for _, w := range it.Windows {
			ws = append(ws, fmt.Sprintf("%s %.2fx", w.Window, w.BurnRate))
		}
		fmt.Fprintf(&b, "  burn rate %s\n", strings.Join(ws, ", "))
		for _, r := range it.FastBurn {
			state := "not tripped"
			switch {
			case r.Tripped:
				state = "TRIPPED"
			case !r.Reachable:
				state = "can never fire (threshold above an error ratio of 1)"
			}
			fmt.Fprintf(&b, "  page %s/%s over %gx: %s\n", r.Long, r.Short, r.Factor, state)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func burnOf(ws []Window, w string) string {
	for _, x := range ws {
		if x.Window == w {
			return fmt.Sprintf("%.1fx", x.BurnRate)
		}
	}
	return "?"
}

func pctStr(f float64) string {
	v := f * 100
	if v > -0.05 && v < 0.05 {
		v = 0
	}
	return fmt.Sprintf("%.1f%%", v)
}

func ppStr(pp float64) string {
	if pp > -0.05 && pp < 0.05 {
		return "no change"
	}
	return fmt.Sprintf("%+.1f pp", pp)
}
