package accesslog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
)

// maxLineBytes bounds one log line. Lines longer than this (a megabyte of
// cookie header) are counted as unparsable rather than aborting the run.
const maxLineBytes = 1 << 20

// Filter decides which parsed requests count towards the SLI.
type Filter struct {
	// ExcludePaths drops requests whose raw path matches any expression:
	// health checks, metrics scrapes, readiness probes.
	ExcludePaths []*regexp.Regexp
	// ExcludeAgents drops requests whose user agent matches: crawlers,
	// uptime checkers, load balancer probes.
	ExcludeAgents []*regexp.Regexp
	// ExcludeCancelled drops requests the client abandoned (nginx 499,
	// Envoy DC). They are neither a server failure nor a served response.
	ExcludeCancelled bool
}

func (f Filter) excluded(r Record) bool {
	if f.ExcludeCancelled && r.Cancelled {
		return true
	}
	for _, re := range f.ExcludePaths {
		if re.MatchString(r.Path) {
			return true
		}
	}
	for _, re := range f.ExcludeAgents {
		if re.MatchString(r.UserAgent) {
			return true
		}
	}
	return false
}

// Options configure an analysis.
type Options struct {
	// Objective is the target as a fraction, e.g. 0.999. LatencyObjective
	// is the same for the latency SLI.
	Objective        float64
	LatencyObjective float64
	// LatencyTarget is the "fast enough" threshold. Zero disables the
	// latency SLI.
	LatencyTarget time.Duration
	// SLOWindow is the window the objective is defined over (30d). The
	// share of that window's budget spent during the analysed span is
	// reported next to the burn rate.
	SLOWindow time.Duration
}

// Bad reports whether a response counts against availability: any 5xx,
// and Envoy's status 0 (no response was ever sent) unless the client was
// the one who hung up.
func Bad(r Record) bool {
	if r.Status == 0 {
		return !r.Cancelled
	}
	return r.Status >= 500
}

// Aggregator accumulates records one at a time.
type Aggregator struct {
	opts   Options
	router *Router
	filter Filter

	stats Stats
	// all holds every observed duration for the overall percentiles.
	all    []time.Duration
	routes map[routeKey]*routeAcc
}

type routeKey struct{ method, route string }

type routeAcc struct {
	requests, errors, slow, timed int
	durations                     []time.Duration
}

// Stats are the line and request counters of one run.
type Stats struct {
	Lines      int `json:"lines"`
	Unparsable int `json:"unparsable"`
	Excluded   int `json:"excluded"`
	Requests   int `json:"requests"`
	Errors     int `json:"errors"`
	// Timed is the number of requests that carried a duration; only those
	// feed the latency SLI.
	Timed int `json:"timed"`
	Slow  int `json:"slow"`
	// First and Last bound the timestamps seen, zero when no line had one.
	First time.Time `json:"first,omitzero"`
	Last  time.Time `json:"last,omitzero"`
}

// NewAggregator builds an aggregator. A nil router normalises paths with
// the automatic rules only.
func NewAggregator(opts Options, router *Router, filter Filter) *Aggregator {
	if router == nil {
		router = &Router{}
	}
	return &Aggregator{opts: opts, router: router, filter: filter, routes: map[routeKey]*routeAcc{}}
}

// ReadFrom parses every line of r. Unparsable lines are counted, not fatal:
// real logs carry the odd truncated or binary line.
func (a *Aggregator) ReadFrom(p *Parser, r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for {
		if !sc.Scan() {
			if err := sc.Err(); err == bufio.ErrTooLong {
				// Skip the rest of the oversize line and carry on.
				a.stats.Lines++
				a.stats.Unparsable++
				if err := skipLine(r); err != nil {
					return err
				}
				sc = bufio.NewScanner(r)
				sc.Buffer(make([]byte, 64<<10), maxLineBytes)
				continue
			} else if err != nil {
				return err
			}
			return nil
		}
		line := sc.Text()
		if line == "" {
			continue
		}
		a.stats.Lines++
		rec, err := p.Parse(line)
		if err != nil {
			a.stats.Unparsable++
			continue
		}
		a.Add(rec)
	}
}

// skipLine discards input up to and including the next newline. bufio's
// Scanner gives up on a line longer than its buffer, and the reader is
// positioned somewhere inside that line afterwards.
func skipLine(r io.Reader) error {
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n == 1 && b[0] == '\n' {
			return nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Add counts one parsed record.
func (a *Aggregator) Add(r Record) {
	if a.filter.excluded(r) {
		a.stats.Excluded++
		return
	}
	s := &a.stats
	s.Requests++
	if r.HasTime {
		if s.First.IsZero() || r.Time.Before(s.First) {
			s.First = r.Time
		}
		if r.Time.After(s.Last) {
			s.Last = r.Time
		}
	}
	key := routeKey{method: r.Method, route: a.router.Route(r.Path)}
	acc := a.routes[key]
	if acc == nil {
		acc = &routeAcc{}
		a.routes[key] = acc
	}
	acc.requests++
	if Bad(r) {
		s.Errors++
		acc.errors++
	}
	if r.HasDuration {
		s.Timed++
		acc.timed++
		a.all = append(a.all, r.Duration)
		acc.durations = append(acc.durations, r.Duration)
		if a.opts.LatencyTarget > 0 && r.Duration > a.opts.LatencyTarget {
			s.Slow++
			acc.slow++
		}
	}
}

// Percentiles are latency quantiles, nearest-rank.
type Percentiles struct {
	P50 time.Duration
	P90 time.Duration
	P99 time.Duration
}

// MarshalJSON writes the quantiles in milliseconds; a raw time.Duration
// would come out as an integer count of nanoseconds.
func (p Percentiles) MarshalJSON() ([]byte, error) {
	ms := func(d time.Duration) float64 { return budget.Round(float64(d)/float64(time.Millisecond), 3) }
	return json.Marshal(struct {
		P50 float64 `json:"p50_ms"`
		P90 float64 `json:"p90_ms"`
		P99 float64 `json:"p99_ms"`
	}{ms(p.P50), ms(p.P90), ms(p.P99)})
}

// RouteStat is one method+route row.
type RouteStat struct {
	Method   string `json:"method"`
	Route    string `json:"route"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
	Slow     int    `json:"slow"`
	// Timed counts requests with a duration; Latency is nil without any.
	Timed   int          `json:"timed"`
	Latency *Percentiles `json:"latency,omitempty"`
	// ErrorShare is this route's part of all failed requests, which is the
	// same as its part of the availability budget spent.
	ErrorShare float64 `json:"error_share"`
	// SlowShare is the same for the latency budget.
	SlowShare float64 `json:"slow_share"`
}

// SLI is one computed indicator over the analysed span.
type SLI struct {
	budget.Result
	// WindowSpent is the share of a full SLO window's budget used up during
	// the analysed span: burn rate * span / SLO window. It assumes the
	// traffic in the logs is representative of the whole window.
	WindowSpent float64 `json:"window_budget_spent"`
}

// Summary is the outcome of an analysis.
type Summary struct {
	Stats
	// Span is Last-First, zero when the logs carry no timestamps.
	Span          time.Duration `json:"-"`
	SpanS         string        `json:"span"`
	SLOWindow     string        `json:"slo_window"`
	LatencyTarget string        `json:"latency_target,omitempty"`
	Availability  SLI           `json:"availability"`
	Latency       *SLI          `json:"latency,omitempty"`
	Percentiles   *Percentiles  `json:"percentiles,omitempty"`
	// Routes is sorted worst first: by failed requests, then slow
	// requests, then traffic.
	Routes []RouteStat `json:"routes"`
}

// Summary computes the SLIs and per-route breakdown from what has been
// added so far.
func (a *Aggregator) Summary() Summary {
	s := a.stats
	sum := Summary{Stats: s, SLOWindow: budget.FormatWindow(a.opts.SLOWindow)}
	if !s.First.IsZero() {
		sum.Span = s.Last.Sub(s.First)
	}
	sum.SpanS = budget.FormatWindow(sum.Span.Truncate(time.Second))

	sum.Availability = a.sli("availability", a.opts.Objective, float64(s.Requests-s.Errors), float64(s.Requests), sum.Span)
	if a.opts.LatencyTarget > 0 {
		sum.LatencyTarget = a.opts.LatencyTarget.String()
		lat := a.sli("latency", a.opts.LatencyObjective, float64(s.Timed-s.Slow), float64(s.Timed), sum.Span)
		sum.Latency = &lat
	}
	if len(a.all) > 0 {
		p := percentiles(a.all)
		sum.Percentiles = &p
	}

	for k, acc := range a.routes {
		rs := RouteStat{
			Method: k.method, Route: k.route,
			Requests: acc.requests, Errors: acc.errors, Slow: acc.slow, Timed: acc.timed,
		}
		if len(acc.durations) > 0 {
			p := percentiles(acc.durations)
			rs.Latency = &p
		}
		if s.Errors > 0 {
			rs.ErrorShare = float64(acc.errors) / float64(s.Errors)
		}
		if s.Slow > 0 {
			rs.SlowShare = float64(acc.slow) / float64(s.Slow)
		}
		sum.Routes = append(sum.Routes, rs)
	}
	sort.Slice(sum.Routes, func(i, j int) bool {
		x, y := sum.Routes[i], sum.Routes[j]
		if x.Errors != y.Errors {
			return x.Errors > y.Errors
		}
		if x.Slow != y.Slow {
			return x.Slow > y.Slow
		}
		if x.Requests != y.Requests {
			return x.Requests > y.Requests
		}
		if x.Route != y.Route {
			return x.Route < y.Route
		}
		return x.Method < y.Method
	})
	return sum
}

func (a *Aggregator) sli(name string, objective, good, total float64, span time.Duration) SLI {
	r := budget.Compute(name, objective, good, total, span)
	out := SLI{Result: r}
	if a.opts.SLOWindow > 0 {
		out.WindowSpent = budget.Round(r.BurnRate*float64(span)/float64(a.opts.SLOWindow), 6)
	}
	return out
}

// percentiles sorts ds in place and returns nearest-rank quantiles.
func percentiles(ds []time.Duration) Percentiles {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return Percentiles{P50: rank(ds, 0.50), P90: rank(ds, 0.90), P99: rank(ds, 0.99)}
}

// rank is the nearest-rank percentile of a sorted slice: the smallest value
// with at least q of the samples at or below it.
func rank(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

// Validate checks options before any input is read.
func (o Options) Validate() error {
	for _, v := range []struct {
		name string
		val  float64
	}{{"objective", o.Objective}, {"latency objective", o.LatencyObjective}} {
		if math.IsNaN(v.val) || v.val <= 0 || v.val >= 1 {
			return fmt.Errorf("%s must be in (0,100), got %v", v.name, v.val*100)
		}
	}
	if o.LatencyTarget < 0 {
		return fmt.Errorf("latency target must not be negative")
	}
	return nil
}
