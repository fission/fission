// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// readyEndpoints builds a slice of Ready endpoints from addresses, in the
// given order (callers construct their own out-of-order or mixed-readiness
// slices where the test needs that).
func readyEndpoints(addresses ...string) []Endpoint {
	eps := make([]Endpoint, len(addresses))
	for i, a := range addresses {
		eps[i] = Endpoint{Address: a, Ready: true}
	}
	return eps
}

// TestPredictStickyDeterministic: the same key against the same endpoint set
// always picks the same endpoint (PredictSticky is a pure function of its
// inputs, so this is really pinning "no hidden state", not the math).
func TestPredictStickyDeterministic(t *testing.T) {
	t.Parallel()
	eps := readyEndpoints("10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888")

	first, ok := PredictSticky(eps, "session-repeat")
	require.True(t, ok)
	for i := 0; i < 20; i++ {
		got, ok := PredictSticky(eps, "session-repeat")
		require.True(t, ok)
		assert.Equal(t, first.Address, got.Address)
	}
}

// TestPredictStickyReadyOnly: the pick is always drawn from the Ready
// subset, never a not-Ready endpoint even when it would otherwise win.
func TestPredictStickyReadyOnly(t *testing.T) {
	t.Parallel()
	eps := []Endpoint{
		{Address: "10.0.0.1:8888", Ready: false},
		{Address: "10.0.0.2:8888", Ready: true},
		{Address: "10.0.0.3:8888", Ready: false},
	}
	got, ok := PredictSticky(eps, "session-x")
	require.True(t, ok)
	assert.Equal(t, "10.0.0.2:8888", got.Address)

	// Cross-check across many keys: whichever wins must be Ready.
	for i := range 50 {
		key := fmt.Sprintf("session-%d", i)
		got, ok := PredictSticky(eps, key)
		require.True(t, ok)
		assert.Equal(t, "10.0.0.2:8888", got.Address, "the only Ready endpoint must always win for key %q", key)
	}
}

// TestPredictStickyNoneReady: empty input and an all-not-Ready input both
// report false with a zero-value Endpoint, never a spurious pick.
func TestPredictStickyNoneReady(t *testing.T) {
	t.Parallel()

	got, ok := PredictSticky(nil, "session-x")
	assert.False(t, ok)
	assert.Equal(t, Endpoint{}, got)

	eps := []Endpoint{
		{Address: "10.0.0.1:8888", Ready: false},
		{Address: "10.0.0.2:8888", Ready: false},
	}
	got, ok = PredictSticky(eps, "session-x")
	assert.False(t, ok)
	assert.Equal(t, Endpoint{}, got)
}

// TestPickHighestScoreTieBreak pins the tie-break directly: pickHighestScore
// is fed a stub score function so two distinct addresses genuinely tie (an
// actual hrwScore collision is not constructible by hand). The pick must be
// the lexicographically smallest Address, independent of slice order.
func TestPickHighestScoreTieBreak(t *testing.T) {
	t.Parallel()
	constScore := func(Endpoint) uint64 { return 42 }

	eps := []Endpoint{
		{Address: "10.0.0.3:8888", Ready: true},
		{Address: "10.0.0.1:8888", Ready: true},
		{Address: "10.0.0.2:8888", Ready: true},
	}
	got, ok := pickHighestScore(eps, constScore)
	require.True(t, ok)
	assert.Equal(t, "10.0.0.1:8888", got.Address, "tie-break must choose the lexicographically smallest address")

	// Same set, different slice order: the pick must not depend on order.
	reordered := []Endpoint{eps[2], eps[0], eps[1]}
	got2, ok := pickHighestScore(reordered, constScore)
	require.True(t, ok)
	assert.Equal(t, got.Address, got2.Address)
}

// TestPredictStickyMatchesHrwScoreMax cross-checks PredictSticky against the
// maximum of hrwScore computed directly over the ready set, so a refactor of
// either side that drifts the ranking fails loudly here instead of silently.
func TestPredictStickyMatchesHrwScoreMax(t *testing.T) {
	t.Parallel()
	addresses := []string{"10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888", "10.0.0.4:8888"}
	eps := readyEndpoints(addresses...)

	for _, key := range []string{"session-1", "abc", "zzz-long-key-1234567890", ""} {
		got, ok := PredictSticky(eps, key)
		require.True(t, ok)

		var wantAddr string
		var wantScore uint64
		found := false
		for _, a := range addresses {
			s := hrwScore(key, a)
			if !found || s > wantScore {
				wantAddr, wantScore, found = a, s, true
			}
		}
		assert.Equal(t, wantAddr, got.Address, "PredictSticky must match the direct hrwScore maximum for key %q", key)
	}
}

// TestPredictStickyRemovingPickedEndpointMovesOnlyThatKey spot-checks the S5
// rendezvous property with 3 keys against a fixed 5-endpoint set: removing
// one endpoint moves the prediction only for keys that were actually
// assigned to IT, never for keys assigned elsewhere. (Two of the 3 keys can
// legitimately land on the same winning endpoint — that is not a violation;
// the assertion below keys off each key's OWN before-pick, not off which key
// nominated the removal.)
func TestPredictStickyRemovingPickedEndpointMovesOnlyThatKey(t *testing.T) {
	t.Parallel()
	addresses := []string{"10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888", "10.0.0.4:8888", "10.0.0.5:8888"}
	eps := readyEndpoints(addresses...)
	keys := []string{"session-a", "session-b", "session-c"}

	before := make(map[string]string, len(keys))
	for _, k := range keys {
		ep, ok := PredictSticky(eps, k)
		require.True(t, ok)
		before[k] = ep.Address
	}

	for _, removeKey := range keys {
		t.Run(removeKey, func(t *testing.T) {
			removedAddr := before[removeKey]
			remaining := make([]Endpoint, 0, len(eps)-1)
			for _, ep := range eps {
				if ep.Address != removedAddr {
					remaining = append(remaining, ep)
				}
			}

			for _, k := range keys {
				got, ok := PredictSticky(remaining, k)
				require.True(t, ok)
				if before[k] == removedAddr {
					assert.NotEqual(t, removedAddr, got.Address, "key %s must move off its removed endpoint", k)
				} else {
					assert.Equal(t, before[k], got.Address, "S5 violated: unrelated key %s moved", k)
				}
			}
		})
	}
}

// TestPredictStickyPropertyUnrelatedRemovalStable is the S5 rendezvous
// property as a rapid check: for a random ready endpoint set and key,
// removing any endpoint OTHER than the current winner never changes the
// pick.
func TestPredictStickyPropertyUnrelatedRemovalStable(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 12).Draw(rt, "n")
		addresses := make([]string, n)
		for i := range n {
			addresses[i] = fmt.Sprintf("10.0.0.%d:8888", i+1)
		}
		eps := readyEndpoints(addresses...)
		key := rapid.StringMatching(`[a-zA-Z0-9_-]{1,32}`).Draw(rt, "key")

		winner, ok := PredictSticky(eps, key)
		if !ok {
			rt.Fatalf("expected a pick among %d ready endpoints", n)
		}

		removeIdx := rapid.IntRange(0, n-1).
			Filter(func(i int) bool { return addresses[i] != winner.Address }).
			Draw(rt, "removeIdx")

		remaining := make([]Endpoint, 0, n-1)
		for i, ep := range eps {
			if i != removeIdx {
				remaining = append(remaining, ep)
			}
		}

		after, ok := PredictSticky(remaining, key)
		if !ok {
			rt.Fatalf("expected a pick among %d ready endpoints", n-1)
		}
		if after.Address != winner.Address {
			rt.Fatalf("S5 violated: removing unrelated endpoint %s moved key %q from %s to %s",
				addresses[removeIdx], key, winner.Address, after.Address)
		}
	})
}
