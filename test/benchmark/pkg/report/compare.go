// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import "sort"

// Delta is the change in one metric between a base and head run.
type Delta struct {
	Scenario   string
	Metric     string
	Unit       string
	Base       float64
	Head       float64
	PctChange  float64 // (head-base)/base * 100; 0 when base is 0
	Better     string
	Regression bool // head moved in the worse direction
}

// Compare returns per-metric deltas for every metric present in both runs (e.g.
// HEAD vs a released version, or gates-on vs gates-off), sorted by scenario then
// metric.
func Compare(base, head Run) []Delta {
	baseIdx := map[string]Metric{}
	for _, s := range base.Scenarios {
		for _, m := range s.Metrics {
			baseIdx[s.Name+"/"+m.Name] = m
		}
	}

	var out []Delta
	for _, s := range head.Scenarios {
		for _, m := range s.Metrics {
			bm, ok := baseIdx[s.Name+"/"+m.Name]
			if !ok {
				continue
			}
			pct := 0.0
			if bm.Value != 0 {
				pct = (m.Value - bm.Value) / bm.Value * 100
			}
			regression := (m.Better == Higher && m.Value < bm.Value) ||
				(m.Better != Higher && m.Value > bm.Value)
			out = append(out, Delta{
				Scenario: s.Name, Metric: m.Name, Unit: m.Unit,
				Base: bm.Value, Head: m.Value, PctChange: pct,
				Better: m.Better, Regression: regression,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scenario != out[j].Scenario {
			return out[i].Scenario < out[j].Scenario
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}
