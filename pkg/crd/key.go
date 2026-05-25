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

type CacheKeyURG struct {
	UID             types.UID
	ResourceVersion string
	Generation      int64
}

func (ck CacheKeyURG) String() string {
	return fmt.Sprintf("%v_%v_%v", ck.UID, ck.ResourceVersion, ck.Generation)
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

// CacheKeyURGFromMeta : Given metadata, create a key that uniquely identifies the contents
// of the object. Since resourceVersion changes on every update and
// UIDs are unique, uid+resourceVersion identifies the
// content.
// Generation is also included to identify latest generation of the object.
func CacheKeyURGFromMeta(metadata *metav1.ObjectMeta) CacheKeyURG {
	return CacheKeyURG{
		UID:             metadata.UID,
		ResourceVersion: metadata.ResourceVersion,
		Generation:      metadata.Generation,
	}
}
