// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestFindCeil(t *testing.T) {
	t.Parallel()
	dist := []functionWeightDistribution{
		{name: "a", weight: 50, sumPrefix: 50},
		{name: "b", weight: 50, sumPrefix: 100},
	}
	tests := []struct {
		randomNumber int
		want         string
	}{
		{0, "a"},
		{30, "a"},
		{50, "b"},
		{75, "b"},
		{100, "b"},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, findCeil(tt.randomNumber, dist), "findCeil(%d)", tt.randomNumber)
	}

	t.Run("single backend always selected", func(t *testing.T) {
		single := []functionWeightDistribution{{name: "only", weight: 100, sumPrefix: 100}}
		assert.Equal(t, "only", findCeil(42, single))
	})

	// Distributions with 3+ entries exercise the binary-search midpoint: a
	// broken midpoint loops/panics here, so these guard that regression.
	t.Run("three backends", func(t *testing.T) {
		dist := []functionWeightDistribution{
			{name: "a", weight: 33, sumPrefix: 33},
			{name: "b", weight: 33, sumPrefix: 66},
			{name: "c", weight: 34, sumPrefix: 100},
		}
		assert.Equal(t, "a", findCeil(10, dist))
		assert.Equal(t, "b", findCeil(50, dist))
		assert.Equal(t, "c", findCeil(80, dist))
		assert.Equal(t, "c", findCeil(100, dist))
	})

	t.Run("four backends", func(t *testing.T) {
		dist := []functionWeightDistribution{
			{name: "a", weight: 25, sumPrefix: 25},
			{name: "b", weight: 25, sumPrefix: 50},
			{name: "c", weight: 25, sumPrefix: 75},
			{name: "d", weight: 25, sumPrefix: 100},
		}
		assert.Equal(t, "a", findCeil(10, dist))
		assert.Equal(t, "b", findCeil(40, dist))
		assert.Equal(t, "c", findCeil(70, dist))
		assert.Equal(t, "d", findCeil(90, dist))
	})
}

func TestGetCanaryBackend(t *testing.T) {
	t.Parallel()
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "only"}}

	t.Run("returns the mapped function", func(t *testing.T) {
		fnMap := map[string]*fv1.Function{"only": fn}
		dist := []functionWeightDistribution{{name: "only", weight: 100, sumPrefix: 100}}
		got := getCanaryBackend(fnMap, dist)
		require.NotNil(t, got)
		assert.Equal(t, "only", got.Name)
	})

	t.Run("missing function maps to nil", func(t *testing.T) {
		dist := []functionWeightDistribution{{name: "absent", weight: 100, sumPrefix: 100}}
		assert.Nil(t, getCanaryBackend(map[string]*fv1.Function{}, dist))
	})

	t.Run("degenerate all-zero weights falls back to first entry instead of panicking", func(t *testing.T) {
		zero := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "zero"}}
		fnMap := map[string]*fv1.Function{"zero": zero}
		dist := []functionWeightDistribution{{name: "zero", weight: 0, sumPrefix: 0}}
		got := getCanaryBackend(fnMap, dist)
		require.NotNil(t, got)
		assert.Equal(t, "zero", got.Name)
	})
}

// buildDistribution mirrors resolveByFunctionWeights (functionReferenceResolver.go):
// cumulative sumPrefix in insertion order.
func buildDistribution(weights []struct {
	name   string
	weight int
}) []functionWeightDistribution {
	dist := make([]functionWeightDistribution, 0, len(weights))
	sumPrefix := 0
	for _, w := range weights {
		sumPrefix += w.weight
		dist = append(dist, functionWeightDistribution{name: w.name, weight: w.weight, sumPrefix: sumPrefix})
	}
	return dist
}

// TestFindCeil_FullEnumeration walks every draw in [0, total) -- the full
// half-open range getCanaryBackend now draws rand.Intn(total) from -- and
// checks that each backend is picked exactly `weight` times out of `total`.
// This is the regression test for the off-by-one bias in issue #3613: under
// the old `rand.Intn(total+1)` inclusive draw, a 50/50 split actually routed
// 51/101 vs 50/101 (the boundary draw==total always fell to the last
// entry). Enumerating every draw deterministically catches any future
// regression without relying on statistical sampling.
func TestFindCeil_FullEnumeration(t *testing.T) {
	t.Parallel()

	t.Run("30/70 split", func(t *testing.T) {
		dist := buildDistribution([]struct {
			name   string
			weight int
		}{{"a", 30}, {"b", 70}})
		total := dist[len(dist)-1].sumPrefix
		counts := map[string]int{}
		for draw := 0; draw < total; draw++ {
			counts[findCeil(draw, dist)]++
		}
		assert.Equal(t, 30, counts["a"])
		assert.Equal(t, 70, counts["b"])
		assert.Equal(t, total, counts["a"]+counts["b"])
	})

	t.Run("50/50 split", func(t *testing.T) {
		dist := buildDistribution([]struct {
			name   string
			weight int
		}{{"a", 50}, {"b", 50}})
		total := dist[len(dist)-1].sumPrefix
		counts := map[string]int{}
		for draw := 0; draw < total; draw++ {
			counts[findCeil(draw, dist)]++
		}
		assert.Equal(t, 50, counts["a"])
		assert.Equal(t, 50, counts["b"])
	})

	t.Run("0-weight entry never picked", func(t *testing.T) {
		dist := buildDistribution([]struct {
			name   string
			weight int
		}{{"a", 100}, {"b", 0}})
		total := dist[len(dist)-1].sumPrefix
		counts := map[string]int{}
		for draw := 0; draw < total; draw++ {
			counts[findCeil(draw, dist)]++
		}
		assert.Equal(t, 100, counts["a"])
		assert.Equal(t, 0, counts["b"])

		// 0-weight entry listed first: it must still never be picked.
		distFirst := buildDistribution([]struct {
			name   string
			weight int
		}{{"a", 0}, {"b", 100}})
		totalFirst := distFirst[len(distFirst)-1].sumPrefix
		countsFirst := map[string]int{}
		for draw := 0; draw < totalFirst; draw++ {
			countsFirst[findCeil(draw, distFirst)]++
		}
		assert.Equal(t, 0, countsFirst["a"])
		assert.Equal(t, 100, countsFirst["b"])
	})

	t.Run("boundary draws land on the correct backend", func(t *testing.T) {
		dist := buildDistribution([]struct {
			name   string
			weight int
		}{{"a", 30}, {"b", 70}})
		total := dist[len(dist)-1].sumPrefix
		assert.Equal(t, "a", findCeil(0, dist), "draw 0 must land on the first backend")
		assert.Equal(t, "b", findCeil(total-1, dist), "draw total-1 must land on the last backend")
	})
}
