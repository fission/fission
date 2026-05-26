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
}
