// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// makeSlice builds a Fission-managed EndpointSlice in the given namespace.
func makeSlice(name, ns, fnName string, addrs ...string) *discoveryv1.EndpointSlice {
	port := int32(8888)
	ready := true
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      fnName,
				fv1.FUNCTION_NAMESPACE: ns,
				fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
			},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
	}
	for _, a := range addrs {
		es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{a},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	return es
}

// TestNamespaceInformersSyncStartsAndStops verifies that Sync starts informers
// for new namespaces and stops them for removed ones, feeding the Index from
// the fake client's pre-populated EndpointSlices. Uses WaitForCacheSync as the
// deterministic sync point (no polling). Close is registered via t.Cleanup so
// informer goroutines don't outlive the test.
func TestNamespaceInformersSyncStartsAndStops(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(
		makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"),
		makeSlice("s2", "ns-b", "fn-b", "10.0.0.2"),
	)
	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)

	// Start informers for both namespaces.
	nsi.Sync([]string{"ns-a", "ns-b"})
	require.True(t, nsi.WaitForCacheSync(t.Context()), "both informers should sync")
	waitForIndexSize(t, index, 2)

	// Both functions must be in the index — proves the informers fed it.
	waitForEndpoints(t, index, "ns-a", "fn-a", "10.0.0.1:8888")
	waitForEndpoints(t, index, "ns-b", "fn-b", "10.0.0.2:8888")

	// Stop ns-b's informer; its entries must be swept from the index.
	// Sync waits for the informer goroutine to exit before sweeping, so the
	// index state is deterministic after Sync returns.
	nsi.Sync([]string{"ns-a"})
	assert.Empty(t, index.Lookup("ns-b", "fn-b", ""), "ns-b entries must be removed after stop")
	assert.NotEmpty(t, index.Lookup("ns-a", "fn-a", ""), "ns-a must survive")
	assert.Equal(t, 1, index.Size(), "only ns-a remains after ns-b offboard")
}

// TestNamespaceInformersSyncIdempotent verifies that calling Sync with the
// same namespace set twice does not double-feed the index. Asserted through
// the exported Index.Size() (observable behavior), not by inspecting private
// informer map state.
func TestNamespaceInformersSyncIdempotent(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)

	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()), "informer should sync")
	waitForIndexSize(t, index, 1)

	nsi.Sync([]string{"ns-a"}) // same set, no-op
	assert.Equal(t, 1, index.Size(), "second Sync with same set must not double-feed")
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(index.Lookup("ns-a", "fn-a", "")))
}

// TestNamespaceInformersHasSyncedEmpty verifies HasSynced returns true when
// no informers are running (vacuously true — nothing to sync).
func TestNamespaceInformersHasSyncedEmpty(t *testing.T) {
	t.Parallel()
	nsi := NewNamespaceInformers(fake.NewSimpleClientset(), NewIndex(), logr.Discard())
	t.Cleanup(nsi.Close)
	assert.True(t, nsi.HasSynced(), "empty informer set is vacuously synced")
}

// waitForIndexSize waits for the index to reach size n. WaitForCacheSync only
// guarantees the reflector's initial LIST is stored; the Add notifications
// still travel through the informer's async processor before ApplySlice runs,
// so index state is genuinely eventual-convergence after a sync (bounded here
// — never a bare sleep).
//
// Size() alone is NOT a sufficient sync point for asserting on endpoints:
// ApplySlice creates the entry and bumps ix.size under the SHARD lock, releases
// it, and only then fills e.slices and rebuilds under the ENTRY lock. A poller
// can therefore observe the entry while Lookup still returns nothing. Use
// waitForEndpoints whenever the next assertion is about addresses.
func waitForIndexSize(t *testing.T, index *Index, n int) {
	t.Helper()
	require.Eventuallyf(t, func() bool {
		return index.Size() == n
	}, 10*time.Second, 10*time.Millisecond, "index size never converged to %d", n)
}

// waitForEndpoints waits until the function's endpoint set matches want. It
// polls the very thing being asserted, so unlike waitForIndexSize it cannot
// observe the half-applied entry described above.
func waitForEndpoints(t *testing.T, index *Index, ns, fn string, want ...string) {
	t.Helper()
	require.EventuallyWithTf(t, func(c *assert.CollectT) {
		assert.ElementsMatch(c, want, addrs(index.Lookup(ns, fn, "")))
	}, 10*time.Second, 10*time.Millisecond, "endpoints for %s/%s never converged to %v", ns, fn, want)
}

// blockingStopWatch lets a test hold a real informer inside watcher shutdown.
// Stop reports that shutdown began, then waits until the test releases it.
type blockingStopWatch struct {
	result      chan watch.Event
	stopStarted chan struct{}
	releaseStop <-chan struct{}
	stopOnce    sync.Once
}

func (w *blockingStopWatch) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopStarted)
		<-w.releaseStop
		close(w.result)
	})
}

func (w *blockingStopWatch) ResultChan() <-chan watch.Event {
	return w.result
}

// TestNamespaceInformersCloseStopsAll verifies that Close stops every running
// informer goroutine and sweeps nothing (Close is lifecycle-only; index
// sweeping is Sync's job). After Close, the index is still queryable but no
// new events arrive.
func TestNamespaceInformersCloseStopsAll(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())

	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()), "informer should sync")
	waitForIndexSize(t, index, 1)

	// Close must return (not hang) — proves the done channel fires.
	nsi.Close()

	// Index state survives Close (Close doesn't sweep).
	assert.Equal(t, 1, index.Size(), "Close must not sweep the index")
}

// TestNamespaceInformersSyncAfterCloseIsNoop verifies the closed guard: after
// Close, a racing Sync must not repopulate informers. Without the guard, Close
// drains the map outside the lock, a racing Sync repopulates it, and those new
// informers — parented on context.Background() — are unreachable by any cancel
// and leak until process exit. With the guard, Sync returns immediately and the
// informer map stays empty.
func TestNamespaceInformersSyncAfterCloseIsNoop(t *testing.T) {
	t.Parallel()

	releaseList := make(chan struct{})

	kubeClient := fake.NewSimpleClientset(
		makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"),
		makeSlice("s2", "ns-b", "fn-b", "10.0.0.2"),
	)

	kubeClient.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ns := action.GetNamespace()
		switch ns {
		case "ns-a":
			return false, nil, nil
		case "ns-b":
			<-releaseList
			return false, nil, nil
		}
		return false, nil, nil
	})

	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())

	// Start an informer for ns-a.
	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	waitForIndexSize(t, index, 1)

	releaseFunc := sync.OnceFunc(func() {
		close(releaseList)
	})

	// Close: stops ns-a informer, sets closed=true.
	t.Cleanup(nsi.Close)
	t.Cleanup(releaseFunc)

	nsi.Close()

	// Sync with ns-b: must be a no-op because closed=true.
	// Without the guard, this would start an informer for ns-b.
	nsi.Sync([]string{"ns-b"})

	// ns-b's slice must NOT be in the index — Sync was a no-op.
	assert.Equal(t, 1, index.Size(), "Sync after Close must not start informers")
	assert.NotEmpty(t, index.Lookup("ns-a", "fn-a", ""), "ns-a entries survive Close")
	assert.Empty(t, index.Lookup("ns-b", "fn-b", ""), "ns-b must not appear — Sync was a no-op")
	assert.True(t, nsi.HasSynced(), "HasSynced vacuously true after Close (zero informers)")
}

// TestNamespaceInformersOffboardReonboard exercises the PRD §11.1 lifecycle:
// onboard → offboard → re-onboard the same namespace. After the fence, a
// re-onboarded informer must deliver fresh entries, and a final offboard must
// leave the index empty — no stale resurrection, no overlap.
func TestNamespaceInformersOffboardReonboard(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)

	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	waitForIndexSize(t, index, 1)

	// Offboard: Sync blocks until the informer drains, so the sweep is final.
	nsi.Sync([]string{})
	assert.Equal(t, 0, index.Size(), "offboard must sweep all entries")

	// Re-onboard: a fresh informer replays the slice.
	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	waitForIndexSize(t, index, 1)
	waitForEndpoints(t, index, "ns-a", "fn-a", "10.0.0.1:8888")

	// Final offboard leaves nothing behind.
	nsi.Sync([]string{})
	assert.Equal(t, 0, index.Size())
}

// TestNamespaceInformersConcurrentSync hammers Sync from multiple goroutines
// with alternating desired sets (offboard/re-onboard overlap). Under -race
// this exercises the serialization: the final Sync must leave exactly the
// desired entries, never a stale or duplicated set.
func TestNamespaceInformersConcurrentSync(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)

	const workers = 4
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range 10 {
				nsi.Sync([]string{"ns-a"})
				nsi.Sync([]string{})
			}
		})
	}
	wg.Wait()

	// Converge on ns-a: the fence guarantees the surviving informer is the
	// only writer, and its replay yields exactly one entry.
	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	waitForIndexSize(t, index, 1)
	waitForEndpoints(t, index, "ns-a", "fn-a", "10.0.0.1:8888")
}

// TestNamespaceInformersFencePreventsResurrection verifies that the real done
// channel does not fire until the real informer has fully shut down. Moving
// close(done) before factory.Shutdown would let Sync sweep the namespace and
// return while the watcher can still deliver a late event and resurrect it.
func TestNamespaceInformersFencePreventsResurrection(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	watchStarted := make(chan struct{})
	stopStarted := make(chan struct{})
	releaseStop := make(chan struct{})
	blockingWatch := &blockingStopWatch{
		result:      make(chan watch.Event),
		stopStarted: stopStarted,
		releaseStop: releaseStop,
	}
	var watchStartOnce sync.Once
	kubeClient.PrependWatchReactor("endpointslices", func(action k8stesting.Action) (bool, watch.Interface, error) {
		watchStartOnce.Do(func() { close(watchStarted) })
		return true, blockingWatch, nil
	})

	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)
	release := sync.OnceFunc(func() { close(releaseStop) })
	t.Cleanup(release)

	// Start the real informer and wait until its initial list has populated
	// the index and its watch connection is active.
	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	waitForIndexSize(t, index, 1)
	require.Eventually(t, func() bool {
		select {
		case <-watchStarted:
			return true
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "informer watch never started")

	// Offboard in the background. Cancelling the informer reaches the real
	// watch's Stop method, which remains blocked until release is called.
	syncDone := make(chan struct{})
	go func() {
		nsi.Sync([]string{})
		close(syncDone)
	}()

	require.Eventually(t, func() bool {
		select {
		case <-stopStarted:
			return true
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "watcher shutdown never started")

	// Since the real watcher has not finished stopping, Sync must still be
	// waiting and the namespace must not have been swept yet.
	select {
	case <-syncDone:
		t.Fatal("Sync completed before the watcher fully stopped")
	default:
	}
	assert.Equal(t, 1, index.Size(), "index must not be swept while the watcher is still stopping")

	// Let watcher shutdown finish. Only then may Sync sweep and return.
	release()
	require.Eventually(t, func() bool {
		select {
		case <-syncDone:
			return true
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "Sync did not finish after watcher shutdown")

	assert.Equal(t, 0, index.Size(), "index must be empty after watcher shutdown and sweep")
}

// TestNamespaceInformersCreateUpdateDeleteThroughInformer exercises
// Create→Update→Delete through the fake client's informer/watch path —
// the full event lifecycle the shared handlers in handlers.go must handle.
// The initial LIST replay exercises AddFunc; this test additionally exercises
// UpdateFunc (via REST Update) and DeleteFunc (via REST Delete), asserting
// the index converges after each step.
func TestNamespaceInformersCreateUpdateDeleteThroughInformer(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)

	// Create: initial LIST replay through the informer (AddFunc).
	nsi.Sync([]string{"ns-a"})
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	waitForIndexSize(t, index, 1)
	waitForEndpoints(t, index, "ns-a", "fn-a", "10.0.0.1:8888")

	// Update: REST Update triggers MODIFIED → UpdateFunc → ApplySlice.
	updated := makeSlice("s1", "ns-a", "fn-a", "10.0.0.2", "10.0.0.3")
	_, err := kubeClient.DiscoveryV1().EndpointSlices("ns-a").Update(t.Context(), updated, metav1.UpdateOptions{})
	require.NoError(t, err)
	// Size stays 1 — same function; only the endpoint set changes.
	waitForEndpoints(t, index, "ns-a", "fn-a", "10.0.0.2:8888", "10.0.0.3:8888")

	// Delete: REST Delete triggers DELETED → DeleteFunc → DeleteSlice.
	err = kubeClient.DiscoveryV1().EndpointSlices("ns-a").Delete(t.Context(), "s1", metav1.DeleteOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return index.Size() == 0 }, 10*time.Second, 10*time.Millisecond,
		"delete must remove the function entry from the index")
	assert.Empty(t, index.Lookup("ns-a", "fn-a", ""))
}

// TestEndpointSliceHandlersUpdateAndDelete verifies the extracted UpdateFunc
// and DeleteFunc handlers in handlers.go — including the tombstone unwrap —
// by calling the handler functions directly. Covers the unit-level logic that
// the informer-level test above exercises end-to-end.
func TestEndpointSliceHandlersUpdateAndDelete(t *testing.T) {
	t.Parallel()

	index := NewIndex()
	handlers := endpointSliceHandlers(index, logr.Discard())

	s1 := makeSlice("s1", "ns-a", "fn-a", "10.0.0.1")

	// Add: same path as the informer's initial LIST replay.
	handlers.AddFunc(s1)
	assert.Equal(t, 1, index.Size())
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(index.Lookup("ns-a", "fn-a", "")))

	// Update: UpdateFunc must call ApplySlice with the new endpoints.
	updated := makeSlice("s1", "ns-a", "fn-a", "10.0.0.2", "10.0.0.3")
	handlers.UpdateFunc(s1, updated)
	assert.Equal(t, 1, index.Size(), "update must not change function count")
	assert.ElementsMatch(t, []string{"10.0.0.2:8888", "10.0.0.3:8888"}, addrs(index.Lookup("ns-a", "fn-a", "")))

	// Delete: DeleteFunc must call DeleteSlice, removing the entry.
	handlers.DeleteFunc(updated)
	assert.Equal(t, 0, index.Size(), "delete must remove the function entry")
	assert.Empty(t, index.Lookup("ns-a", "fn-a", ""))

	// Tombstone unwrap: when the informer's store loses the object, the
	// reflector wraps it in a DeletedFinalStateUnknown. DeleteFunc must
	// unwrap the tombstone and call DeleteSlice on the real object.
	handlers.AddFunc(s1)
	assert.Equal(t, 1, index.Size())
	handlers.DeleteFunc(cache.DeletedFinalStateUnknown{Key: "ns-a/s1", Obj: s1})
	assert.Equal(t, 0, index.Size(), "tombstone delete must also remove the entry")
}

// TestNamespaceInformersHasSyncedNonEmpty verifies the HasSynced state
// machine: false while an informer is running but hasn't completed its
// initial LIST, true after it syncs. Uses a list reactor that blocks the
// initial LIST until released, so the test can observe the false state
// deterministically.
func TestNamespaceInformersHasSyncedNonEmpty(t *testing.T) {
	t.Parallel()

	kubeClient := fake.NewSimpleClientset(makeSlice("s1", "ns-a", "fn-a", "10.0.0.1"))
	// Block the initial LIST so HasSynced stays false until we release it.
	releaseList := make(chan struct{})
	kubeClient.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		<-releaseList
		return false, nil, nil // fall through to default reactor
	})

	index := NewIndex()
	nsi := NewNamespaceInformers(kubeClient, index, logr.Discard())
	t.Cleanup(nsi.Close)

	// Start the informer — LIST is blocked, so HasSynced must be false.
	nsi.Sync([]string{"ns-a"})
	assert.False(t, nsi.HasSynced(), "HasSynced must be false while initial LIST is blocked")

	// Release the LIST; the informer syncs.
	close(releaseList)
	require.True(t, nsi.WaitForCacheSync(t.Context()))
	assert.True(t, nsi.HasSynced(), "HasSynced must be true after informer completes initial LIST")
}
