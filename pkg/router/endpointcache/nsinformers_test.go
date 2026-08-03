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
	"k8s.io/client-go/kubernetes/fake"

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
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(index.Lookup("ns-a", "fn-a", "")))
	assert.ElementsMatch(t, []string{"10.0.0.2:8888"}, addrs(index.Lookup("ns-b", "fn-b", "")))

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
func waitForIndexSize(t *testing.T, index *Index, n int) {
	t.Helper()
	require.Eventuallyf(t, func() bool {
		return index.Size() == n
	}, 10*time.Second, 10*time.Millisecond, "index size never converged to %d", n)
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
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(index.Lookup("ns-a", "fn-a", "")))

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
	assert.ElementsMatch(t, []string{"10.0.0.1:8888"}, addrs(index.Lookup("ns-a", "fn-a", "")))
}
