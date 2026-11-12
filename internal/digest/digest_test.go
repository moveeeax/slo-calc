package digest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/spec"
)

var (
	now  = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	prev = now.Add(-budget.Day)
)

// fakeQuerier answers by expression and instant; unknown keys are 0.
type fakeQuerier map[string]float64

func key(expr string, at time.Time) string { return fmt.Sprintf("%s@%d", expr, at.Unix()) }

func (f fakeQuerier) Query(_ context.Context, expr string, at time.Time) (float64, error) {
	return f[key(expr, at)], nil
}

func (f fakeQuerier) set(at time.Time, good, total string, g, t float64) {
	f[key(good, at)] = g
	f[key(total, at)] = t
}

func twoSLOs(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Parse([]byte(`
slos:
  - name: api
    objective: 99.9
    good: good[$window]
    total: total[$window]
  - name: web
    objective: 99
    good: wgood[$window]
    total: wtotal[$window]
`))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// healthy fills every window of both SLOs with error-free traffic.
func healthy() fakeQuerier {
	q := fakeQuerier{}
	for _, at := range []time.Time{now, prev} {
		for _, w := range []string{"30d", "5m", "30m", "1h", "6h"} {
			q.set(at, "good["+w+"]", "total["+w+"]", 1000, 1000)
			q.set(at, "wgood["+w+"]", "wtotal["+w+"]", 1000, 1000)
		}
	}
	return q
}

func opts() Options {
	return Options{Window: 30 * budget.Day, At: now, Compare: budget.Day, MovedPP: 2}
}

func TestQuietWhenHealthy(t *testing.T) {
	d, err := Evaluate(context.Background(), healthy(), twoSLOs(t), opts())
	if err != nil {
		t.Fatal(err)
	}
	if !d.Quiet() || d.Loud() || len(d.Burning()) != 0 {
		t.Errorf("healthy digest should be quiet: %+v", d.Items)
	}
	if !strings.Contains(d.Summary(), "all 2 SLOs healthy, nothing moved") {
		t.Errorf("summary:\n%s", d.Summary())
	}
	if d.Compare != "1d" || d.Window != "30d" {
		t.Errorf("compare/window = %q/%q", d.Compare, d.Window)
	}
}

func TestFastBurnTrips(t *testing.T) {
	q := healthy()
	// api: 2% errors over 1h and 5m = 20x a 0.1% budget, above the 14.4x
	// page row. 6h is at 0.5% = 5x, under the 6x row.
	q.set(now, "good[1h]", "total[1h]", 980, 1000)
	q.set(now, "good[5m]", "total[5m]", 980, 1000)
	q.set(now, "good[6h]", "total[6h]", 995, 1000)
	q.set(now, "good[30m]", "total[30m]", 980, 1000)
	d, err := Evaluate(context.Background(), q, twoSLOs(t), opts())
	if err != nil {
		t.Fatal(err)
	}
	api := d.Items[0]
	if !api.Tripped || len(api.FastBurn) != 2 || !api.FastBurn[0].Tripped || api.FastBurn[1].Tripped {
		t.Fatalf("fast burn rows: %+v", api.FastBurn)
	}
	var got []string
	for _, w := range api.Windows {
		got = append(got, fmt.Sprintf("%s=%g", w.Window, w.BurnRate))
	}
	if strings.Join(got, " ") != "5m=20 30m=20 1h=20 6h=5" {
		t.Errorf("burn windows: %v", got)
	}
	if d.Quiet() || !d.Loud() {
		t.Error("a tripped row must make the digest loud")
	}
	if b := d.Burning(); len(b) != 1 || b[0] != "api" {
		t.Errorf("burning = %v", b)
	}
	sum := d.Summary()
	if !strings.Contains(sum, "[FAST BURN] api") || !strings.Contains(sum, "1h/5m burn 20.0x/20.0x over 14.4x") {
		t.Errorf("summary:\n%s", sum)
	}
	if !strings.Contains(sum, "1 other SLO healthy and unchanged") {
		t.Errorf("summary should count the quiet SLO:\n%s", sum)
	}
	det := d.Detail()
	for _, want := range []string{"api (objective 99.9%)", "page 1h/5m over 14.4x: TRIPPED", "page 6h/30m over 6x: not tripped", "web (objective 99%)"} {
		if !strings.Contains(det, want) {
			t.Errorf("detail missing %q:\n%s", want, det)
		}
	}
}

func TestMovedNamesTheSLO(t *testing.T) {
	q := healthy()
	// web over 30d: 0.5% errors now (half its 1% budget), 0.2% a day ago.
	q.set(now, "wgood[30d]", "wtotal[30d]", 995, 1000)
	q.set(prev, "wgood[30d]", "wtotal[30d]", 998, 1000)
	d, err := Evaluate(context.Background(), q, twoSLOs(t), opts())
	if err != nil {
		t.Fatal(err)
	}
	web := d.Items[1]
	if !web.Moved || web.Delta != -30 || web.PreviousRemaining == nil {
		t.Fatalf("web should have moved -30pp: %+v", web)
	}
	if d.Items[0].Moved {
		t.Error("api did not move")
	}
	if d.Quiet() || d.Loud() || len(d.Burning()) != 0 {
		t.Error("a moved but healthy SLO is reported, not loud, not burning")
	}
	if !strings.Contains(d.Summary(), "[MOVED] web: budget left 50.0% (-30.0 pp over 1d)") {
		t.Errorf("summary:\n%s", d.Summary())
	}
	if !strings.Contains(d.Detail(), "1d ago: budget left 80.0%, now 50.0% (-30.0 pp)") {
		t.Errorf("detail:\n%s", d.Detail())
	}
}

func TestBreachedAndNoCompare(t *testing.T) {
	q := healthy()
	q.set(now, "good[30d]", "total[30d]", 990, 1000) // 10x the budget
	o := opts()
	o.Compare = 0
	d, err := Evaluate(context.Background(), q, twoSLOs(t), o)
	if err != nil {
		t.Fatal(err)
	}
	if d.Items[0].PreviousRemaining != nil || d.Compare != "" {
		t.Error("compare 0 should skip the previous evaluation")
	}
	if b := d.Burning(); len(b) != 1 || b[0] != "api" {
		t.Errorf("an exhausted budget is burning: %v", b)
	}
	if !strings.Contains(d.Summary(), "[BREACHED] api: budget left -900.0%") {
		t.Errorf("summary:\n%s", d.Summary())
	}
}

func TestUnreachableRowIsLabelled(t *testing.T) {
	s, err := spec.Parse([]byte("slos:\n  - name: loose\n    objective: 90\n    good: g[$window]\n    total: t[$window]\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Evaluate(context.Background(), fakeQuerier{}, s, opts())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Detail(), "page 1h/5m over 14.4x: can never fire") {
		t.Errorf("detail:\n%s", d.Detail())
	}
}
