package spec

import (
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	s, err := Parse([]byte(`
slos:
  - name: a
    objective: 99.9
    good: g[$window]
    total: t[$window]
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.SLOs[0].Type != Ratio {
		t.Errorf("type should default to ratio, got %q", s.SLOs[0].Type)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"empty":          `slos: []`,
		"no name":        "slos:\n  - objective: 99\n    good: g\n    total: t",
		"bad objective":  "slos:\n  - name: a\n    objective: 100\n    good: g\n    total: t",
		"missing good":   "slos:\n  - name: a\n    objective: 99\n    total: t",
		"bad type":       "slos:\n  - name: a\n    objective: 99\n    type: weird\n    good: g\n    total: t",
		"dup name":       "slos:\n  - name: a\n    objective: 99\n    good: g\n    total: t\n  - name: a\n    objective: 98\n    good: g\n    total: t",
		"unknown field":  "slos:\n  - name: a\n    objective: 99\n    good: g\n    total: t\n    bogus: 1",
		"nan objective":  "slos:\n  - name: a\n    objective: .nan\n    good: g\n    total: t",
		"-inf objective": "slos:\n  - name: a\n    objective: -.inf\n    good: g\n    total: t",
	}
	for label, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", label)
		}
	}
}

// TestParseRejectsNaNObjective is a regression test: every Go comparison
// with NaN (<=, >=, ...) is false, so a naive "objective <= 0 || >= 100"
// range check lets YAML's ".nan" literal straight through. That NaN then
// propagates into the generated burn-rate thresholds and comes out as the
// literal text "NaN" in the rules file, which is not valid PromQL.
func TestParseRejectsNaNObjective(t *testing.T) {
	_, err := Parse([]byte("slos:\n  - name: a\n    objective: .nan\n    good: g\n    total: t\n"))
	if err == nil {
		t.Fatal("a NaN objective must be rejected")
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Errorf("error should mention NaN, got: %v", err)
	}
}

func TestRender(t *testing.T) {
	if got := Render("rate(x[$window])", "5m"); got != "rate(x[5m])" {
		t.Errorf("Render = %q", got)
	}
}
