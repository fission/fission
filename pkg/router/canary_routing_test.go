// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"hash/fnv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestStickyWeightHash_MatchesFNV64a pins stickyWeightHash's inlined FNV-1a
// against the standard library's hash/fnv New64a for the same inputs: the
// inlining (heap-allocation fix, RFC-0025 efficiency pass) must be
// byte-for-byte identical, or existing sticky assignments would silently
// shift the moment it ships. Includes a multi-write case matching how
// hrwScore (pkg/router/endpointcache/index.go) composes a key from several
// parts via successive Write calls, to prove the inlined accumulator agrees
// with hash.Hash64 not just for a single Write but across write boundaries
// too.
func TestStickyWeightHash_MatchesFNV64a(t *testing.T) {
	t.Parallel()

	stdFNV64a := func(parts ...string) uint64 {
		h := fnv.New64a()
		for _, p := range parts {
			_, _ = h.Write([]byte(p))
		}
		return h.Sum64()
	}

	keys := []string{
		"",
		"a",
		"user-1",
		"session-abc",
		"тест-utf8",
		"a-fairly-long-sticky-key-with-several-words-in-it-1234567890",
		string([]byte{0x00, 0xff, 0x10, 0x7f}),
	}
	for _, key := range keys {
		assert.Equalf(t, stdFNV64a(key), stickyWeightHash(key), "stickyWeightHash(%q)", key)
	}

	// Multi-write sequence: stickyWeightHash itself only ever does a single
	// Write of the whole key, but the inlined accumulator must still agree
	// with hash.Hash64 write-for-write -- concatenating the parts first and
	// hashing the whole string in one shot must equal writing them
	// separately, exactly like fnv.New64a() does.
	parts := []string{"default", "/", "hello", "/", "hello-v1"}
	concatenated := ""
	for _, p := range parts {
		concatenated += p
	}
	assert.Equal(t, stdFNV64a(parts...), stickyWeightHash(concatenated),
		"stickyWeightHash over the concatenated key must match FNV-1a written part-by-part")
}

func TestGetCanaryBackend(t *testing.T) {
	t.Parallel()
	fn := &fv1.Function{Name: "only"}

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

	t.Run("degenerate all-zero weights falls back to first entry instead of panicking", func(t *testing.T) {
		zero := &fv1.Function{Name: "zero"}
		fnMap := map[string]*fv1.Function{"zero": zero}
		dist := []functionWeightDistribution{{name: "zero", weight: 0, sumPrefix: 0}}
		got := getCanaryBackend(fnMap, dist, "")
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
		for draw := range total {
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
		for draw := range total {
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
		for draw := range total {
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
		for draw := range totalFirst {
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

func twoBackendDist(primaryWeight int) (map[string]*fv1.Function, []functionWeightDistribution) {
	primary := &fv1.Function{Name: "primary"}
	secondary := &fv1.Function{Name: "secondary"}
	fnMap := map[string]*fv1.Function{"primary": primary, "secondary": secondary}
	dist := []functionWeightDistribution{
		{name: "primary", weight: primaryWeight, sumPrefix: primaryWeight},
		{name: "secondary", weight: 100 - primaryWeight, sumPrefix: 100},
	}
	return fnMap, dist
}

// TestGetCanaryBackend_Boundary pins the invariant that a 100/0 split must
// ALWAYS pick the primary and a 0/100 split must ALWAYS pick the secondary —
// for both the random draw (unkeyed) and the deterministic hash draw
// (keyed) — never the ~1% leak rand.Intn(sumPrefix+1) would produce at
// exactly 0 or 100.
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
