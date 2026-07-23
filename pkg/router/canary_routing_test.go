// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
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
		got := getCanaryBackend(fnMap, dist, "")
		require.NotNil(t, got)
		assert.Equal(t, "only", got.Name)
	})

	t.Run("missing function maps to nil", func(t *testing.T) {
		dist := []functionWeightDistribution{{name: "absent", weight: 100, sumPrefix: 100}}
		assert.Nil(t, getCanaryBackend(map[string]*fv1.Function{}, dist, ""))
	})
}

func twoBackendDist(primaryWeight int) (map[string]*fv1.Function, []functionWeightDistribution) {
	primary := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "primary"}}
	secondary := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "secondary"}}
	fnMap := map[string]*fv1.Function{"primary": primary, "secondary": secondary}
	dist := []functionWeightDistribution{
		{name: "primary", weight: primaryWeight, sumPrefix: primaryWeight},
		{name: "secondary", weight: 100 - primaryWeight, sumPrefix: 100},
	}
	return fnMap, dist
}

// TestGetCanaryBackend_Boundary pins the Task-3-review off-by-one fix: a
// 100/0 split must ALWAYS pick the primary and a 0/100 split must ALWAYS
// pick the secondary — for both the random draw (unkeyed) and the
// deterministic hash draw (keyed) — never the old ~1% leak from
// rand.Intn(sumPrefix+1).
func TestGetCanaryBackend_Boundary(t *testing.T) {
	t.Parallel()

	t.Run("100/0 always primary, unkeyed", func(t *testing.T) {
		t.Parallel()
		fnMap, dist := twoBackendDist(100)
		for range 2000 {
			assert.Equal(t, "primary", getCanaryBackend(fnMap, dist, "").Name)
		}
	})
	t.Run("0/100 always secondary, unkeyed", func(t *testing.T) {
		t.Parallel()
		fnMap, dist := twoBackendDist(0)
		for range 2000 {
			assert.Equal(t, "secondary", getCanaryBackend(fnMap, dist, "").Name)
		}
	})
	t.Run("100/0 always primary, keyed", func(t *testing.T) {
		t.Parallel()
		fnMap, dist := twoBackendDist(100)
		for i := range 2000 {
			key := fmt.Sprintf("key-%d", i)
			assert.Equal(t, "primary", getCanaryBackend(fnMap, dist, key).Name)
		}
	})
	t.Run("0/100 always secondary, keyed", func(t *testing.T) {
		t.Parallel()
		fnMap, dist := twoBackendDist(0)
		for i := range 2000 {
			key := fmt.Sprintf("key-%d", i)
			assert.Equal(t, "secondary", getCanaryBackend(fnMap, dist, key).Name)
		}
	})
}

// TestGetCanaryBackend_KeyedIsStable proves the headline Task-5 property: the
// SAME sticky key always picks the SAME backend, across many repeated calls.
func TestGetCanaryBackend_KeyedIsStable(t *testing.T) {
	t.Parallel()
	fnMap, dist := twoBackendDist(50)

	for _, key := range []string{"user-1", "user-2", "session-abc", "тест-utf8"} {
		first := getCanaryBackend(fnMap, dist, key).Name
		for range 100 {
			assert.Equal(t, first, getCanaryBackend(fnMap, dist, key).Name,
				"key %q must pick the same backend on every call", key)
		}
	}
}

// TestGetCanaryBackend_KeyedDistribution proves the deterministic hash pick
// lands close to the configured split over many DISTINCT keys (the
// rendezvous-style hash must not skew toward one side).
func TestGetCanaryBackend_KeyedDistribution(t *testing.T) {
	t.Parallel()
	fnMap, dist := twoBackendDist(70)

	primaryHits := 0
	const trials = 20000
	for i := range trials {
		key := fmt.Sprintf("sticky-key-%d", i)
		if getCanaryBackend(fnMap, dist, key).Name == "primary" {
			primaryHits++
		}
	}
	ratio := float64(primaryHits) / float64(trials)
	assert.InDelta(t, 0.70, ratio, 0.03, "keyed pick across %d distinct keys must land close to 70/30", trials)
}

// TestGetCanaryBackend_Unkeyed_Random proves an unkeyed pick is NOT
// deterministic — repeated calls with "" vary, unlike the keyed case.
func TestGetCanaryBackend_Unkeyed_Random(t *testing.T) {
	t.Parallel()
	fnMap, dist := twoBackendDist(50)

	seenPrimary, seenSecondary := false, false
	for range 200 {
		if getCanaryBackend(fnMap, dist, "").Name == "primary" {
			seenPrimary = true
		} else {
			seenSecondary = true
		}
		if seenPrimary && seenSecondary {
			break
		}
	}
	assert.True(t, seenPrimary && seenSecondary, "unkeyed pick over 200 draws at a 50/50 split must hit both sides")
}

// TestGetCanaryBackend_WeightChangeMigratesOnlyBoundaryCrossingKeys is the
// RFC-0025 Task 5 migration-scope test: generate a large key population,
// record each key's pick at a 90/10 split, then re-pick the SAME keys at a
// 70/30 split. Only keys whose hash landed in [70,90) (owned by primary
// under 90/10, owned by secondary under 70/30) may change backend; every
// other key's pick must be unchanged.
func TestGetCanaryBackend_WeightChangeMigratesOnlyBoundaryCrossingKeys(t *testing.T) {
	t.Parallel()
	fnMapBefore, distBefore := twoBackendDist(90)
	fnMapAfter, distAfter := twoBackendDist(70)

	const n = 1000
	keys := make([]string, n)
	before := make([]string, n)
	for i := range n {
		keys[i] = fmt.Sprintf("migrate-key-%d", i)
		before[i] = getCanaryBackend(fnMapBefore, distBefore, keys[i]).Name
	}

	movedCount := 0
	for i := range n {
		after := getCanaryBackend(fnMapAfter, distAfter, keys[i]).Name
		hash := int(stickyWeightHash(keys[i]) % 100)
		inBoundaryBand := hash >= 70 && hash < 90
		if before[i] != after {
			movedCount++
			assert.Truef(t, inBoundaryBand,
				"key %q (hash %% 100 = %d) moved from %s to %s but is outside the [70,90) boundary band",
				keys[i], hash, before[i], after)
			assert.Equal(t, "primary", before[i], "a moved key must have been primary-owned under 90/10")
			assert.Equal(t, "secondary", after, "a moved key must become secondary-owned under 70/30")
		} else if inBoundaryBand {
			t.Errorf("key %q (hash %% 100 = %d) is inside the [70,90) boundary band but did not move", keys[i], hash)
		}
	}
	assert.Positive(t, movedCount, "at least one key should fall in the ~20%% boundary band over %d keys", n)
}
