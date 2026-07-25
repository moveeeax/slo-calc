package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpec drops a spec file in a temp dir and returns its path.
func writeSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "slos.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalSpec = `
slos:
  - name: api
    objective: 99.9
    good: sum(increase(good[$window]))
    total: sum(increase(total[$window]))
`

func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = run(args, &out, &errb)
	return out.String(), errb.String(), err
}

// TestWindowMustBePositive covers the zero-length window. A window of zero
// divides by zero in every burn-rate formula and renders as an invalid
// PromQL range selector, so it has to be rejected up front rather than
// producing thresholds of 0 that make every alert fire immediately.
func TestWindowMustBePositive(t *testing.T) {
	spec := writeSpec(t, minimalSpec)
	for _, w := range []string{"0d", "0s", "0m", "0h", "0w"} {
		_, _, err := runCLI(t, "--spec", spec, "--window", w, "--output", "rules")
		if err == nil {
			t.Errorf("--window %q should be rejected", w)
			continue
		}
		if !strings.Contains(err.Error(), "must be positive") {
			t.Errorf("--window %q: unhelpful error %v", w, err)
		}
	}
}

// TestWindowOverflowRejected makes sure a window too large for a Duration
// fails loudly instead of silently wrapping into a short window.
func TestWindowOverflowRejected(t *testing.T) {
	spec := writeSpec(t, minimalSpec)
	_, _, err := runCLI(t, "--spec", spec, "--window", "100000000000d", "--output", "rules")
	if err == nil {
		t.Fatal("an overflowing --window should be rejected")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Errorf("unhelpful error for an overflowing window: %v", err)
	}
}

func TestRulesOutput(t *testing.T) {
	spec := writeSpec(t, minimalSpec)
	stdout, stderr, err := runCLI(t, "--spec", spec, "--window", "30d", "--output", "rules")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"slo:sli_error:ratio_rate1h",
		"slo:sli_error:ratio_rate5m",
		`{slo="api"}`,
		"> 0.0144", // 14.4x page threshold for a 99.9% objective
		"SLOBurnPage",
		"SLOBurnTicket",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("rules output missing %q:\n%s", want, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("a sane 99.9%% spec should produce no warnings, got:\n%s", stderr)
	}
}

// TestUnreachableAlertWarning checks that a loose objective, whose page
// threshold lands above an achievable error ratio, is called out on stderr.
// The rules file is still emitted and still valid — it just never fires.
func TestUnreachableAlertWarning(t *testing.T) {
	spec := writeSpec(t, `
slos:
  - name: loose
    objective: 90
    good: g[$window]
    total: t[$window]
`)
	stdout, stderr, err := runCLI(t, "--spec", spec, "--window", "30d", "--output", "rules")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "can never fire") {
		t.Errorf("expected an unreachable-threshold warning, got stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "1.44") {
		t.Errorf("warning should name the offending threshold, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "SLOBurnPage") {
		t.Errorf("the rules should still be emitted:\n%s", stdout)
	}
}

func TestFlagErrors(t *testing.T) {
	spec := writeSpec(t, minimalSpec)
	cases := map[string][]string{
		"missing spec":       {"--window", "30d"},
		"unknown output":     {"--spec", spec, "--output", "yaml"},
		"bad window":         {"--spec", spec, "--window", "30x"},
		"prometheus needed":  {"--spec", spec, "--output", "table"},
		"json needs prom":    {"--spec", spec, "--output", "json"},
		"bad at":             {"--spec", spec, "--output", "table", "--prometheus", "http://x", "--at", "yesterday"},
		"nonexistent spec":   {"--spec", filepath.Join(t.TempDir(), "nope.yaml")},
		"objective too high": {"--spec", writeSpec(t, "slos:\n  - name: a\n    objective: 100\n    good: g\n    total: t\n")},
	}
	for label, args := range cases {
		if _, _, err := runCLI(t, args...); err == nil {
			t.Errorf("%s: expected an error, got none", label)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	stdout, _, err := runCLI(t, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != version {
		t.Errorf("--version printed %q, want %q", stdout, version)
	}
}
