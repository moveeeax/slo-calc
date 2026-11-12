package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/moveeeax/slo-calc/internal/budget"
	"github.com/moveeeax/slo-calc/internal/digest"
	"github.com/moveeeax/slo-calc/internal/prom"
	"github.com/moveeeax/slo-calc/internal/spec"
)

// getenv is swapped in tests.
var getenv = os.Getenv

// runDigest implements "slo-calc digest": a one-shot burn report for chat,
// meant to be run from cron, a Kubernetes CronJob or a CI schedule.
func runDigest(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("slo-calc digest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		specPath   = fs.String("spec", "", "path to the SLO spec YAML (required)")
		windowS    = fs.String("window", "30d", "SLO window")
		promURL    = fs.String("prometheus", "", "Prometheus base URL (required)")
		atS        = fs.String("at", "", "evaluate at this RFC3339 instant (default: now)")
		compareS   = fs.String("compare", "1d", "compare budget left with this long ago; 0 turns it off")
		moved      = fs.Float64("moved", 2, "change in budget left, in percentage points, that counts as moved")
		deliver    = fs.String("deliver", "stdout", "where to send the digest: stdout, slack or telegram")
		slackChan  = fs.String("slack-channel", "", "Slack channel ID for SLACK_BOT_TOKEN delivery (threads the detail)")
		chatID     = fs.String("chat-id", "", "Telegram chat ID (default: $TELEGRAM_CHAT_ID)")
		always     = fs.Bool("always", false, "deliver even when every budget is healthy and nothing moved")
		failOnBurn = fs.Bool("fail-on-burn", false, "exit with status 2 when a fast-burn row is tripped or a budget is exhausted")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: slo-calc digest --spec slos.yaml --prometheus URL [--deliver stdout|slack|telegram] [--fail-on-burn]\n\n"+
			"Secrets come from the environment: SLACK_WEBHOOK_URL or SLACK_BOT_TOKEN, TELEGRAM_BOT_TOKEN.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *specPath == "" {
		fs.Usage()
		return fmt.Errorf("--spec is required")
	}
	if *promURL == "" {
		return fmt.Errorf("--prometheus is required")
	}
	window, err := budget.ParseDuration(*windowS)
	if err != nil {
		return err
	}
	if window <= 0 {
		return fmt.Errorf("--window must be positive, got %q", *windowS)
	}
	var compare time.Duration
	if *compareS != "0" && *compareS != "" {
		if compare, err = budget.ParseDuration(*compareS); err != nil {
			return fmt.Errorf("invalid --compare: %w", err)
		}
	}
	if *moved < 0 {
		return fmt.Errorf("--moved must not be negative")
	}
	at := time.Now()
	if *atS != "" {
		if at, err = time.Parse(time.RFC3339, *atS); err != nil {
			return fmt.Errorf("invalid --at: %w", err)
		}
	}
	// The deliverer is resolved before querying Prometheus, so a missing
	// token fails fast instead of after a round of queries.
	d, err := deliverer(*deliver, *slackChan, *chatID, stdout)
	if err != nil {
		return err
	}
	sp, err := spec.Load(*specPath)
	if err != nil {
		return err
	}
	warnUnreachable(stderr, sp, window)

	ctx := context.Background()
	dg, err := digest.Evaluate(ctx, prom.New(*promURL), sp, digest.Options{
		Window: window, At: at, Compare: compare, MovedPP: *moved,
	})
	if err != nil {
		return err
	}

	if dg.Quiet() && !*always {
		fmt.Fprintf(stderr, "slo-calc: all %d SLOs healthy and nothing moved, not delivering (use --always to send anyway)\n", len(dg.Items))
	} else if err := d.Deliver(ctx, digest.Message{Summary: dg.Summary(), Detail: dg.Detail(), Loud: dg.Loud()}); err != nil {
		return err
	}

	if burning := dg.Burning(); *failOnBurn && len(burning) > 0 {
		return &exitError{code: 2, msg: "error budget burning: " + strings.Join(burning, ", ")}
	}
	return nil
}

func deliverer(kind, slackChannel, chatID string, stdout io.Writer) (digest.Deliverer, error) {
	switch kind {
	case "stdout":
		return digest.Writer{W: stdout}, nil
	case "slack":
		if slackChannel != "" {
			tok := getenv("SLACK_BOT_TOKEN")
			if tok == "" {
				return nil, fmt.Errorf("--slack-channel needs SLACK_BOT_TOKEN in the environment")
			}
			return &digest.SlackBot{Token: tok, Channel: slackChannel}, nil
		}
		if u := getenv("SLACK_WEBHOOK_URL"); u != "" {
			return &digest.SlackWebhook{URL: u}, nil
		}
		return nil, fmt.Errorf("--deliver slack needs SLACK_WEBHOOK_URL, or SLACK_BOT_TOKEN with --slack-channel")
	case "telegram":
		tok := getenv("TELEGRAM_BOT_TOKEN")
		if tok == "" {
			return nil, fmt.Errorf("--deliver telegram needs TELEGRAM_BOT_TOKEN in the environment")
		}
		if chatID == "" {
			chatID = getenv("TELEGRAM_CHAT_ID")
		}
		if chatID == "" {
			return nil, fmt.Errorf("--deliver telegram needs --chat-id or TELEGRAM_CHAT_ID")
		}
		return &digest.Telegram{Token: tok, ChatID: chatID}, nil
	}
	return nil, fmt.Errorf("unknown --deliver %q (want stdout, slack or telegram)", kind)
}
