// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"fmt"
	"hash/fnv"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/fission/fission/pkg/utils/metrics/metricstest"
)

// RFC-0023 phase-3 sticky-pick properties. S4: the pick is a pure function
// of (key, ready endpoint set) — same inputs, same pod, on every router
// replica independently. S5: HRW's minimal reshuffle — removing a pod moves
// only its keys; adding one moves only keys now ranking it first. Stickiness
// is best-effort (S6): a saturated sticky target overflows to the
// next-ranked admissible endpoint, never queues.

// TestHrwScoreMatchesFNV64a pins hrwScore's inlined FNV-1a against the
// standard library's hash/fnv New64a over the identical write sequence (key,
// a single 0x00 separator byte, address): the inlining (heap-allocation fix,
// issue #3615) must be byte-for-byte identical, or existing sticky
// assignments would silently shift the moment it ships.
func TestHrwScoreMatchesFNV64a(t *testing.T) {
	t.Parallel()

	stdHrwScore := func(key, address string) uint64 {
		h := fnv.New64a()
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(address))
		return h.Sum64()
	}

	strs := []string{
		"",
		"a",
		"10.0.0.1:8888",
		"session-abc-1234567890",
		"тест-utf8",
		"a-fairly-long-sticky-key-with-several-words-in-it-1234567890",
		string([]byte{0x00, 0xff, 0x10, 0x7f}),
		string([]byte{0x00}),
	}
	for _, key := range strs {
		for _, address := range strs {
			assert.Equalf(t, stdHrwScore(key, address), hrwScore(key, address),
				"hrwScore(%q, %q)", key, address)
		}
	}
}

// TestHrwScoreAllocs guards the fix itself: hrwScore runs once per candidate
// endpoint per sticky-keyed Admit call, so any heap allocation here is a
// per-request cost multiplied by endpoint count.
func TestHrwScoreAllocs(t *testing.T) {
	var sink uint64
	allocs := testing.AllocsPerRun(1000, func() {
		sink = hrwScore("session-abc-1234567890", "10.0.0.1:8888")
	})
	assert.Zero(t, allocs, "hrwScore must not heap-allocate on the per-request hot path")
	benchSinkU64 = sink
}

// benchSinkU64 defeats dead-code elimination in the benchmarks below.
var benchSinkU64 uint64

// BenchmarkHrwScoreNew/Old bracket the fix: New exercises the shipped inline
// FNV-1a, Old inlines the previous fnv.New64a()-based implementation so a
// side-by-side allocs/op read shows the delta.
// Run: go test -run=^$ -bench=HrwScore -benchmem ./pkg/router/endpointcache/
func BenchmarkHrwScoreNew(b *testing.B) {
	var sink uint64
	b.ReportAllocs()
	for b.Loop() {
		sink = hrwScore("session-abc-1234567890", "10.0.0.1:8888")
	}
	benchSinkU64 = sink
}

func BenchmarkHrwScoreOld(b *testing.B) {
	var sink uint64
	b.ReportAllocs()
	for b.Loop() {
		// Previous implementation: fnv.New64a() heap-allocates the hasher
		// (behind the hash.Hash64 interface) on every call.
		h := fnv.New64a()
		_, _ = h.Write([]byte("session-abc-1234567890"))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte("10.0.0.1:8888"))
		sink = h.Sum64()
	}
	benchSinkU64 = sink
}

// admitAll drains one Admit per key and returns key->address, releasing every
// admission so load never influences the pick under test.
func admitOnce(t interface{ Fatalf(string, ...any) }, ix *Index, keys []string) map[string]string {
	got := make(map[string]string, len(keys))
	for _, k := range keys {
		ep, release, res := ix.Admit("default", "fn-a", "", 100, k)
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
			ep, release, res := ix.Admit("default", "fn-a", "", nKeys, fmt.Sprintf("bal-%05d", i))
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
	ep1, release1, res := ix.Admit("default", "fn-a", "", 1, key)
	require.Equal(t, Admitted, res)

	// Same key while its owner is at capacity: the OTHER pod admits.
	ep2, release2, res := ix.Admit("default", "fn-a", "", 1, key)
	require.Equal(t, Admitted, res)
	assert.NotEqual(t, ep1.Address, ep2.Address, "saturated sticky target must overflow, not double-book")

	// Both saturated: AllBusy, same as the default pick.
	_, _, res = ix.Admit("default", "fn-a", "", 1, key)
	assert.Equal(t, AllBusy, res)

	release1()
	release2()
	ep3, release3, res := ix.Admit("default", "fn-a", "", 1, key)
	require.Equal(t, Admitted, res)
	assert.Equal(t, ep1.Address, ep3.Address, "capacity restored: the key returns to its HRW owner")
	release3()
}

// TestNoteStickyPick pins the bucketed teleport detector: the first pick for
// a key is never a teleport, a repeat pick is not, a changed pick is, and
// bucket contention with a DIFFERENT key suppresses counting (undercount)
// instead of fabricating teleports from interleaved overwrites.
func TestNoteStickyPick(t *testing.T) {
	t.Parallel()
	e := &fnEntry{}

	assert.False(t, noteStickyPick(e, "session-1", "10.0.0.1:8888"), "first pick is not a teleport")
	assert.False(t, noteStickyPick(e, "session-1", "10.0.0.1:8888"), "repeat pick is not a teleport")
	assert.True(t, noteStickyPick(e, "session-1", "10.0.0.2:8888"), "changed pick is a teleport")
	assert.False(t, noteStickyPick(e, "session-1", "10.0.0.2:8888"), "sticky again after the move")

	// Find a second key sharing session-1's bucket with a different
	// fingerprint: the two overwriting each other must count nothing.
	kh := fnv1a32("session-1")
	var other string
	for i := 0; ; i++ {
		cand := fmt.Sprintf("other-%d", i)
		if ch := fnv1a32(cand); ch%stickyBuckets == kh%stickyBuckets && ch != kh {
			other = cand
			break
		}
	}
	assert.False(t, noteStickyPick(e, other, "10.0.0.3:8888"),
		"a different key overwriting a contested bucket is not a teleport")
	assert.False(t, noteStickyPick(e, "session-1", "10.0.0.2:8888"),
		"the original key after contention is suppressed (undercount), not miscounted")
}

// teleportCount reads fission_router_sticky_teleports_total for one function
// (0 when the series does not exist yet).
func teleportCount(t *testing.T, reg *prometheus.Registry, ns, fn string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, fam := range families {
		if fam.GetName() != "fission_router_sticky_teleports_total" {
			continue
		}
		if m := metricstest.FindMetric(fam, map[string]string{
			"function_namespace": ns,
			"function_name":      fn,
		}); m != nil {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// TestAdmitStickyTeleportMetric drives the teleport counter through Admit's
// exported surface end to end: a busy-owner overflow (and its snap-back)
// counts nothing, an endpoint loss that moves the key counts exactly once,
// and repeat owner admits stay flat.
func TestAdmitStickyTeleportMetric(t *testing.T) {
	reg := metricstest.SetupMeter(t)
	// Function coordinates unique to this test so concurrent tests recording
	// other series can't collide with the assertion below.
	const ns, fn = "default", "fn-teleport-metric"
	ix := NewIndex()
	ix.ApplySlice(slice("s1", fn, ns, 8888, "10.0.0.1", "10.0.0.2"))

	const key = "session-teleport"
	ep1, release1, res := ix.Admit(ns, fn, "", 1, key)
	require.Equal(t, Admitted, res)
	assert.Zero(t, teleportCount(t, reg, ns, fn), "first pick is not a teleport")

	// Busy owner: the same key overflows to the other pod — load shedding,
	// not a teleport — and the snap-back after release isn't one either.
	_, release2, res := ix.Admit(ns, fn, "", 1, key)
	require.Equal(t, Admitted, res)
	release2()
	release1()
	_, release3, res := ix.Admit(ns, fn, "", 1, key)
	require.Equal(t, Admitted, res)
	release3()
	assert.Zero(t, teleportCount(t, reg, ns, fn), "saturation overflow and snap-back must not count")

	// Drop the winner: the key's next admit lands on the survivor.
	survivor := "10.0.0.1"
	if ep1.Address == "10.0.0.1:8888" {
		survivor = "10.0.0.2"
	}
	ix.ApplySlice(slice("s1", fn, ns, 8888, survivor))
	_, release4, res := ix.Admit(ns, fn, "", 1, key)
	require.Equal(t, Admitted, res)
	release4()
	assert.Equal(t, float64(1), teleportCount(t, reg, ns, fn), "endpoint loss moves the key: exactly one teleport")

	_, release5, res := ix.Admit(ns, fn, "", 1, key)
	require.Equal(t, Admitted, res)
	release5()
	assert.Equal(t, float64(1), teleportCount(t, reg, ns, fn), "repeat owner admits stay flat")
}

// TestStickyEmptyKeyKeepsLeastOutstanding: no key = today's pick, bit for bit.
func TestStickyEmptyKeyKeepsLeastOutstanding(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))

	ep1, r1, res := ix.Admit("default", "fn-a", "", 4, "")
	require.Equal(t, Admitted, res)
	ep2, r2, res := ix.Admit("default", "fn-a", "", 4, "")
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
				_, release, res := ix.Admit("default", "fn-a", "", 100, key)
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
