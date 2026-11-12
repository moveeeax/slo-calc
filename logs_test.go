package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moveeeax/slo-calc/internal/spec"
)

const fixtureLog = "internal/accesslog/testdata/combined.log"

func TestLogsTable(t *testing.T) {
	stdout, stderr, err := runCLI(t, "logs", "--input", fixtureLog, "--latency-target", "300ms",
		"--exclude-path", "^/healthz", "--exclude-agent", "(?i)bot", "--exclude-cancelled", "--top", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"analysed 5 requests from 9 lines (3 excluded, 1 unparsable)",
		"availability      99.900%    60.0000%  400.00x",
		"latency <= 300ms",
		"BURNING",
		"worst routes (2 of 3)",
		"POST    /orders",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q:\n%s", want, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
}

func TestLogsEmitSpecAndRules(t *testing.T) {
	dir := t.TempDir()
	specOut := filepath.Join(dir, "spec.yaml")
	rulesOut := filepath.Join(dir, "rules.yaml")
	_, _, err := runCLI(t, "logs", "--input", fixtureLog, "--objective", "99.5", "--latency-target", "300ms",
		"--latency-objective", "99", "--name", "shop", "--selector", `job="shop"`, "--output", "json",
		"--emit-spec", specOut, "--emit-rules", rulesOut)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := spec.Load(specOut)
	if err != nil {
		t.Fatalf("emitted spec does not load: %v", err)
	}
	if sp.SLOs[0].Name != "shop-availability" || sp.SLOs[0].Objective != 99.5 || sp.SLOs[1].Objective != 99 {
		t.Errorf("emitted spec: %+v", sp.SLOs)
	}
	raw, err := os.ReadFile(rulesOut)
	if err != nil {
		t.Fatal(err)
	}
	// The rules must be exactly what "slo-calc --spec" makes of that spec.
	fromSpec, _, err := runCLI(t, "--spec", specOut, "--output", "rules")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != fromSpec {
		t.Errorf("--emit-rules differs from --spec --output rules:\n%s\n---\n%s", raw, fromSpec)
	}
	if !strings.Contains(string(raw), `le="0.3"`) || !strings.Contains(string(raw), "SLOBurnPage") {
		t.Errorf("rules:\n%s", raw)
	}
}

func TestLogsGzipAndStdinStyleInputs(t *testing.T) {
	raw, err := os.ReadFile(fixtureLog)
	if err != nil {
		t.Fatal(err)
	}
	gz := filepath.Join(t.TempDir(), "access.log.gz")
	f, err := os.Create(gz)
	if err != nil {
		t.Fatal(err)
	}
	w := gzip.NewWriter(f)
	w.Write(raw)
	w.Close()
	f.Close()
	stdout, _, err := runCLI(t, "logs", "--input", gz, "--input", fixtureLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "analysed 16 requests from 18 lines") {
		t.Errorf("two inputs should add up:\n%s", stdout)
	}
}

func TestLogsWarnsWithoutDurations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clf.log")
	os.WriteFile(p, []byte(`1.2.3.4 - - [01/Nov/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 1`+"\n"), 0o600)
	_, stderr, err := runCLI(t, "logs", "--input", p, "--format", "common", "--latency-target", "1s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "latency SLI is empty") {
		t.Errorf("stderr: %s", stderr)
	}
}

func TestLogsErrors(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "g.log")
	os.WriteFile(garbage, []byte("nope\nnope\n"), 0o600)
	cases := map[string][]string{
		"no input":        {"logs"},
		"stray arg":       {"logs", "--input", fixtureLog, "extra"},
		"bad format":      {"logs", "--input", fixtureLog, "--format", "iis"},
		"bad unit":        {"logs", "--input", fixtureLog, "--duration-unit", "h"},
		"bad field":       {"logs", "--input", fixtureLog, "--format", "json", "--field", "nope=x"},
		"bad objective":   {"logs", "--input", fixtureLog, "--objective", "100"},
		"bad latency":     {"logs", "--input", fixtureLog, "--latency-target", "fast"},
		"zero latency":    {"logs", "--input", fixtureLog, "--latency-target", "0s"},
		"bad window":      {"logs", "--input", fixtureLog, "--window", "0d"},
		"bad route":       {"logs", "--input", fixtureLog, "--route", "orders/:id"},
		"bad regexp":      {"logs", "--input", fixtureLog, "--exclude-path", "("},
		"bad metric":      {"logs", "--input", fixtureLog, "--emit-rules", "x", "--metric-requests", "a-b"},
		"bad output":      {"logs", "--input", fixtureLog, "--output", "rules"},
		"missing file":    {"logs", "--input", filepath.Join(t.TempDir(), "none.log")},
		"nothing matched": {"logs", "--input", garbage},
	}
	for label, args := range cases {
		if _, _, err := runCLI(t, args...); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}
