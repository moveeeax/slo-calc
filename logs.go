package main

import (
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/moveeeax/slo-calc/internal/accesslog"
	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/burnrate"
	"github.com/moveeeax/slo-calc/internal/spec"

	"go.yaml.in/yaml/v3"
)

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// runLogs implements "slo-calc logs": SLIs measured from access logs, plus
// the spec and rules that keep measuring them once metrics exist.
func runLogs(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("slo-calc logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		inputs, fields, routes, exPaths, exAgents stringList

		format     = fs.String("format", "combined", "log format: combined, common, envoy or json")
		unitS      = fs.String("duration-unit", "", "unit of a numeric duration: s, ms, us or ns (default: s, ms for envoy)")
		objective  = fs.Float64("objective", 99.9, "availability objective in percent")
		latObj     = fs.Float64("latency-objective", 0, "latency objective in percent (default: --objective)")
		latTargetS = fs.String("latency-target", "", "latency threshold such as 300ms; enables the latency SLI")
		windowS    = fs.String("window", "30d", "SLO window the objective is defined over")
		exCancel   = fs.Bool("exclude-cancelled", false, "drop client-cancelled requests (nginx 499, Envoy DC)")
		top        = fs.Int("top", 10, "number of worst routes to show (0 for all)")
		output     = fs.String("output", "table", "output format: table or json")
		emitSpec   = fs.String("emit-spec", "", "write an slo-calc spec for the measured SLIs to this file")
		emitRules  = fs.String("emit-rules", "", "write Prometheus recording and burn-rate alerting rules to this file")
		name       = fs.String("name", accesslog.DefaultMetrics.Name, "SLO name prefix for --emit-spec/--emit-rules")
		reqMetric  = fs.String("metric-requests", accesslog.DefaultMetrics.Requests, "request counter for the emitted SLIs")
		codeLabel  = fs.String("code-label", accesslog.DefaultMetrics.CodeLabel, "status code label of the request counter")
		durMetric  = fs.String("metric-duration", accesslog.DefaultMetrics.Duration, "request duration histogram (seconds) for the emitted SLIs")
		selector   = fs.String("selector", "", `label matchers added to every emitted series, e.g. job="api"`)
	)
	fs.Var(&inputs, "input", "access log file, - for stdin, .gz is decompressed (repeatable)")
	fs.Var(&fields, "field", "JSON field mapping name=key, e.g. status=resp.code (repeatable)")
	fs.Var(&routes, "route", "route template such as /orders/:id (repeatable)")
	fs.Var(&exPaths, "exclude-path", "regexp of request paths to drop, e.g. ^/healthz (repeatable)")
	fs.Var(&exAgents, "exclude-agent", "regexp of user agents to drop, e.g. (?i)bot|crawler (repeatable)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: slo-calc logs --input access.log [--format combined] --objective 99.9 [--latency-target 300ms] [--emit-rules rules.yaml]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (use --input)", fs.Arg(0))
	}
	if len(inputs) == 0 {
		fs.Usage()
		return fmt.Errorf("--input is required")
	}
	if *output != "table" && *output != "json" {
		return fmt.Errorf("unknown --output %q (want table or json)", *output)
	}

	unit, err := accesslog.ParseUnit(*unitS)
	if err != nil {
		return err
	}
	p, err := accesslog.NewParser(accesslog.Format(*format), unit)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if err := p.Fields.SetField(f); err != nil {
			return err
		}
	}

	window, err := budget.ParseDuration(*windowS)
	if err != nil {
		return err
	}
	if window <= 0 {
		return fmt.Errorf("--window must be positive, got %q", *windowS)
	}
	var target time.Duration
	if *latTargetS != "" {
		target, err = time.ParseDuration(*latTargetS)
		if err != nil {
			return fmt.Errorf("invalid --latency-target: %w", err)
		}
		if target <= 0 {
			return fmt.Errorf("--latency-target must be positive, got %q", *latTargetS)
		}
	}
	if *latObj == 0 {
		*latObj = *objective
	}
	opts := accesslog.Options{
		Objective:        *objective / 100,
		LatencyObjective: *latObj / 100,
		LatencyTarget:    target,
		SLOWindow:        window,
	}
	if err := opts.Validate(); err != nil {
		return err
	}

	router := &accesslog.Router{}
	for _, r := range routes {
		if err := router.AddTemplate(r); err != nil {
			return err
		}
	}
	var filter accesslog.Filter
	filter.ExcludeCancelled = *exCancel
	for _, list := range []struct {
		flag string
		in   stringList
		out  *[]*regexp.Regexp
	}{{"--exclude-path", exPaths, &filter.ExcludePaths}, {"--exclude-agent", exAgents, &filter.ExcludeAgents}} {
		for _, expr := range list.in {
			re, err := regexp.Compile(expr)
			if err != nil {
				return fmt.Errorf("invalid %s: %w", list.flag, err)
			}
			*list.out = append(*list.out, re)
		}
	}

	m := accesslog.DefaultMetrics
	m.Name, m.Requests, m.CodeLabel, m.Duration, m.Selector = *name, *reqMetric, *codeLabel, *durMetric, *selector
	if *emitSpec != "" || *emitRules != "" {
		// Checked before reading the logs, which can take a while.
		if err := m.Validate(); err != nil {
			return err
		}
	}

	agg := accesslog.NewAggregator(opts, router, filter)
	for _, in := range inputs {
		if err := readLog(agg, p, in); err != nil {
			return err
		}
	}
	sum := agg.Summary()
	if sum.Lines > 0 && sum.Unparsable == sum.Lines {
		return fmt.Errorf("none of the %d lines matched the %s format", sum.Lines, *format)
	}
	if target > 0 && sum.Timed == 0 && sum.Requests > 0 {
		fmt.Fprintln(stderr, "slo-calc: warning: no line carried a request duration, so the latency SLI is empty. "+
			"Log one (nginx $request_time, Apache %D with --duration-unit us) or map it with --field duration=...")
	}

	if *emitSpec != "" || *emitRules != "" {
		sp := accesslog.BuildSpec(m, opts.Objective, opts.LatencyObjective, target)
		if *emitSpec != "" {
			if err := writeYAML(*emitSpec, sp); err != nil {
				return err
			}
		}
		if *emitRules != "" {
			// Round-trip through the validator so the rules are built from
			// exactly what a later "slo-calc --spec" run would accept.
			raw, err := yaml.Marshal(sp)
			if err != nil {
				return err
			}
			checked, err := spec.Parse(raw)
			if err != nil {
				return fmt.Errorf("emitted spec is invalid: %w", err)
			}
			warnUnreachable(stderr, checked, window)
			if err := writeYAML(*emitRules, burnrate.BuildRules(checked, window)); err != nil {
				return err
			}
		}
	}

	if *output == "json" {
		return sum.JSON(stdout, *top)
	}
	return sum.Table(stdout, *top)
}

func readLog(agg *accesslog.Aggregator, p *accesslog.Parser, path string) error {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}
	if err := agg.ReadFrom(p, r); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeYAML(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		f.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
