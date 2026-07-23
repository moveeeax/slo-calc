// Package spec loads and validates the YAML SLO specification.
package spec

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind is the type of SLI a target is built from.
type Kind string

const (
	// Ratio SLIs divide a "good events" query by a "total events" query.
	Ratio Kind = "ratio"
	// Latency SLIs are ratio SLIs where "good" means "fast enough".
	Latency Kind = "latency"
)

// SLO is a single service level objective read from the spec file.
type SLO struct {
	Name string `yaml:"name"`
	// Objective is the target availability as a percentage, e.g. 99.9.
	Objective float64 `yaml:"objective"`
	// Type selects ratio or latency semantics. Defaults to ratio.
	Type Kind `yaml:"type"`
	// Good and Total are PromQL expressions. They may contain the
	// placeholder $window, which the evaluator replaces with the rate
	// window it is computing (e.g. 5m, 1h, 30d).
	Good  string `yaml:"good"`
	Total string `yaml:"total"`
	// Labels are attached to generated recording and alerting rules.
	Labels map[string]string `yaml:"labels"`
}

// Spec is the top-level document.
type Spec struct {
	SLOs []SLO `yaml:"slos"`
}

// Load reads, parses and validates a spec file.
func Load(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse validates raw YAML bytes into a Spec.
func Parse(raw []byte) (*Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Spec) validate() error {
	if len(s.SLOs) == 0 {
		return fmt.Errorf("spec has no slos")
	}
	seen := map[string]bool{}
	for i := range s.SLOs {
		slo := &s.SLOs[i]
		if slo.Name == "" {
			return fmt.Errorf("slo #%d: name is required", i)
		}
		if seen[slo.Name] {
			return fmt.Errorf("duplicate slo name %q", slo.Name)
		}
		seen[slo.Name] = true
		if slo.Objective <= 0 || slo.Objective >= 100 {
			return fmt.Errorf("slo %q: objective must be in (0,100), got %v", slo.Name, slo.Objective)
		}
		if slo.Type == "" {
			slo.Type = Ratio
		}
		if slo.Type != Ratio && slo.Type != Latency {
			return fmt.Errorf("slo %q: unknown type %q", slo.Name, slo.Type)
		}
		if strings.TrimSpace(slo.Good) == "" {
			return fmt.Errorf("slo %q: good query is required", slo.Name)
		}
		if strings.TrimSpace(slo.Total) == "" {
			return fmt.Errorf("slo %q: total query is required", slo.Name)
		}
	}
	return nil
}

// Render substitutes the $window placeholder in a query with the given
// window string (e.g. "5m").
func Render(query, window string) string {
	return strings.ReplaceAll(query, "$window", window)
}
