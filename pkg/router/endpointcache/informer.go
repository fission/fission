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
func RegisterInformer(ctx context.Context, mgr ctrl.Manager, ix *Index, logger logr.Logger) error {
	informer, err := mgr.GetCache().GetInformer(ctx, &discoveryv1.EndpointSlice{})
	if err != nil {
		return fmt.Errorf("error getting endpointslice informer: %w", err)
	}
	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if es, ok := obj.(*discoveryv1.EndpointSlice); ok {
				ix.ApplySlice(es)
			}
		},
		UpdateFunc: func(_, newObj any) {
			if es, ok := newObj.(*discoveryv1.EndpointSlice); ok {
				ix.ApplySlice(es)
			}
		},
		DeleteFunc: func(obj any) {
			es, ok := obj.(*discoveryv1.EndpointSlice)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					logger.V(1).Info("unexpected object type in endpointslice delete event")
					return
				}
				es, ok = tombstone.Obj.(*discoveryv1.EndpointSlice)
				if !ok {
					logger.V(1).Info("unexpected tombstone object type in endpointslice delete event")
					return
				}
			}
			ix.DeleteSlice(es)
		},
	})
	if err != nil {
		return fmt.Errorf("error adding endpointslice event handler: %w", err)
	}
	return nil
}
