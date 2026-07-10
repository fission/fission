// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"fmt"
	"slices"
	"strconv"
)

// Aggregate folds repetition results for one scenario into a single
// ScenarioResult, keeping metric names stable for thresholds and the trend:
// each metric's value is the median across reps, with the observed min..max
// recorded in Meta["<metric>_range"] so the run's noise floor is visible next
// to the number. The first non-clean rep is returned as-is (annotated with its
// rep index) since partial metrics aren't comparable.
func Aggregate(reps []ScenarioResult) ScenarioResult {
	last := reps[len(reps)-1]
	if len(reps) == 1 {
		return last
	}
	if last.Error != "" || last.Skipped {
		last.SetMeta("failed_repetition", strconv.Itoa(len(reps)-1))
		return last
	}
	// Build the aggregate from scratch (no struct copy — that would alias
	// rep 0's Meta map and mutate the rep's own result).
	agg := ScenarioResult{Name: reps[0].Name, Tags: reps[0].Tags}
	for k, v := range reps[0].Meta {
		agg.SetMeta(k, v)
	}
	agg.SetMeta("repetitions", strconv.Itoa(len(reps)))
	// Single sweep collecting values per metric name. The metric set is the
	// union across reps (first-seen order), not rep 0's set: conditionally
	// emitted metrics (apiserver_calls drops its sample on a counter reset or
	// scrape miss) must survive one rep missing them.
	var order []Metric
	vals := map[string][]float64{}
	for _, r := range reps {
		for _, m := range r.Metrics {
			if _, ok := vals[m.Name]; !ok {
				order = append(order, m)
			}
			vals[m.Name] = append(vals[m.Name], m.Value)
		}
	}
	for _, m := range order {
		v := vals[m.Name]
		slices.Sort(v)
		med := v[len(v)/2]
		if len(v)%2 == 0 {
			med = (v[len(v)/2-1] + v[len(v)/2]) / 2
		}
		agg.Metrics = append(agg.Metrics, Metric{Name: m.Name, Unit: m.Unit, Value: med, Better: m.Better})
		agg.SetMeta(m.Name+"_range", fmt.Sprintf("%.3f..%.3f", v[0], v[len(v)-1]))
	}
	return agg
}
