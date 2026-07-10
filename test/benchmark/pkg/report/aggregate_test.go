// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAggregate(t *testing.T) {
	t.Parallel()
	mk := func(p50, rps float64) ScenarioResult {
		var r ScenarioResult
		r.Name = "warm-path"
		r.Add("p50", "ms", Lower, p50)
		r.Add("throughput", "rps", Higher, rps)
		return r
	}

	t.Run("single rep passes through untouched", func(t *testing.T) {
		t.Parallel()
		in := mk(10, 100)
		out := Aggregate([]ScenarioResult{in})
		assert.Equal(t, in, out)
		assert.Empty(t, out.Meta)
	})

	t.Run("odd rep count takes the median and records the range", func(t *testing.T) {
		t.Parallel()
		out := Aggregate([]ScenarioResult{mk(30, 90), mk(10, 110), mk(20, 100)})
		require.Len(t, out.Metrics, 2)
		assert.Equal(t, 20.0, out.Metrics[0].Value)
		assert.Equal(t, 100.0, out.Metrics[1].Value)
		assert.Equal(t, "3", out.Meta["repetitions"])
		assert.Equal(t, "10.000..30.000", out.Meta["p50_range"])
		// Direction/unit survive aggregation — thresholds and trend depend on them.
		assert.Equal(t, Higher, out.Metrics[1].Better)
		assert.Equal(t, "ms", out.Metrics[0].Unit)
	})

	t.Run("even rep count averages the middle pair", func(t *testing.T) {
		t.Parallel()
		out := Aggregate([]ScenarioResult{mk(10, 0), mk(20, 0), mk(40, 0), mk(30, 0)})
		assert.Equal(t, 25.0, out.Metrics[0].Value)
	})

	t.Run("metric missing from rep 0 still aggregates from later reps", func(t *testing.T) {
		t.Parallel()
		withCalls := mk(20, 100)
		withCalls.Add("apiserver_calls", "count", Lower, 42)
		out := Aggregate([]ScenarioResult{mk(10, 100), withCalls, mk(30, 100)})
		names := make([]string, 0, len(out.Metrics))
		for _, m := range out.Metrics {
			names = append(names, m.Name)
		}
		assert.Contains(t, names, "apiserver_calls")
	})

	t.Run("aggregation does not mutate rep 0's meta", func(t *testing.T) {
		t.Parallel()
		rep0 := mk(10, 100)
		rep0.SetMeta("executor", "poolmgr")
		_ = Aggregate([]ScenarioResult{rep0, mk(20, 100)})
		assert.Equal(t, map[string]string{"executor": "poolmgr"}, rep0.Meta)
	})

	t.Run("an errored rep short-circuits with its rep index", func(t *testing.T) {
		t.Parallel()
		bad := ScenarioResult{Name: "warm-path", Error: "boom"}
		out := Aggregate([]ScenarioResult{mk(10, 100), bad})
		assert.Equal(t, "boom", out.Error)
		assert.Equal(t, "1", out.Meta["failed_repetition"])
		assert.Empty(t, out.Metrics)
	})
}
