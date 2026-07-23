// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type CacheKeyUG struct {
	UID        types.UID
	Generation int64
}

func (ck CacheKeyUG) String() string {
	return fmt.Sprintf("%v_%v", ck.UID, ck.Generation)
}

// CacheKeyUIDFromMeta create a key that uniquely identifies the
// of the object. Since resourceVersion changes on every update and
// UIDs are unique, we don't use resource version here
func CacheKeyUIDFromMeta(metadata *metav1.ObjectMeta) types.UID {
	return metadata.UID
}

// CacheKeyUGFromMeta creates a cache key that uniquely identifies the
// function's content version. UID is stable for the function's
// lifetime; Generation increments on spec changes. ResourceVersion is
// intentionally excluded: it changes on status updates (not just spec
// changes), and the router's informer cache may lag the executor's
// view, causing UnTapService to miss the cache entry and leak
// activeRequests.
func CacheKeyUGFromMeta(metadata *metav1.ObjectMeta) CacheKeyUG {
	return CacheKeyUG{
		UID:        metadata.UID,
		Generation: metadata.Generation,
	}
}

// CacheKeyUGFromObject is CacheKeyUGFromMeta for callers that only have a
// metav1.Object (e.g. *fv1.Function passed as the dispatcher dedup key),
// not a bare ObjectMeta.
func CacheKeyUGFromObject(obj metav1.Object) CacheKeyUG {
	return CacheKeyUG{
		UID:        obj.GetUID(),
		Generation: obj.GetGeneration(),
	}
}
