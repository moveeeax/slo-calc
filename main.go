// Command slo-calc turns raw SLIs into SLOs, error budgets, burn rates and
// Prometheus multi-burn-rate alerting rules.
package main

import (
	"context"
	"flag"
	"fmt"
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

func run(args []string, stdout, stderr *os.File) error {
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
	sp, err := spec.Load(*specPath)
	if err != nil {
		return err
	}

	switch *output {
	case "rules":
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
