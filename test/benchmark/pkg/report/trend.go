// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"encoding/json"
	"fmt"
	"os"
)

// TrendPoint is one data point in the benchmark-action/github-action-benchmark
// "customSmallerIsBetter"/"customBiggerIsBetter" input format.
type TrendPoint struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

// Trend splits a run's metrics into two series by improvement direction: the
// "smaller" series (latencies, error rates) and the "bigger" series
// (throughput). github-action-benchmark needs a single direction per file, so
// the CI publishes each under the matching tool type.
func Trend(run Run) (smaller, bigger []TrendPoint) {
	for _, s := range run.Scenarios {
		if s.Skipped || s.Error != "" {
			continue
		}
		for _, m := range s.Metrics {
			p := TrendPoint{Name: s.Name + "/" + m.Name, Unit: m.Unit, Value: m.Value}
			if m.Better == Higher {
				bigger = append(bigger, p)
			} else {
				smaller = append(smaller, p)
			}
		}
	}
	return smaller, bigger
}

// WriteTrend writes the two trend series to smallerPath and biggerPath as JSON
// arrays. An empty path skips that series.
func WriteTrend(run Run, smallerPath, biggerPath string) error {
	smaller, bigger := Trend(run)
	if smallerPath != "" {
		if err := writeJSONArray(smallerPath, smaller); err != nil {
			return err
		}
	}
	if biggerPath != "" {
		if err := writeJSONArray(biggerPath, bigger); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONArray(path string, points []TrendPoint) error {
	if points == nil {
		points = []TrendPoint{}
	}
	data, err := json.MarshalIndent(points, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal trend %s: %w", path, err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
