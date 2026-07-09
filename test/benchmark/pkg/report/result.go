// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package report defines the benchmark result schema and the gate/summary/trend
// outputs computed from it. It is decoupled from how results are produced.
package report

import (
	"math"
	"time"
)

// Improvement direction for a metric, used by threshold gating and the trend
// dashboard to know which way is "better".
const (
	Lower  = "lower"
	Higher = "higher"
)

// Metric is one measured value with its unit and improvement direction.
type Metric struct {
	Name   string  `json:"name"`
	Unit   string  `json:"unit"` // ms | rps | ratio | count | MiB | s
	Value  float64 `json:"value"`
	Better string  `json:"better,omitempty"` // Lower | Higher
}

// ScenarioResult holds one scenario's metrics (or its skip/error state).
type ScenarioResult struct {
	Name    string            `json:"name"`
	Tags    []string          `json:"tags"`
	Metrics []Metric          `json:"metrics,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
	Skipped bool              `json:"skipped,omitempty"`
	Skip    string            `json:"skipReason,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// Add appends a metric. Non-finite values are dropped: json.Marshal rejects
// NaN/±Inf, so a single poisoned sample would otherwise lose the entire run's
// results file.
func (r *ScenarioResult) Add(name, unit, better string, value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	r.Metrics = append(r.Metrics, Metric{Name: name, Unit: unit, Value: value, Better: better})
}

// SetMeta records a free-form key/value (e.g. executor type, pool size).
func (r *ScenarioResult) SetMeta(k, v string) {
	if r.Meta == nil {
		r.Meta = map[string]string{}
	}
	r.Meta[k] = v
}

// Run is the full output of a benchmark invocation.
type Run struct {
	RunID          string           `json:"runId"`
	FissionVersion string           `json:"fissionVersion,omitempty"`
	K8sVersion     string           `json:"k8sVersion,omitempty"`
	GitSHA         string           `json:"gitSha,omitempty"`
	StartedAt      time.Time        `json:"startedAt"`
	FinishedAt     time.Time        `json:"finishedAt"`
	Scenarios      []ScenarioResult `json:"scenarios"`
}
