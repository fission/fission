// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// RFC-0023 phase-3 sticky-pick properties. S4: the pick is a pure function
// of (key, ready endpoint set) — same inputs, same pod, on every router
// replica independently. S5: HRW's minimal reshuffle — removing a pod moves
// only its keys; adding one moves only keys now ranking it first. Stickiness
// is best-effort (S6): a saturated sticky target overflows to the
// next-ranked admissible endpoint, never queues.

// admitAll drains one Admit per key and returns key->address, releasing every
// admission so load never influences the pick under test.
func admitOnce(t interface{ Fatalf(string, ...any) }, ix *Index, keys []string) map[string]string {
	got := make(map[string]string, len(keys))
	for _, k := range keys {
		ep, release, res := ix.Admit("default", "fn-a", 100, k)
		if res != Admitted {
			t.Fatalf("Admit(%q) = %v", k, res)
		}
		release()
		got[k] = ep.Address
	}
	return got
}

func TestStickyDeterministicAcrossReplicas(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "pods")
		addresses := make([]string, n)
		for i := range n {
			addresses[i] = fmt.Sprintf("10.0.0.%d", i+1)
		}
		key := rapid.StringMatching(`[a-zA-Z0-9_-]{1,32}`).Draw(rt, "key")

		// Two independent replicas fed the same slice in different orders
		// (rapid permutation) must agree on the pick.
		perm := rapid.Permutation(addresses).Draw(rt, "perm")
		ixA, ixB := NewIndex(), NewIndex()
		ixA.ApplySlice(slice("s1", "fn-a", "default", 8888, addresses...))
		ixB.ApplySlice(slice("s1", "fn-a", "default", 8888, perm...))

		a := admitOnce(rt, ixA, []string{key})[key]
		b := admitOnce(rt, ixB, []string{key})[key]
		if a != b {
			rt.Fatalf("S4 violated: replica A picked %s, replica B picked %s", a, b)
		}
	})
}

func TestStickyMinimalReshuffle(t *testing.T) {
	t.Parallel()
	const nKeys = 300
	keys := make([]string, nKeys)
	for i := range nKeys {
		keys[i] = fmt.Sprintf("key-%03d", i)
	}
	pods := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"}

	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "default", 8888, pods...))
	before := admitOnce(t, ix, keys)

	t.Run("removal moves only the removed pod's keys", func(t *testing.T) {
		ix2 := NewIndex()
		ix2.ApplySlice(slice("s1", "fn-a", "default", 8888, pods[:4]...)) // drop 10.0.0.5
		after := admitOnce(t, ix2, keys)
		for _, k := range keys {
			if before[k] == "10.0.0.5:8888" {
				assert.NotEqual(t, "10.0.0.5:8888", after[k], "key %s must move off the removed pod", k)
			} else {
				assert.Equal(t, before[k], after[k], "S5 violated: key %s moved without cause", k)
			}
		}
	})

	t.Run("addition moves only keys that now rank the new pod first", func(t *testing.T) {
		ix2 := NewIndex()
		ix2.ApplySlice(slice("s1", "fn-a", "default", 8888, append(append([]string{}, pods...), "10.0.0.6")...))
		after := admitOnce(t, ix2, keys)
		for _, k := range keys {
			if after[k] != "10.0.0.6:8888" {
				assert.Equal(t, before[k], after[k], "S5 violated: key %s moved to a pre-existing pod", k)
			}
		}
	})
}

func TestStickyDistributionBalance(t *testing.T) {
	t.Parallel()
	const nKeys = 10000
	for _, pods := range []int{2, 3, 5, 8} {
		addresses := make([]string, pods)
		for i := range pods {
			addresses[i] = fmt.Sprintf("10.0.1.%d", i+1)
		}
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, addresses...))

		counts := map[string]int{}
		for i := range nKeys {
			ep, release, res := ix.Admit("default", "fn-a", nKeys, fmt.Sprintf("bal-%05d", i))
			require.Equal(t, Admitted, res)
			release()
			counts[ep.Address]++
		}
		expected := float64(nKeys) / float64(pods)
		for addr, c := range counts {
			assert.InDeltaf(t, expected, float64(c), expected*0.25,
				"%d pods: address %s got %d of %d keys (>±25%% off fair share)", pods, addr, c, nKeys)
		}
	}
}

// TestStickySaturationOverflow: a saturated sticky winner overflows to the
// next-ranked admissible endpoint (S6 — never a queue, never a refusal while
// capacity exists), and the release/accounting seam is unchanged.
func TestStickySaturationOverflow(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))

	const key = "session-owner"
	ep1, release1, res := ix.Admit("default", "fn-a", 1, key)
	require.Equal(t, Admitted, res)

	// Same key while its owner is at capacity: the OTHER pod admits.
	ep2, release2, res := ix.Admit("default", "fn-a", 1, key)
	require.Equal(t, Admitted, res)
	assert.NotEqual(t, ep1.Address, ep2.Address, "saturated sticky target must overflow, not double-book")

	// Both saturated: AllBusy, same as the default pick.
	_, _, res = ix.Admit("default", "fn-a", 1, key)
	assert.Equal(t, AllBusy, res)

	release1()
	release2()
	ep3, release3, res := ix.Admit("default", "fn-a", 1, key)
	require.Equal(t, Admitted, res)
	assert.Equal(t, ep1.Address, ep3.Address, "capacity restored: the key returns to its HRW owner")
	release3()
}

// TestStickyEmptyKeyKeepsLeastOutstanding: no key = today's pick, bit for bit.
func TestStickyEmptyKeyKeepsLeastOutstanding(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))

	ep1, r1, res := ix.Admit("default", "fn-a", 4, "")
	require.Equal(t, Admitted, res)
	ep2, r2, res := ix.Admit("default", "fn-a", 4, "")
	require.Equal(t, Admitted, res)
	assert.NotEqual(t, ep1.Address, ep2.Address, "least-outstanding spreads load")
	r1()
	r2()
}

// TestStickyChurnRace runs Admit with keys against concurrent slice churn
// under -race (the build-vs-serve race family the gorilla Methods() bite
// documented lives here).
func TestStickyChurnRace(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2", "10.0.0.3"))

	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Go(func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			if i%2 == 0 {
				ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))
			} else {
				ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"))
			}
		}
	})

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 500 {
				key := fmt.Sprintf("churn-%d-%d", w, i%17)
				_, release, res := ix.Admit("default", "fn-a", 100, key)
				if res == Admitted {
					release()
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	churn.Wait()
}
