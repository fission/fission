// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleRun() Run {
	return Run{
		RunID: "t1",
		Scenarios: []ScenarioResult{
			{
				Name: "warm-path",
				Metrics: []Metric{
					{Name: "p99", Unit: "ms", Value: 40, Better: Lower},
					{Name: "throughput", Unit: "rps", Value: 1200, Better: Higher},
				},
			},
			{Name: "skipped-one", Skipped: true, Skip: "image unset"},
		},
	}
}

func TestEvaluateMaxBreachAndPass(t *testing.T) {
	t.Parallel()
	run := sampleRun()

	pass := Thresholds{Scenarios: map[string]ScenarioThresholds{
		"warm-path": {Metrics: map[string]Threshold{"p99": {Max: new(50.0)}}},
	}}
	assert.Empty(t, pass.Evaluate(run), "p99 under the max should not breach")

	fail := Thresholds{Scenarios: map[string]ScenarioThresholds{
		"warm-path": {Metrics: map[string]Threshold{"p99": {Max: new(30.0)}}},
	}}
	breaches := fail.Evaluate(run)
	require.Len(t, breaches, 1)
	assert.Equal(t, "max", breaches[0].Kind)
}

func TestEvaluateMissingMetricIsBreach(t *testing.T) {
	t.Parallel()
	th := Thresholds{Scenarios: map[string]ScenarioThresholds{
		"warm-path": {Metrics: map[string]Threshold{"p999": {Max: new(100.0)}}},
	}}
	breaches := th.Evaluate(sampleRun())
	require.Len(t, breaches, 1)
	assert.Equal(t, "missing", breaches[0].Kind)
}

func TestEvaluateSkippedScenarioNotGated(t *testing.T) {
	t.Parallel()
	th := Thresholds{Scenarios: map[string]ScenarioThresholds{
		"skipped-one": {Metrics: map[string]Threshold{"p99": {Max: new(1.0)}}},
	}}
	assert.Empty(t, th.Evaluate(sampleRun()), "skipped scenarios are not gated")
}

func TestTrendSplitsByDirection(t *testing.T) {
	t.Parallel()
	smaller, bigger := Trend(sampleRun())
	require.Len(t, smaller, 1)
	assert.Equal(t, "warm-path/p99", smaller[0].Name)
	require.Len(t, bigger, 1)
	assert.Equal(t, "warm-path/throughput", bigger[0].Name)
}

func TestCompareFlagsRegression(t *testing.T) {
	t.Parallel()
	base := sampleRun()
	head := sampleRun()
	// Worsen latency (higher p99) and throughput (lower rps).
	head.Scenarios[0].Metrics[0].Value = 60   // p99 40 -> 60 (worse, lower-better)
	head.Scenarios[0].Metrics[1].Value = 1000 // tput 1200 -> 1000 (worse, higher-better)

	got := map[string]Delta{}
	for _, d := range Compare(base, head) {
		got[d.Metric] = d
	}
	assert.True(t, got["p99"].Regression)
	assert.Positive(t, got["p99"].PctChange)
	assert.True(t, got["throughput"].Regression)
	assert.Negative(t, got["throughput"].PctChange)
}
