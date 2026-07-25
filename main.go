// Command slo-calc turns raw SLIs into SLOs, error budgets, burn rates and
// Prometheus multi-burn-rate alerting rules.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/burnrate"
	"github.com/moveeeax/slo-calc/internal/prom"
	"github.com/moveeeax/slo-calc/internal/report"
	"github.com/moveeeax/slo-calc/internal/spec"

	"gopkg.in/yaml.v3"
)

var version = "v0.1.0"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "slo-calc:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("slo-calc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		specPath = fs.String("spec", "", "path to the SLO spec YAML (required)")
		windowS  = fs.String("window", "30d", "SLO window, e.g. 30d, 7d, 24h")
		output   = fs.String("output", "table", "output format: table, json or rules")
		promURL  = fs.String("prometheus", "", "Prometheus base URL (required for table/json)")
		atS      = fs.String("at", "", "evaluate at this RFC3339 instant for backfill (default: now)")
		showVer  = fs.Bool("version", false, "print version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "slo-calc %s\n\nUsage: slo-calc --spec slos.yaml --window 30d [--output table|json|rules]\n\n", version)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVer {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if *specPath == "" {
		fs.Usage()
		return fmt.Errorf("--spec is required")
	}

	window, err := budget.ParseDuration(*windowS)
	if err != nil {
		return err
	}
	if window <= 0 {
		// A zero-length window divides by zero in every downstream
		// formula and renders as an invalid PromQL range selector.
		return fmt.Errorf("--window must be positive, got %q", *windowS)
	}
	sp, err := spec.Load(*specPath)
	if err != nil {
		return err
	}

	switch *output {
	case "rules":
		warnUnreachable(stderr, sp, window)
		rf := burnrate.BuildRules(sp, window)
		enc := yaml.NewEncoder(stdout)
		enc.SetIndent(2)
		defer enc.Close()
		return enc.Encode(rf)

	case "table", "json":
		if *promURL == "" {
			return fmt.Errorf("--prometheus is required for %s output", *output)
		}
		at := time.Now()
		if *atS != "" {
			at, err = time.Parse(time.RFC3339, *atS)
			if err != nil {
				return fmt.Errorf("invalid --at: %w", err)
			}
		}
		client := prom.New(*promURL)
		rep, err := report.Evaluate(context.Background(), client, sp, window, at)
		if err != nil {
			return err
		}
		if *output == "json" {
			return rep.JSON(stdout)
		}
		return rep.Table(stdout)

	default:
		return fmt.Errorf("unknown --output %q (want table, json or rules)", *output)
	}
}

// warnUnreachable flags burn-rate rows whose threshold has been pushed to or
// past an error ratio of 1, which no series can ever exceed. The rules file
// still validates and still deploys — it just silently never alerts — so the
// only way a user finds out is if we tell them.
func warnUnreachable(stderr io.Writer, sp *spec.Spec, window time.Duration) {
	for _, slo := range sp.SLOs {
		for _, a := range burnrate.Alerts(slo.Objective/100, window) {
			if a.Reachable {
				continue
			}
			fmt.Fprintf(stderr,
				"slo-calc: warning: slo %q (%g%%): the %s row %s/%s needs an error ratio above %g, "+
					"but a ratio cannot exceed 1 — this alert can never fire. "+
					"Tighten the objective or lengthen the SLO window.\n",
				slo.Name, slo.Objective, a.Severity, a.Long, a.Short, a.Threshold)
		}
	}
}
