// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type CacheKeyUR struct {
	UID             types.UID
	ResourceVersion string
}

func (ck CacheKeyUR) String() string {
	return fmt.Sprintf("%v_%v", ck.UID, ck.ResourceVersion)
}

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

// CacheKeyURFromMeta : Given metadata, create a key that uniquely identifies the contents
// of the object. Since resourceVersion changes on every update and
// UIDs are unique, uid+resourceVersion identifies the
// content. (ResourceVersion may also update on status updates, so
// this will result in some unnecessary cache misses. That should be
// ok.)
func CacheKeyURFromMeta(metadata *metav1.ObjectMeta) CacheKeyUR {
	return CacheKeyUR{
		UID:             metadata.UID,
		ResourceVersion: metadata.ResourceVersion,
	}
}

func CacheKeyURFromObject(obj metav1.Object) CacheKeyUR {
	return CacheKeyUR{
		UID:             obj.GetUID(),
		ResourceVersion: obj.GetResourceVersion(),
	}
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
