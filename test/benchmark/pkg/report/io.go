// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"encoding/json"
	"fmt"
	"os"
)

// WriteRun marshals run to path as indented JSON.
func WriteRun(path string, run Run) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadRun loads a Run from a JSON file.
func ReadRun(path string) (Run, error) {
	var run Run
	data, err := os.ReadFile(path)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(data, &run); err != nil {
		return run, fmt.Errorf("decode %s: %w", path, err)
	}
	return run, nil
}

// Metric looks up a metric by name within a scenario result.
func (r ScenarioResult) Metric(name string) (Metric, bool) {
	for _, m := range r.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}
