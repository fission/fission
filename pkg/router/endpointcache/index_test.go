// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func slice(name, fnName, fnNamespace string, port int32, addrs ...string) *discoveryv1.EndpointSlice {
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "fn-ns",
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      fnName,
				fv1.FUNCTION_NAMESPACE: fnNamespace,
				fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
			},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
	}
	ready := true
	for i, a := range addrs {
		es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{a},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &apiv1.ObjectReference{Kind: "Pod", UID: types.UID(fmt.Sprintf("pod-%s-%d", a, i))},
		})
	}
	return es
}

func addrs(eps []Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.Address)
	}
	return out
}

func TestIndexApplyAndDelete(t *testing.T) {
	t.Parallel()
	ix := NewIndex()

	t.Run("slice add populates the function entry", func(t *testing.T) {
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))
		assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(ix.Lookup("default", "fn-a")))
		assert.Equal(t, 1, ix.ReadyCount("default", "fn-a"))
		assert.Equal(t, 1, ix.Size())
	})

	t.Run("multiple slices for one function merge", func(t *testing.T) {
		ix.ApplySlice(slice("s2", "fn-a", "default", 8888, "10.0.0.2", "10.0.0.3"))
		assert.ElementsMatch(t,
			[]string{"10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888"},
			addrs(ix.Lookup("default", "fn-a")))
		assert.Equal(t, 1, ix.Size(), "one function, regardless of slice count")
	})

	t.Run("slice update replaces only its own endpoints", func(t *testing.T) {
		ix.ApplySlice(slice("s2", "fn-a", "default", 8888, "10.0.0.9"))
		assert.ElementsMatch(t,
			[]string{"10.0.0.1:8888", "10.0.0.9:8888"},
			addrs(ix.Lookup("default", "fn-a")))
	})

	t.Run("slice delete removes its endpoints; last slice drops the entry", func(t *testing.T) {
		ix.DeleteSlice(slice("s2", "fn-a", "default", 8888))
		assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(ix.Lookup("default", "fn-a")))
		ix.DeleteSlice(slice("s1", "fn-a", "default", 8888))
		assert.Empty(t, ix.Lookup("default", "fn-a"))
		assert.Equal(t, 0, ix.Size())
	})
}

func TestIndexIgnoresNonFissionSlices(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	es := slice("s1", "fn-a", "default", 8888, "10.0.0.1")
	es.Labels = map[string]string{} // no function labels
	ix.ApplySlice(es)
	assert.Equal(t, 0, ix.Size())
}

func TestIndexNotReadyEndpoints(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	es := slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2")
	notReady := false
	es.Endpoints[1].Conditions.Ready = &notReady
	ix.ApplySlice(es)

	assert.Len(t, ix.Lookup("default", "fn-a"), 2, "not-ready endpoints stay visible (drain awareness)")
	assert.Equal(t, 1, ix.ReadyCount("default", "fn-a"))
}

func TestIndexNamespaceIsolation(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "ns1", 8888, "10.0.0.1"))
	ix.ApplySlice(slice("s2", "fn-a", "ns2", 8888, "10.0.0.2"))
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(ix.Lookup("ns1", "fn-a")))
	assert.ElementsMatch(t, []string{"10.0.0.2:8888"}, addrs(ix.Lookup("ns2", "fn-a")))
}

// TestIndexConcurrentReadersDuringEventStorm drives concurrent readers against
// a high-churn writer; the race detector is the assertion.
func TestIndexConcurrentReadersDuringEventStorm(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	const fns = 16

	var readers, writers sync.WaitGroup
	stop := make(chan struct{})
	for r := range 50 {
		readers.Go(func() {
			fn := fmt.Sprintf("fn-%d", r%fns)
			for {
				select {
				case <-stop:
					return
				default:
					_ = ix.Lookup("default", fn)
					_ = ix.ReadyCount("default", fn)
					_ = ix.Size()
				}
			}
		})
	}
	for w := range 8 {
		writers.Go(func() {
			for i := range 500 {
				fn := fmt.Sprintf("fn-%d", (w+i)%fns)
				s := slice(fmt.Sprintf("s-%d", w), fn, "default", 8888, fmt.Sprintf("10.0.%d.%d", w, i%250))
				if i%7 == 0 {
					ix.DeleteSlice(s)
				} else {
					ix.ApplySlice(s)
				}
			}
		})
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	require.True(t, true, "the race detector is the real assertion")
}

// TestAdmit covers the admission contract directly: least-outstanding
// selection, the requestsPerPod cap, release idempotency, and every named
// refusal reason.
func TestAdmit(t *testing.T) {
	t.Parallel()

	t.Run("no entry", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		_, release, result := ix.Admit("default", "ghost", 1, "")
		assert.Equal(t, NoEntry, result)
		assert.Nil(t, release)
	})

	t.Run("least-outstanding selection spreads load", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2", "10.0.0.3"))

		// Admit 3 slots at requestsPerPod=1 without releasing: each must land
		// on a DIFFERENT pod, or warm traffic would pile onto one pod while
		// others idle.
		seen := map[string]int{}
		for i := range 3 {
			ep, release, result := ix.Admit("default", "fn-a", 1, "")
			require.Equalf(t, Admitted, result, "admit %d", i)
			require.NotNil(t, release)
			seen[ep.Address]++
		}
		assert.Len(t, seen, 3, "3 admissions at cap 1 must use 3 distinct pods, got %v", seen)

		// Saturated now.
		_, release, result := ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, AllBusy, result)
		assert.Nil(t, release)
	})

	t.Run("requestsPerPod above one allows multiple slots per pod", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))

		var releases []func()
		for i := range 3 {
			_, release, result := ix.Admit("default", "fn-a", 3, "")
			require.Equalf(t, Admitted, result, "admit %d of 3 on one pod", i)
			releases = append(releases, release)
		}
		_, _, result := ix.Admit("default", "fn-a", 3, "")
		assert.Equal(t, AllBusy, result)

		// Releasing one slot re-opens admission.
		releases[0]()
		_, release, result := ix.Admit("default", "fn-a", 3, "")
		assert.Equal(t, Admitted, result)
		release()
	})

	t.Run("release is idempotent", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))

		_, release, result := ix.Admit("default", "fn-a", 1, "")
		require.Equal(t, Admitted, result)
		// The transport deliberately releases from two places (re-resolve and
		// the per-request defer); a double release driving the counter negative
		// would permanently over-admit a busy pod.
		release()
		release()
		release()

		_, r1, result := ix.Admit("default", "fn-a", 1, "")
		require.Equal(t, Admitted, result)
		_, _, result = ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, AllBusy, result, "counter must not have gone negative from double release")
		r1()
	})

	t.Run("quarantined endpoints are skipped, then all_quarantined", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))

		ix.Quarantine("default", "fn-a", "10.0.0.1:8888")
		ep, release, result := ix.Admit("default", "fn-a", 1, "")
		require.Equal(t, Admitted, result)
		assert.Equal(t, "10.0.0.2:8888", ep.Address, "quarantined endpoint must be skipped")
		release()

		ix.Quarantine("default", "fn-a", "10.0.0.2:8888")
		_, _, result = ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, AllQuarantined, result)

		// Any slice event lifts the quarantine.
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))
		_, release, result = ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, Admitted, result)
		release()
	})

	t.Run("quarantine expires after the TTL without a slice event", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.quarantineTTL = 50 * time.Millisecond
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))

		// The CI-observed outage mode: the function's ONLY endpoint gets
		// quarantined while the executor is down, so no slice event will ever
		// arrive to lift it. The TTL is the self-heal.
		ix.Quarantine("default", "fn-a", "10.0.0.1:8888")
		_, _, result := ix.Admit("default", "fn-a", 1, "")
		require.Equal(t, AllQuarantined, result)

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, release, result := ix.Admit("default", "fn-a", 1, "")
			if assert.Equal(c, Admitted, result, "quarantine must expire after the TTL") {
				release()
			}
		}, 2*time.Second, 10*time.Millisecond)

		// A dead pod is simply re-quarantined by the next dial failure.
		ix.Quarantine("default", "fn-a", "10.0.0.1:8888")
		_, _, result = ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, AllQuarantined, result)
	})

	t.Run("not-ready endpoints are never admitted", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		es := slice("s1", "fn-a", "default", 8888, "10.0.0.1")
		notReady := false
		es.Endpoints[0].Conditions.Ready = &notReady
		ix.ApplySlice(es)

		_, release, result := ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, NoCountedReady, result)
		assert.Nil(t, release)
	})

	t.Run("concurrent admits never exceed the per-pod cap", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1", "10.0.0.2"))

		const goroutines = 50
		const perPod = 3 // 2 pods × 3 = 6 admissible slots
		var admitted, refused atomic.Int64
		var wg sync.WaitGroup
		for range goroutines {
			wg.Go(func() {
				_, release, result := ix.Admit("default", "fn-a", perPod, "")
				switch result {
				case Admitted:
					admitted.Add(1)
					_ = release // hold the slot: count peak concurrency
				default:
					refused.Add(1)
				}
			})
		}
		wg.Wait()
		assert.LessOrEqual(t, admitted.Load(), int64(2*perPod),
			"admissions must never exceed pods×requestsPerPod")
		assert.Equal(t, int64(goroutines), admitted.Load()+refused.Load())
	})
}

// TestReportDialTimeout pins the strike escalation: a dial timeout is how a
// saturated-but-alive pod presents, so soft failures must not quarantine a
// function's only endpoint until the strike limit is reached within one TTL
// window.
func TestReportDialTimeout(t *testing.T) {
	t.Parallel()

	t.Run("quarantines only at the strike limit", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))

		for i := 1; i < dialTimeoutStrikeLimit; i++ {
			assert.Falsef(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"), "strike %d must not quarantine", i)
			_, release, result := ix.Admit("default", "fn-a", 1, "")
			require.Equalf(t, Admitted, result, "endpoint must stay admissible after strike %d", i)
			release()
		}
		assert.True(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"), "the limit-th strike escalates")
		_, _, result := ix.Admit("default", "fn-a", 1, "")
		assert.Equal(t, AllQuarantined, result)
	})

	t.Run("strikes lapse with the window", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.quarantineTTL = 30 * time.Millisecond
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))

		require.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"))
		require.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"))
		time.Sleep(60 * time.Millisecond)
		// The window lapsed: the count restarts, so this is strike 1 again.
		assert.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"),
			"stale strikes must not accumulate across windows")
	})

	t.Run("slice events clear pending strikes", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))

		require.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"))
		require.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"))
		ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))
		assert.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"),
			"a slice event resets the strike count")
	})

	t.Run("unknown function is a no-op", func(t *testing.T) {
		t.Parallel()
		ix := NewIndex()
		assert.False(t, ix.ReportDialTimeout("default", "nope", "10.0.0.1:8888"))
	})
}

func TestReportDialTimeoutIgnoredWhileQuarantined(t *testing.T) {
	t.Parallel()
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-a", "default", 8888, "10.0.0.1"))
	ix.Quarantine("default", "fn-a", "10.0.0.1:8888")

	// In-flight requests keep timing out after the quarantine stores; those
	// reports must not bank strikes that outlive the quarantine window.
	for range dialTimeoutStrikeLimit + 2 {
		assert.False(t, ix.ReportDialTimeout("default", "fn-a", "10.0.0.1:8888"))
	}
}
