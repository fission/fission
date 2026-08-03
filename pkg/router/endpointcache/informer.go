// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
)

// RegisterInformer feeds the index from the Manager-cached EndpointSlice
// informer (label-filtered and namespace-scoped via the Manager's cache
// options). The informer's initial LIST replays every existing slice as an Add,
// so the index is complete once the cache syncs — no executor involvement on
// router restart.
//
// It returns the registration's HasSynced. The Manager's own cache sync says
// the informer's store is populated, which is NOT the same as this handler
// having received the replay: registration-level sync is what tells you the
// index has actually seen every existing slice. Discarding it leaves a bounded
// window where a Ready replica serves warm traffic from a partially-filled
// index and falls back to the executor — correct, but a latency cliff exactly
// during a rolling upgrade (RFC-0028 §2).
func RegisterInformer(ctx context.Context, mgr ctrl.Manager, ix *Index, logger logr.Logger) (cache.InformerSynced, error) {
	informer, err := mgr.GetCache().GetInformer(ctx, &discoveryv1.EndpointSlice{})
	if err != nil {
		return nil, fmt.Errorf("error getting endpointslice informer: %w", err)
	}
	reg, err := informer.AddEventHandler(endpointSliceHandlers(ix, logger))
	if err != nil {
		return nil, fmt.Errorf("error adding endpointslice event handler: %w", err)
	}
	return reg.HasSynced, nil
}
