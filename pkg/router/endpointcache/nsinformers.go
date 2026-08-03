// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"context"
	"fmt"
	"sync"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// nsInformer pairs one namespace's EndpointSlice informer with the cancel
// function that stops it and a done channel closed only after the informer's
// goroutines have fully drained (factory.Start + factory.Shutdown). Stored in
// NamespaceInformers.informers, keyed by namespace. Waiting on done before
// sweeping the index is the teardown fence (PRD §11.1): it guarantees no queued
// or in-flight ApplySlice can repopulate a namespace after offboard.
type nsInformer struct {
	informer cache.SharedIndexInformer
	cancel   context.CancelFunc
	done     chan struct{}
}

// NamespaceInformers manages one EndpointSlice informer per tenant namespace.
// Unlike the controller-runtime Manager's cache (scoped once at startup),
// this can start/stop informers as tenants are onboarded/offboarded at runtime.
// Close stops every running informer and waits for their goroutines to exit;
// tests must call it (typically via t.Cleanup) to avoid leaking goroutines.
type NamespaceInformers struct {
	kubeClient kubernetes.Interface
	index      *Index
	logger     logr.Logger
	mu         sync.Mutex
	informers  map[string]*nsInformer
}

// NewNamespaceInformers creates an empty informer manager. The caller owns
// the lifecycle: call Close when done (production ties it to the router's
// context; tests register it via t.Cleanup).
func NewNamespaceInformers(kubeClient kubernetes.Interface, index *Index, logger logr.Logger) *NamespaceInformers {
	return &NamespaceInformers{
		kubeClient: kubeClient,
		index:      index,
		logger:     logger,
		informers:  make(map[string]*nsInformer),
	}
}

// Sync reconciles the running informer set to match namespaces:
// starts informers for new namespaces, stops informers for removed namespaces.
// The whole transition is serialized under n.mu: cancel → wait for the
// informer's goroutines to drain (factory.Shutdown via the done channel) →
// sweep the index. Holding the lock across the wait prevents a concurrent
// Sync from installing a replacement informer whose fresh entries the earlier
// call would then delete (offboard/re-onboard overlap). The drain never takes
// n.mu (handlers only touch the Index's own locks), so this cannot deadlock —
// other callers just wait a moment.
func (n *NamespaceInformers) Sync(namespaces []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Build a set of desired namespaces.
	desired := make(map[string]struct{}, len(namespaces))
	for _, ns := range namespaces {
		desired[ns] = struct{}{}
	}
	// Stop informers for namespaces no longer in the desired set. Waiting for
	// the drain BEFORE sweeping is the teardown fence (PRD §11.1): no queued
	// ApplySlice from the dying informer can repopulate the namespace after
	// RemoveNamespace, resurrecting stale endpoints during offboard.
	for ns, ni := range n.informers {
		if _, ok := desired[ns]; !ok {
			ni.cancel()
			<-ni.done
			n.index.RemoveNamespace(ns)
			delete(n.informers, ns)
			n.logger.Info("stopped endpointslice informer for namespace", "namespace", ns)
		}
	}
	// Start informers for new namespaces.
	for ns := range desired {
		if _, ok := n.informers[ns]; ok {
			continue // already running
		}
		ctx, cancel := context.WithCancel(context.Background())
		informer, done, err := n.start(ctx, ns)
		if err != nil {
			cancel()
			n.logger.Error(err, "failed to start endpointslice informer", "namespace", ns)
			continue
		}
		n.informers[ns] = &nsInformer{informer: informer, cancel: cancel, done: done}
		n.logger.Info("started endpointslice informer for namespace", "namespace", ns)
	}
}

// start builds and runs a single-namespace EndpointSlice informer that feeds
// slice add/update/delete events into the shared Index. The informer runs in a
// background goroutine via factory.Start; cancelling ctx stops it. Returns the
// informer (for HasSynced queries) and a done channel closed when the factory
// goroutine has fully exited.
func (n *NamespaceInformers) start(ctx context.Context, ns string) (cache.SharedIndexInformer, chan struct{}, error) {
	// Label filter: only Fission-managed slices (same selector as routerCacheOptions).
	labelSelector := labels.SelectorFromSet(labels.Set{
		fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
	}).String()
	factory := informers.NewSharedInformerFactoryWithOptions(
		n.kubeClient,
		0, // no resync — the index is the source of truth
		informers.WithNamespace(ns),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = labelSelector
		}))
	informer := factory.Discovery().V1().EndpointSlices().Informer()

	// Shared handlers (same as the manager-cache informer path).
	_, err := informer.AddEventHandler(endpointSliceHandlers(n.index, n.logger))
	if err != nil {
		return nil, nil, fmt.Errorf("error adding endpointslice event handler: %w", err)
	}
	// Run the factory in the background; cancelling ctx stops it. Start is
	// non-blocking (it only launches informer goroutines), so done must wait on
	// Shutdown, which blocks until every informer's Run has returned — and the
	// informer's processor drains all queued handler notifications before Run
	// returns. Closing done after Shutdown is therefore a real teardown fence:
	// once done fires, no handler for this namespace can ever run again.
	done := make(chan struct{})
	go func() {
		factory.Start(ctx.Done())
		factory.Shutdown()
		close(done)
	}()
	return informer, done, nil
}

// HasSynced reports whether all running informers have completed their initial
// list. Satisfies cache.InformerSynced (func() bool) so the router can gate
// readiness on the index being populated, same as the manager-cache path.
// Returns true when no informers are running (vacuously synced).
func (n *NamespaceInformers) HasSynced() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ni := range n.informers {
		if !ni.informer.HasSynced() {
			return false
		}
	}
	return true
}

// WaitForCacheSync blocks until all running informers have completed their
// initial list or ctx is cancelled. Returns true if all synced, false on
// timeout/cancel. This is the deterministic synchronization point for tests
// (replacing polling) and for the router's readiness gate.
func (n *NamespaceInformers) WaitForCacheSync(ctx context.Context) bool {
	n.mu.Lock()
	informers := make([]cache.InformerSynced, 0, len(n.informers))
	for _, ni := range n.informers {
		informers = append(informers, ni.informer.HasSynced)
	}
	n.mu.Unlock()
	return cache.WaitForCacheSync(ctx.Done(), informers...)
}

// Close stops every running informer and waits for their goroutines to exit.
// Safe to call multiple times. Production should call it when the router's
// context is cancelled; tests should register it via t.Cleanup.
func (n *NamespaceInformers) Close() {
	n.mu.Lock()
	stopped := n.informers
	n.informers = make(map[string]*nsInformer)
	n.mu.Unlock()
	for _, ni := range stopped {
		ni.cancel()
		<-ni.done
	}
}
