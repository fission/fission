// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// Threshold is an absolute SLO bound on a metric. Either or both bounds may be
// set; a value strictly outside [Min, Max] is a breach.
type Threshold struct {
	Max *float64 `json:"max,omitempty"`
	Min *float64 `json:"min,omitempty"`
}

// ScenarioThresholds maps metric name -> bound for one scenario.
type ScenarioThresholds struct {
	Metrics map[string]Threshold `json:"metrics"`
}

// Thresholds is the parsed thresholds.yaml.
type Thresholds struct {
	Scenarios map[string]ScenarioThresholds `json:"scenarios"`
}

// LoadThresholds reads and parses a thresholds YAML file.
func LoadThresholds(path string) (Thresholds, error) {
	var t Thresholds
	data, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := yaml.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("parse %s: %w", path, err)
	}
	return t, nil
}

// Breach is a single threshold violation.
type Breach struct {
	Scenario string
	Metric   string
	Value    float64
	Limit    float64
	Kind     string // "max" or "min"
}

func (b Breach) String() string {
	return fmt.Sprintf("%s/%s = %.3f exceeds %s %.3f", b.Scenario, b.Metric, b.Value, b.Kind, b.Limit)
}

// Evaluate checks run against the thresholds and returns every breach. A metric
// named in the thresholds but absent from the result is reported as a breach so
// a silently-missing measurement cannot pass the gate.
func (t Thresholds) Evaluate(run Run) []Breach {
	var breaches []Breach
	byName := make(map[string]ScenarioResult, len(run.Scenarios))
	for _, s := range run.Scenarios {
		byName[s.Name] = s
	}
	for scenario, st := range t.Scenarios {
		res, ok := byName[scenario]
		if !ok || res.Skipped {
			continue // not run / skipped: nothing to gate
		}
		for metric, th := range st.Metrics {
			m, found := res.Metric(metric)
			if !found {
				breaches = append(breaches, Breach{Scenario: scenario, Metric: metric, Kind: "missing"})
				continue
			}
			if th.Max != nil && m.Value > *th.Max {
				breaches = append(breaches, Breach{Scenario: scenario, Metric: metric, Value: m.Value, Limit: *th.Max, Kind: "max"})
			}
			if th.Min != nil && m.Value < *th.Min {
				breaches = append(breaches, Breach{Scenario: scenario, Metric: metric, Value: m.Value, Limit: *th.Min, Kind: "min"})
			}
		}
	}
	return breaches
}
