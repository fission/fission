// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"github.com/go-logr/logr"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/tools/cache"
)

// endpointSliceHandlers returns the Add/Update/Delete handlers that feed the
// Index from EndpointSlice informer events. Shared by the manager-cache
// informer (informer.go) and the per-namespace informers (nsinformers.go) so
// the event-handling logic — including tombstone unwrapping — cannot drift
// between the two paths.
func endpointSliceHandlers(ix *Index, logger logr.Logger) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
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
	}
}
